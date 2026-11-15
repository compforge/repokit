"""Domain-owned cleanup of Home records; Git and Forge remain authoritative."""

from __future__ import annotations

import logging
import shutil
import time
from dataclasses import dataclass, field
from pathlib import Path

from ..budget import check_cancelled
from .home import resolve_home
from .identity import digest_path
from .policy import CleanupPolicy, load_cleanup_policy
from .store import ObservationStore

log = logging.getLogger(__name__)


@dataclass(frozen=True)
class CacheCleanOptions:
    home: str | Path | None = None
    repository_key: str = ""
    clone_key: str = ""
    pr_number: int = 0
    policy: CleanupPolicy | None = None
    dry_run: bool = False
    expire: bool = False
    access_before: float = 0
    merged: frozenset[int] = frozenset()


@dataclass(frozen=True)
class CacheCleanReport:
    actions: list[dict[str, object]] = field(default_factory=list)
    counts: list[dict[str, object]] = field(default_factory=list)
    errors: list[str] = field(default_factory=list)
    affected: list[str] = field(default_factory=list)
    dry_run: bool = False


def _directories(path: Path) -> list[Path]:
    try:
        if path.is_symlink() or not path.is_dir() and path.exists():
            raise OSError("resource collection must be a real directory")
        return sorted(p for p in path.iterdir() if p.is_dir() and not p.is_symlink())
    except FileNotFoundError:
        return []


class _Cleanup:
    def __init__(self, options: CacheCleanOptions):
        for key in (options.repository_key, options.clone_key):
            if key and (Path(key).name != key or key in (".", "..")):
                raise ValueError("invalid resource key")
        if not options.repository_key or options.pr_number < 0:
            raise ValueError("repository key and nonnegative PR number are required")
        self.options = options
        self.store = ObservationStore(resolve_home(options.home))
        self.root = self.store.root / "repositories" / options.repository_key
        self.policy = options.policy or load_cleanup_policy(options.home, options.repository_key)
        self.before = options.access_before or time.time()
        self.report = CacheCleanReport(dry_run=options.dry_run)

    def access(self, path: Path) -> float:
        import math

        record = self.store.read(path / "access.json")
        if record is None:
            return 0
        value = record.get("lastAccessAt")
        if (
            record.get("version") != 1
            or isinstance(value, bool)
            or not isinstance(value, (int, float))
            or not math.isfinite(value)
            or value < 0
        ):
            raise ValueError("invalid resource access")
        return float(value)

    def remove(self, path: Path, resource: str, reason: str) -> None:
        check_cancelled()
        if any(path == Path(p) or Path(p) in path.parents for p in self.report.affected):
            return
        if path.is_symlink():
            raise OSError(f"refusing symlink cache {path}")
        if not path.exists():
            return
        path.relative_to(self.root)
        if path == self.root:
            raise ValueError("invalid cleanup boundary")
        size = (
            sum(p.stat().st_size for p in path.rglob("*") if p.is_file() and not p.is_symlink())
            if path.is_dir()
            else path.stat().st_size
        )
        if not self.options.dry_run:
            self.owned()
            shutil.rmtree(path) if path.is_dir() else path.unlink()
        self.report.actions.append(
            dict(
                action="remove_" + resource,
                status="planned" if self.options.dry_run else "completed",
                reason=reason,
                path=str(path),
                estimatedBytes=size,
            )
        )
        self.report.affected.append(str(path))
        log.info(
            "cache cleanup resource=%s path=%s reason=%s dry_run=%s",
            resource,
            path,
            reason,
            self.options.dry_run,
        )

    def pr_paths(self) -> list[Path]:
        return [
            p
            for p in _directories(self.root / "prs")
            if p.name.isascii()
            and p.name.isdigit()
            and int(p.name) > 0
            and str(int(p.name)) == p.name
            and (not self.options.pr_number or int(p.name) == self.options.pr_number)
        ]

    def branches(self) -> list[Path]:
        return [
            p
            for clone in _directories(self.root / "clones")
            if not self.options.clone_key or clone.name == self.options.clone_key
            for p in _directories(clone / "branches")
        ]

    def tracked(self, path: Path) -> bool:
        for file in path.glob("*.json"):
            if file.name == "access.json":
                continue
            record = self.store.read(file)
            if not record or record.get("version") != 1:
                raise ValueError("invalid PR record")
            observation = record.get("observation", {})
            if not isinstance(observation, dict):
                raise ValueError("invalid PR observation")
            if record.get("scopeIds") or observation.get("nextRefreshAt") is not None:
                return True
        return False

    def budget(self, resource: str, maximum: int) -> None:
        paths = self.branches() if resource == "branch" else self.pr_paths()
        rows: list[tuple[float, Path, int, str]] = []
        for path in paths:
            reason = ""
            count = 1
            if resource == "comment":
                if str(path) in self.report.affected:
                    continue
                path = path / "comments"
                count = sum(
                    any(f.is_file() and not f.is_symlink() for f in p.glob("*.json"))
                    for p in _directories(path)
                )
                if not count:
                    continue
            elif resource == "pr" and self.tracked(path):
                reason = "observation_tracked"
            elif resource == "branch" and not any(
                (path / n).exists() for n in ("branch.json", "associations")
            ):
                continue
            rows.append((self.access(path), path, count, reason))
        before = remaining = sum(row[2] for row in rows)
        rows.sort()
        cutoff, retained = len(rows), 0
        while cutoff > 0 and retained < maximum:
            if retained > 0 and retained + rows[cutoff - 1][2] > maximum:
                break
            cutoff -= 1
            retained += rows[cutoff][2]
        for index, (access, path, count, protection) in enumerate(rows):
            check_cancelled()
            merged = resource == "comment" and int(path.parent.name) in self.options.merged
            if not self.options.expire and index >= cutoff and not merged:
                continue
            if access > self.before:
                protection = "access_changed"
            if protection:
                self.report.actions.append(
                    dict(
                        action="retain_" + resource,
                        status="skipped",
                        reason=protection,
                        path=str(path),
                    )
                )
                continue
            reason = "pr_merged" if merged else "inactive" if self.options.expire else "max_count"
            try:
                if resource == "branch":
                    for name in ("branch.json", "associations"):
                        self.remove(path / name, "branch_cache", reason)
                else:
                    self.remove(path, resource, reason)
                if resource == "pr":
                    for branch in self.branches():
                        self.remove(branch / "associations", "branch_associations", "pr_evicted")
                remaining -= count
            except OSError as exc:
                self.report.errors.append(str(exc))
        self.report.counts.append(
            dict(
                resource=resource,
                repository=self.options.repository_key,
                before=before,
                after=remaining,
                max_count=maximum,
                over_budget=max(0, remaining - maximum),
            )
        )

    def clones(self) -> None:
        for clone in _directories(self.root / "clones"):
            if self.options.clone_key and clone.name != self.options.clone_key:
                continue
            record = self.store.read(clone / "clone.json")
            if not record:
                continue
            common = record.get("commonDir")
            if (
                record.get("version") != 1
                or not isinstance(common, str)
                or not Path(common).is_absolute()
                or digest_path(common) != clone.name
            ):
                raise ValueError("invalid clone registration")
            for checkout in _directories(clone / "checkouts"):
                row = self.store.read(checkout / "checkout.json")
                if not row:
                    continue
                location = row.get("path")
                if (
                    row.get("commonDir") != common
                    or row.get("clone") != clone.name
                    or not isinstance(location, str)
                    or not Path(location).is_absolute()
                    or digest_path(location) != checkout.name
                ):
                    raise ValueError("invalid checkout registration")
                try:
                    Path(location).lstat()
                except FileNotFoundError:
                    self.remove(checkout, "checkout_registration", "path_missing")
            try:
                Path(common).lstat()
            except FileNotFoundError:
                self.remove(clone / "clone.json", "clone_registration", "path_missing")

    def run(self, domain: str) -> CacheCleanReport:
        check_cancelled()
        if self.root.parent.is_symlink() or self.root.is_symlink():
            raise OSError("repository cache must be a real directory")
        if not self.root.exists():
            return self.report
        lock = Path(str(self.store.lock_path(self.root / "cleanup")) + ".lock")
        try:
            busy = time.time() - lock.lstat().st_mtime <= 30
        except FileNotFoundError:
            busy = False
        if busy:
            self.report.actions.append(
                dict(
                    action="retain_repository",
                    status="skipped",
                    reason="state_locked",
                    path=str(self.root),
                )
            )
            return self.report
        with (
            self.store.locked(self.store.root / "locks" / "activity" / self.options.repository_key),
            self.store.locked(self.root / "cleanup") as owned,
        ):
            self.owned = owned
            if domain == "repository" and self.access(self.root) > self.before:
                self.report.actions.append(
                    dict(
                        action="retain_repository",
                        status="skipped",
                        reason="access_changed",
                        path=str(self.root),
                    )
                )
                return self.report
            for resource, maximum in (
                ("pr", self.policy.pr),
                ("comment", self.policy.comment),
                ("branch", self.policy.branch),
            ):
                if domain in (resource, "repository"):
                    try:
                        self.budget(resource, maximum)
                    except (OSError, ValueError) as exc:
                        check_cancelled()
                        self.report.errors.append(f"{resource}: {exc}")
            if domain in ("clone", "repository"):
                try:
                    self.clones()
                except (OSError, ValueError) as exc:
                    self.report.errors.append(f"clone: {exc}")
            if domain == "repository" and self.options.expire:
                for name in ("metadata.json", "remotes"):
                    self.remove(self.root / name, "repository_cache", "inactive")
        check_cancelled()
        return self.report


def clean_repository(options: CacheCleanOptions) -> CacheCleanReport:
    return _Cleanup(options).run("repository")


def clean_pull_requests(options: CacheCleanOptions) -> CacheCleanReport:
    return _Cleanup(options).run("pr")


def clean_comments(options: CacheCleanOptions) -> CacheCleanReport:
    return _Cleanup(options).run("comment")


def clean_branches(options: CacheCleanOptions) -> CacheCleanReport:
    return _Cleanup(options).run("branch")


def clean_clones(options: CacheCleanOptions) -> CacheCleanReport:
    return _Cleanup(options).run("clone")
