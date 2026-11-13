"""Repository information storage. Observations are scoped, not a live Git mirror."""

from __future__ import annotations

import logging
import math
import os
import re
import time
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import cast

from ..errors import RepoKitError
from ..git import operations as git
from ..git import state as git_state
from ..git.remote import parse_remote_url
from .activity import record_access
from .home import resolve_home
from .identity import digest_path, local_key, name_part, repository_key
from .store import JSON, ObservationStore

logger = logging.getLogger(__name__)


def repository_location(repo: str | Path) -> tuple[str, git_state.CheckoutInfo, JSON]:
    """Resolve an explicit checkout's identity without network access or credentials."""
    info = git_state.checkout_info(repo)
    origin = git.git(repo, "config", "--get", "remote.origin.url", raw=True)
    if not origin.ok and not (origin.rc == 1 and not origin.err):
        raise OSError(f"cannot observe repository identity: {origin.err}")
    remote = parse_remote_url(origin.out.strip()) if origin.ok else None
    if remote:
        forge = {"github.com": "github", "gitlab.com": "gitlab"}.get(remote.host, remote.host)
        return (
            repository_key(forge, remote.path),
            info,
            {"forge": {"name": forge}, "path": remote.path},
        )
    return local_key(info.common_dir), info, {"local": info.common_dir}


@dataclass(frozen=True)
class BranchObservation:
    complete: bool
    branch: str
    head: str
    checkouts: tuple[git_state.CheckoutEntry, ...]
    observed_at: float
    source: str


class _IncompleteObservation(RepoKitError, OSError):
    """A rebuildable projection is incomplete; Git absence has not been established."""

    def __init__(self, message: str = "") -> None:
        super().__init__("state.incomplete", message)


class RepositoryState:
    """Own read-through local observations and disposable checkout projections.

    @spec A complete read of one clone updates only that clone. Failed reads never
    imply absence. Persisted topology never authorizes a Git write or deletion.
    @link component://docs/state.md
    """

    def __init__(self, home: str | Path | None = None):
        self.store = ObservationStore(resolve_home(home))

    def branch(
        self, repo: str | Path, branch: str, *, max_age: float = 0
    ) -> BranchObservation | None:
        """Load or repair one branch observation; zero requests a live Git read.

        @spec Partial publication triggers repair without reading unrelated branch records.
        @case tests/test_repository_state.py
        """
        if not math.isfinite(max_age) or max_age < 0:
            raise ValueError("max_age must be finite and nonnegative")
        key, info, _ = repository_location(repo)
        record_access(self.store, key, info)
        directory = self.store.root / "repositories" / key
        clone = digest_path(info.common_dir)
        if max_age > 0:
            with self.store.locked(directory / "topology"):
                try:
                    cached = self._branch(directory, clone, branch, "cache")
                except _IncompleteObservation:
                    logger.info(
                        "Repairing incomplete branch observation repository=%s branch=%s",
                        key,
                        branch,
                    )
                    cached = None
                if cached and 0 <= time.time() - cached.observed_at < max_age:
                    return cached
        self._observe(repo, branch=branch)
        with self.store.locked(directory / "topology"):
            return self._branch(directory, clone, branch, "git")

    def _branch(
        self, directory: Path, clone: str, branch: str, source: str
    ) -> BranchObservation | None:
        record = self.store.read(directory / "branches" / name_part(branch) / "observation.json")
        if not record:
            return None
        if record.get("version") != 1 or record.get("branch") != branch:
            raise OSError("invalid branch observation identity")
        observed = cast(JSON, cast(JSON, record["clones"]).get(clone, {}))
        if not observed:
            return None
        entries = []
        for checkout in cast(list[str], observed["checkouts"]):
            if not re.fullmatch(r"[0-9a-f]{32}", checkout):
                raise OSError("invalid checkout observation key")
            row = self.store.read(directory / "checkouts" / checkout / "observation.json")
            if (
                not row
                or row.get("version") != 1
                or row.get("clone") != clone
                or row.get("branch") != branch
                or row.get("observedAt") != observed["observedAt"]
            ):
                raise _IncompleteObservation("incomplete checkout observation")
            entries.append(
                git_state.CheckoutEntry(
                    str(row["path"]), str(row["head"]), branch, bool(row["primary"])
                )
            )
        return BranchObservation(
            bool(observed.get("complete", False)),
            branch,
            str(observed["head"]),
            tuple(entries),
            float(str(observed["observedAt"])),
            source,
        )

    def observe(
        self, repo: str | Path, *, reconcile: bool = False, record_use: bool = True
    ) -> Path:
        """Publish a complete local inventory; reconcile only absent checkout resources.

        @spec Detached checkouts have records but no branch link. Checkout reclamation
        never deletes source directories or execution history, and never crosses clones.
        """
        if record_use:
            key, info, _ = repository_location(repo)
            record_access(self.store, key, info)
        return self._observe(repo, reconcile=reconcile)

    def _observe(
        self, repo: str | Path, *, reconcile: bool = False, branch: str | None = None
    ) -> Path:
        key, info, identity = repository_location(repo)
        directory = self.store.root / "repositories" / key
        clone = digest_path(info.common_dir)
        with self.store.locked(directory / "topology") as owned:
            # Capture under the publication lease. No partial read can retire old facts.
            branches = dict(git_state.list_local_branches(repo))
            entries = git_state.list_checkouts(repo)
            if repository_location(repo)[0] != key:
                raise OSError("repository identity changed during observation")
            at = time.time()
            rows = {
                digest_path(entry.path): entry
                for entry in entries
                if entry.path and (branch is None or entry.branch == branch)
            }
            previous: dict[str, JSON] = {}
            for path in (
                (directory / "checkouts").glob("*/observation.json") if branch is None else ()
            ):
                row = self.store.read(path)
                if row and row.get("clone") == clone:
                    previous[path.parent.name] = row
            self.store.write(directory / "repository.json", identity, owned)
            for checkout, entry in rows.items():
                root = directory / "checkouts" / checkout
                self.store.write(
                    root / "observation.json",
                    {
                        "version": 1,
                        "clone": clone,
                        "commonDir": info.common_dir,
                        "path": entry.path,
                        "head": entry.head,
                        "branch": entry.branch,
                        "primary": entry.primary,
                        "observedAt": at,
                    },
                    owned,
                )
                self._link(root / "path", str(entry.path), owned)
            known = set(previous) | set(rows)
            names = set(branches) if branch is None else {branch}
            for path in (
                (directory / "branches").glob("*/observation.json") if branch is None else ()
            ):
                record = self.store.read(path)
                if record and clone in cast(JSON, record.get("clones", {})):
                    names.add(str(record["branch"]))
            for branch in sorted(names):
                root = directory / "branches" / name_part(branch)
                path = root / "observation.json"
                record = self.store.read(path) or {"version": 1, "branch": branch, "clones": {}}
                if record.get("version") != 1 or record.get("branch") != branch:
                    raise OSError("branch identity collision")
                clones = cast(JSON, record["clones"])
                branch_known = known | set(
                    cast(list[str], cast(JSON, clones.get(clone, {})).get("checkouts", []))
                )
                links = {k: e for k, e in rows.items() if e.branch == branch}
                if branch in branches:
                    clones[clone] = {
                        "head": branches[branch],
                        "commonDir": info.common_dir,
                        "observedAt": at,
                        "checkouts": sorted(links),
                        "complete": all(e.path for e in entries if e.branch == branch),
                    }
                else:
                    clones.pop(clone, None)
                self.store.write(path, record, owned)
                for checkout in branch_known:
                    if not re.fullmatch(r"[0-9a-f]{32}", checkout):
                        raise OSError("invalid checkout observation key")
                    link = root / "worktrees" / checkout
                    if checkout in links:
                        self._link(link, "../../../checkouts/" + checkout, owned)
                    elif link.is_symlink():
                        owned()
                        link.unlink()
                if reconcile and not clones:
                    self._retire_branch(root, owned)
            if reconcile and all(entry.path for entry in entries):
                for checkout, row in previous.items():
                    if checkout not in rows:
                        try:
                            Path(str(row["path"])).lstat()
                        except FileNotFoundError:
                            owned()
                            self._retire_checkout(directory / "checkouts" / checkout)
            logger.info(
                "repository observed key=%s clone=%s branches=%d checkouts=%d reconcile=%s",
                key,
                clone,
                len(branches),
                len(rows),
                reconcile,
            )
        return directory

    @staticmethod
    def _link(path: Path, target: str, owned: Callable[[], None]) -> None:
        from uuid import uuid4

        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        if path.exists() and not path.is_symlink():
            raise OSError(f"refusing to replace non-link {path}")
        temporary = path.with_name(path.name + "." + uuid4().hex + ".tmp")
        try:
            temporary.symlink_to(target)
            owned()
            os.replace(temporary, path)
        finally:
            temporary.unlink(missing_ok=True)

    @staticmethod
    def _retire_checkout(root: Path) -> None:
        if root.is_symlink():
            raise OSError("checkout state directory must not be a symlink")
        if any(path.name not in {"path", "observation.json"} for path in root.iterdir()):
            raise OSError(f"unrecognized checkout state retained: {root}")
        link = root / "path"
        if link.is_symlink():
            link.unlink()
        elif link.exists():
            raise OSError("checkout path marker is not a symlink")
        (root / "observation.json").unlink(missing_ok=True)
        root.rmdir()

    @staticmethod
    def _retire_branch(root: Path, owned: Callable[[], None]) -> None:
        # Association records have their own retention. Only retire an empty projection.
        if set(p.name for p in root.iterdir()) - {"worktrees", "observation.json"}:
            return
        links = root / "worktrees"
        if links.exists() and any(links.iterdir()):
            return
        owned()
        if links.exists():
            links.rmdir()
        (root / "observation.json").unlink()
        root.rmdir()
