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

from ..budget import current_budget
from ..dependencies import dependency_owned, inspect_dependencies
from ..git import operations, state
from .home import resolve_home

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
    from .policy import policy_documents
    from .repository import repository_location

    value: dict[str, object] = {"max_count": 10, "max_prepared": 3, "outputs": []}
    for document in policy_documents(home, repository_location(repo)[0]):
        section = document.get("worktree", {})
        if not isinstance(section, dict) or section.keys() - value.keys():
            raise ValueError("invalid worktree policy")
        value.update(section)
    count, prepared, outputs = value["max_count"], value["max_prepared"], value["outputs"]
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


def _environments(path: str, *, recover: bool = False) -> tuple[tuple[Path, ...], bool]:
    from .. import inspect

    root = Path(path)
    report = inspect(root)
    if not report.complete:
        raise ValueError("dependency inventory is incomplete")
    directories: set[Path] = set()
    unmanaged: set[Path] = set()
    pending = _pending_releases(root, recover=recover)
    for binding in report.components:
        for env in inspect_dependencies(root / binding.root, inspection=report):
            if env.directory.is_dir():
                if env.directory not in pending and not dependency_owned(
                    env.directory, env.manager
                ):
                    unmanaged.add(env.directory)
                    continue
                directories.add(env.directory)
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
    info = state._checkout_info(path, entries[0].path if entries else None)
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


def _retire_missing_worktree(
    repo: str, entry: state.CheckoutEntry, dry_run: bool
) -> tuple[str, str]:
    assert entry.path
    live = next((row for row in state.list_checkouts(repo) if row.path == entry.path), None)
    if live is None:
        return ("skipped" if dry_run else "completed"), "already_absent"
    if live.locked:
        return "skipped", "locked"
    if live.primary or not live.prunable or (live.head, live.branch) != (entry.head, entry.branch):
        return "skipped", "checkout_identity_changed"
    try:
        Path(entry.path).lstat()
    except FileNotFoundError:
        if dry_run:
            return "planned", "missing_checkout"
        result = operations.git(repo, "worktree", "remove", "--", entry.path)
        if not result.ok and any(row.path == entry.path for row in state.list_checkouts(repo)):
            raise OSError(result.err or "worktree registration removal failed")
        return "completed", "missing_checkout"
    return "skipped", "checkout_identity_changed"


def clean_worktrees(
    repo: str | Path,
    *,
    policy: WorktreePolicy | None = None,
    dry_run: bool = False,
    home: str | Path | None = None,
) -> WorktreeCleanReport:
    """Apply both retention windows to all linked checkouts in creation order.

    +spec=Keep max_prepared intact, release Depot through max_count, then remove older checkouts.
    +link=`component://docs/worktrees.md`
    """
    policy = policy or load_policy(repo, home=home)
    info = state.checkout_info(repo)
    control = info.main_root or info.root
    with worktree_lease(control):
        result = _clean(control, info.root, resolve_home(home).resolve(), policy, dry_run)
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
    home: Path,
    policy: WorktreePolicy,
    dry_run: bool,
) -> WorktreeCleanReport:
    inventory = state.list_checkouts(repo)
    entries: list[state.CheckoutEntry] = []
    actions: list[WorktreeAction] = []
    errors: list[str] = []
    dates: dict[str, int] = {}
    inspected: set[str] = set()
    prepared: set[str] = set()
    excluded: set[str] = set()
    removed: set[str] = set()

    def action(entry: state.CheckoutEntry, kind: str, status: str, reason: str) -> None:
        actions.append(WorktreeAction(kind, status, reason, entry.path or "", entry.branch or ""))
        logger.info(
            "worktree maintenance action=%s status=%s path=%s branch=%s reason=%s",
            kind,
            status,
            entry.path,
            entry.branch,
            reason,
        )

    def retire_absent(entry: state.CheckoutEntry) -> bool:
        assert entry.path
        try:
            Path(entry.path).lstat()
        except FileNotFoundError:
            try:
                status, reason = _retire_missing_worktree(repo, entry, dry_run)
                action(entry, "remove_worktree", status, reason)
            except (OSError, ValueError) as exc:
                errors.append(f"{entry.path}: {exc}")
                action(entry, "remove_worktree", "failed", str(exc))
            return True
        except OSError:
            # Let the normal eligibility check report errors accessing a live path.
            pass
        return False

    invoking = Path.cwd().resolve()
    for entry in inventory:
        if entry.primary or not entry.path:
            continue
        if retire_absent(entry):
            continue
        try:
            if home.is_relative_to(entry.path):
                reason = "contains_state_home"
            elif invoking.is_relative_to(entry.path):
                reason = "current_checkout"
            else:
                reason = _protection(repo, entry, current, inventory)
            created = _created(entry.path)
            dates[entry.path] = created or 0
            if reason:
                excluded.add(entry.path)
                action(entry, "retain", "skipped", reason)
        except (OSError, ValueError) as exc:
            if retire_absent(entry):
                continue
            excluded.add(entry.path)
            errors.append(f"{entry.path}: {exc}")
            action(entry, "retain", "skipped", str(exc))
        entries.append(entry)

    def remove(entry: state.CheckoutEntry) -> None:
        assert entry.path
        if entry.path in excluded:
            return
        try:
            protection = _protection(repo, entry, current)
            if protection:
                if retire_absent(entry):
                    removed.add(entry.path)
                    return
                action(entry, "remove_worktree", "skipped", protection)
                return
            if not dry_run:
                result = operations.git(
                    repo, "worktree", "remove", "--force", "--", entry.path, timeout=30
                )
                if not result.ok:
                    raise OSError(result.err or "worktree removal failed")
            removed.add(entry.path)
            action(entry, "remove_worktree", "planned" if dry_run else "completed", "max_count")
        except (OSError, ValueError) as exc:
            if retire_absent(entry):
                removed.add(entry.path)
                return
            errors.append(f"{entry.path}: {exc}")
            action(entry, "remove_worktree", "failed", str(exc))

    oldest = sorted(entries, key=lambda entry: (dates.get(entry.path or "", 0), entry.path or ""))
    remove_end = max(0, len(oldest) - policy.max_count)
    release_end = max(0, len(oldest) - policy.max_prepared)
    for entry in oldest[:remove_end]:
        if entry.path:
            remove(entry)
    for entry in oldest[remove_end:release_end]:
        path = entry.path
        if not path or path in excluded:
            continue
        try:
            protection = _protection(repo, entry, current)
            if protection:
                action(entry, "release_environment", "skipped", protection)
                continue
            try:
                directories, has_unmanaged = _environments(path, recover=not dry_run)
            except (OSError, ValueError) as exc:
                errors.append(f"{path}: {exc}")
                action(entry, "inspect_environment", "failed", str(exc))
                continue
            if not directories:
                inspected.add(path)
                if has_unmanaged:
                    prepared.add(path)
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
            inspected.add(path)
            if has_unmanaged:
                prepared.add(path)
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
        len(prepared) if len(inspected) + len(removed) == len(entries) else None,
        dry_run,
    )
