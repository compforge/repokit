"""Repository-owned retention for linked checkouts and prepared environments."""

from __future__ import annotations

import fcntl
import json
import logging
import os
import shutil
import time
from collections.abc import Iterator
from contextlib import contextmanager
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from ..budget import current_budget
from ..dependencies import inspect_dependencies
from ..git import operations, state

if TYPE_CHECKING:
    from ..forge.observation.context import PullRequestContext
    from ..forge.observation.model import BranchPullRequests

logger = logging.getLogger(__name__)
CREATED = "repokit-created.json"


@dataclass(frozen=True)
class WorktreePolicy:
    max_count: int = 10
    max_prepared: int = 3
    outputs: tuple[str, ...] = ()

    def __post_init__(self) -> None:
        if (
            type(self.max_count) is not int
            or type(self.max_prepared) is not int
            or not 0 <= self.max_prepared <= self.max_count
        ):
            raise ValueError("worktree limits require 0 <= max_prepared <= max_count")
        for value in self.outputs:
            path = Path(value)
            if (
                not value
                or path.is_absolute()
                or any(p in ("..", ".git") for p in path.parts)
                or str(path) == "."
            ):
                raise ValueError(f"unsafe regenerable output: {value!r}")


def load_policy(repo: str | Path, *, home: str | Path | None = None) -> WorktreePolicy:
    """Repository-local configuration shared by all its checkouts."""
    from .persistent import persistent_directory

    path = persistent_directory(repo, home=home) / "config.json"
    try:
        document = json.loads(path.read_text())
        if not isinstance(document, dict):
            raise ValueError("repository configuration must be an object")
        value = document.get("worktree", {})
    except FileNotFoundError:
        return WorktreePolicy()
    if not isinstance(value, dict):
        raise ValueError("worktree configuration must be an object")
    count, prepared = value.get("max_count", 10), value.get("max_prepared", 3)
    outputs = value.get("outputs", [])
    if (
        type(count) is not int
        or type(prepared) is not int
        or not isinstance(outputs, list)
        or any(not isinstance(p, str) for p in outputs)
    ):
        raise ValueError("worktree limits must be integers and outputs a list of paths")
    return WorktreePolicy(count, prepared, tuple(outputs))


@dataclass(frozen=True)
class WorktreeAction:
    action: str
    status: str
    reason: str
    path: str
    branch: str = ""
    pr_number: int = 0


@dataclass(frozen=True)
class WorktreeCleanReport:
    actions: tuple[WorktreeAction, ...] = ()
    errors: tuple[str, ...] = ()
    remaining_count: int = 0
    remaining_prepared: int | None = 0
    dry_run: bool = False


@contextmanager
def worktree_lease(repo: str | Path, *, timeout: float = 0) -> Iterator[None]:
    """Serialize RepoKit creation, preparation and maintenance across processes."""
    path = Path(state.checkout_info(repo).common_dir) / "repokit-worktrees.lock"
    budget = current_budget(timeout) if timeout else None
    with path.open("a") as stream:
        while True:
            if budget:
                budget.remaining()
            try:
                fcntl.flock(stream, fcntl.LOCK_EX | fcntl.LOCK_NB)
                break
            except BlockingIOError:
                if budget is None:
                    raise
                time.sleep(min(0.05, budget.remaining()))
        try:
            yield
        finally:
            fcntl.flock(stream, fcntl.LOCK_UN)


def record_creation(path: str | Path) -> None:
    info = state.checkout_info(path)
    (Path(info.git_dir) / CREATED).write_text(
        json.dumps({"version": 1, "created_at": time.time_ns()})
    )


def _created(path: str) -> int | None:
    try:
        value = json.loads((Path(state.checkout_info(path).git_dir) / CREATED).read_text())
    except FileNotFoundError:
        return None
    timestamp = value.get("created_at")
    if value.get("version") != 1 or type(timestamp) is not int or timestamp <= 0:
        raise ValueError(f"invalid creation record: {path}")
    return timestamp


def _git(repo: str | Path, *args: str) -> str:
    result = operations.git(repo, *args, raw=True)
    if not result.ok:
        raise OSError(result.err or "Git observation failed")
    return result.out


def _environments(
    path: str, policy: WorktreePolicy, *, recover: bool = False
) -> tuple[tuple[Path, ...], bool]:
    from .. import inspect

    root = Path(path)
    report = inspect(root)
    if not report.complete:
        raise ValueError("dependency inventory is incomplete")
    directories: set[Path] = set()
    unmanaged: set[Path] = set()
    pending = _pending_releases(root, recover=recover)
    directories.update(pending)
    for binding in report.components:
        for env in inspect_dependencies(root / binding.root, inspection=report):
            if env.directory.is_dir():
                from ..dependencies import dependency_owned

                if not dependency_owned(env.directory, env.manager):
                    unmanaged.add(env.directory)
                    continue
                directories.add(env.directory)
    directories.update(root / value for value in policy.outputs if (root / value).exists())
    # Only explicitly managed, ignored, checkout-local trees can be erased.
    for directory in directories:
        if (
            directory.is_symlink()
            or not directory.is_dir()
            or directory.resolve() != directory
            or not directory.is_relative_to(root)
        ):
            raise ValueError(f"unsafe environment path: {directory}")
        relative = str(directory.relative_to(root))
        if _git(root, "--literal-pathspecs", "ls-files", "-z", "--", relative):
            raise ValueError(f"environment contains tracked files: {relative}")
        if not operations.git(root, "check-ignore", "--quiet", "--", relative).ok:
            raise ValueError(f"environment is not ignored: {relative}")
        if any(directory.rglob(".git")):
            raise ValueError(f"environment contains a nested repository: {relative}")
    return tuple(sorted(directories)), bool(unmanaged - directories)


def _pending_releases(root: Path, *, recover: bool = False) -> dict[Path, list[int]]:
    journal = Path(state.checkout_info(root).git_dir) / "repokit-releases.json"
    try:
        value = json.loads(journal.read_text())
    except FileNotFoundError:
        return {}
    if not isinstance(value, dict):
        raise ValueError("release journal must be an object")
    pending = {}
    for relative, identity in value.items():
        path = root / relative
        if (
            Path(relative).is_absolute()
            or any(part in ("..", ".git") for part in Path(relative).parts)
            or relative in ("", ".")
        ):
            raise ValueError(f"unsafe release journal path: {relative}")
        try:
            stat = path.lstat()
        except FileNotFoundError:
            continue
        if identity != [stat.st_dev, stat.st_ino]:
            raise ValueError(f"environment identity changed during release: {path}")
        pending[path] = identity
    # +spec=`Under the repository lease, completed deletions are retired before preparation.`
    if recover and len(pending) != len(value):
        _save_releases(root, journal, pending)
        logger.info(
            "retired completed environment releases checkout=%s count=%d",
            root,
            len(value) - len(pending),
        )
    return pending


def _save_releases(root: Path, journal: Path, pending: dict[Path, list[int]]) -> None:
    temporary = journal.with_suffix(".tmp")
    with temporary.open("w") as stream:
        json.dump({str(p.relative_to(root)): identity for p, identity in pending.items()}, stream)
        stream.flush()
        os.fsync(stream.fileno())
    temporary.replace(journal)


def _release_environment(root: Path, directory: Path) -> None:
    """+spec=`Ownership survives interruption; a replacement directory is never resumed.`"""
    journal = Path(state.checkout_info(root).git_dir) / "repokit-releases.json"
    pending = _pending_releases(root)
    stat = directory.lstat()
    pending[directory] = [stat.st_dev, stat.st_ino]

    _save_releases(root, journal, pending)
    shutil.rmtree(directory)
    del pending[directory]
    _save_releases(root, journal, pending)


def _protection(
    repo: str,
    entry: state.CheckoutEntry,
    current: str,
    inventory: list[state.CheckoutEntry] | None = None,
) -> str:
    path = entry.path
    if not path or entry.primary or path == current:
        return "primary_or_current_checkout"
    entries = state.list_checkouts(repo) if inventory is None else inventory
    if entry not in entries:
        return "checkout_identity_changed"
    if not entry.branch:
        return "detached_checkout"
    info = state._checkout_info(path, [(e.path or "", e.head, e.branch) for e in entries])
    if (Path(info.git_dir) / "locked").exists():
        return "locked"
    if any(
        (Path(info.git_dir) / name).exists()
        for name in ("index.lock", "MERGE_HEAD", "rebase-merge", "rebase-apply")
    ):
        return "git_operation_in_progress"
    if any(
        other.path and other.path != path and Path(other.path).is_relative_to(path)
        for other in entries
    ):
        return "nested_checkout"
    return ""


def _removal_protection(entry: state.CheckoutEntry, directories: tuple[Path, ...]) -> str:
    assert entry.path
    if _git(entry.path, "status", "--porcelain=v1", "-z", "--untracked-files=all"):
        return "unsaved_changes"
    ignored = _git(
        entry.path, "ls-files", "--others", "--ignored", "--exclude-standard", "--directory", "-z"
    )
    for value in ignored.split("\0"):
        if value and not any(
            (Path(entry.path) / value).is_relative_to(directory) for directory in directories
        ):
            return "unmanaged_ignored_files"
    return ""


def clean_worktrees(
    repo: str | Path,
    *,
    context: PullRequestContext | None = None,
    inventory: tuple[BranchPullRequests, ...] | None = None,
    policy: WorktreePolicy | None = None,
    dry_run: bool = False,
    home: str | Path | None = None,
) -> WorktreeCleanReport:
    """Merged first, then oldest checkouts, then oldest prepared environments.

    +spec=`Retention never forces Git removal or deletes branch refs. Unknown creation
    times sort before recorded times, with paths breaking ties deterministically.`
    +link=`component://docs/worktrees.md`
    """
    policy = policy or load_policy(repo, home=home)
    info = state.checkout_info(repo)
    control = info.main_root or info.root
    with worktree_lease(control):
        rows = inventory if inventory is not None else context.local_branches() if context else ()
        result = _clean(control, info.root, context, rows, policy, dry_run)
        if not dry_run:
            from .repository import RepositoryState

            try:
                RepositoryState(home).observe(control, reconcile=True, record_use=False)
            except (OSError, ValueError) as exc:
                from dataclasses import replace

                result = replace(
                    result, errors=(*result.errors, f"repository observation failed: {exc}")
                )
        return result


def _clean(
    repo: str,
    current: str,
    context: PullRequestContext | None,
    rows: tuple[BranchPullRequests, ...],
    policy: WorktreePolicy,
    dry_run: bool,
) -> WorktreeCleanReport:
    inventory = state.list_checkouts(repo)
    entries = [entry for entry in inventory if not entry.primary]
    actions: list[WorktreeAction] = []
    errors = [row.error for row in rows if row.error]
    dates: dict[str, int] = {}
    environments: dict[str, tuple[Path, ...]] = {}
    unmanaged: set[str] = set()
    excluded: set[str] = set()
    removed: set[str] = set()
    unknown_environments = False

    def action(
        entry: state.CheckoutEntry, kind: str, status: str, reason: str, number: int = 0
    ) -> None:
        actions.append(
            WorktreeAction(kind, status, reason, entry.path or "", entry.branch or "", number)
        )
        logger.info(
            "worktree maintenance action=%s status=%s path=%s branch=%s reason=%s",
            kind,
            status,
            entry.path,
            entry.branch,
            reason,
        )

    for entry in entries:
        if not entry.path:
            continue
        try:
            reason = _protection(repo, entry, current, inventory)
            directories, has_unmanaged = _environments(entry.path, policy, recover=not dry_run)
            environments[entry.path] = directories
            if has_unmanaged:
                unmanaged.add(entry.path)
            created = _created(entry.path)
            dates[entry.path] = created or 0
            if reason:
                excluded.add(entry.path)
                action(entry, "retain", "skipped", reason)
        except (OSError, ValueError) as exc:
            unknown_environments |= entry.path not in environments
            excluded.add(entry.path)
            errors.append(f"{entry.path}: {exc}")
            action(entry, "retain", "skipped", str(exc))

    def remove(entry: state.CheckoutEntry, reason: str, number: int = 0) -> bool:
        assert entry.path
        if entry.path in excluded:
            return False
        try:
            protection = _protection(repo, entry, current) or _removal_protection(
                entry, environments[entry.path]
            )
            # Status/ignored-file enumeration may outlive a branch switch in another process.
            protection = protection or _protection(repo, entry, current)
            if protection:
                action(entry, "remove_worktree", "skipped", protection, number)
                return False
            if not dry_run:
                result = operations.git(repo, "worktree", "remove", "--", entry.path, timeout=30)
                if not result.ok:
                    raise OSError(result.err or "worktree removal failed")
            removed.add(entry.path)
            action(entry, "remove_worktree", "planned" if dry_run else "completed", reason, number)
            return True
        except (OSError, ValueError) as exc:
            errors.append(f"{entry.path}: {exc}")
            action(entry, "remove_worktree", "failed", str(exc), number)
            return False

    by_branch = {row.branch: row for row in rows}
    for entry in entries:
        row = by_branch.get(entry.branch or "")
        if not entry.path or entry.path in excluded or row is None or row.error or row.ambiguous:
            continue
        pr = row.selected
        if pr is None or pr.state != "merged" or not pr.sha or pr.sha != entry.head:
            continue
        assert context is not None
        try:
            verified = context.verify(row)
            latest = verified.selected
            if (
                verified.error
                or verified.ambiguous
                or latest is None
                or latest.state != "merged"
                or latest.sha != entry.head
                or (latest.number, latest.target_repository) != (pr.number, pr.target_repository)
            ):
                raise ValueError("merged PR identity changed")
            remove(entry, "merged", pr.number)
        except Exception as exc:
            excluded.add(entry.path)
            errors.append(f"{entry.path}: {exc}")
            action(entry, "retain", "skipped", "pr_verification_failed", pr.number)

    oldest = sorted(
        (entry for entry in entries if entry.path not in removed),
        key=lambda entry: (dates.get(entry.path or "", 0), entry.path or ""),
    )
    # Protected old resources may exceed the limit; never evict the newest retained set instead.
    for entry in oldest[: max(0, len(oldest) - policy.max_count)]:
        if entry.path:
            remove(entry, "max_count")
    prepared = {
        path for path, directories in environments.items() if directories and path not in removed
    } | (unmanaged - removed)
    prepared_oldest = [entry for entry in oldest if entry.path in prepared]
    for entry in prepared_oldest[: max(0, len(prepared_oldest) - policy.max_prepared)]:
        path = entry.path
        if not path or path not in prepared or path in excluded:
            continue
        try:
            protection = _protection(repo, entry, current)
            if protection:
                action(entry, "release_environment", "skipped", protection)
                continue
            directories, has_unmanaged = _environments(path, policy, recover=not dry_run)
            if directories != environments[path]:
                raise ValueError("environment inventory changed")
            if not directories:
                action(entry, "release_environment", "skipped", "unmanaged_environment")
                continue
            protection = _protection(repo, entry, current)
            if protection:
                action(entry, "release_environment", "skipped", protection)
                continue
            if not dry_run:
                for directory in directories:
                    if directory.exists():
                        _release_environment(Path(path), directory)
            if not has_unmanaged:
                prepared.remove(path)
            action(
                entry, "release_environment", "planned" if dry_run else "completed", "max_prepared"
            )
        except (OSError, ValueError) as exc:
            errors.append(f"{path}: {exc}")
            action(entry, "release_environment", "failed", str(exc))
    return WorktreeCleanReport(
        tuple(actions),
        tuple(errors),
        len(entries) - len(removed),
        None if unknown_environments else len(prepared),
        dry_run,
    )
