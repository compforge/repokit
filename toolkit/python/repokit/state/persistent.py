"""Durable repository configuration and clone-scoped branch creation facts."""

from dataclasses import dataclass
from pathlib import Path

from .home import resolve_home
from .identity import digest_path, name_part
from .repository import repository_location
from .store import ObservationStore


def persistent_directory(repo: str | Path, *, home: str | Path | None = None) -> Path:
    key, _, _ = repository_location(repo)
    return resolve_home(home) / "repositories" / key / "persistent"


@dataclass(frozen=True)
class BranchCreation:
    branch: str
    base_ref: str
    base_commit: str
    remote: str
    common_dir: str
    created_at: float


def branch_creation(
    repo: str | Path, branch: str, *, home: str | Path | None = None
) -> BranchCreation | None:
    key, info, _ = repository_location(repo)
    store = ObservationStore(resolve_home(home))
    path = (
        store.root
        / "repositories"
        / key
        / "persistent"
        / "clones"
        / digest_path(info.common_dir)
        / "branches"
        / (name_part(branch) + ".json")
    )
    value = store.read(path)
    if value is None:
        return None
    base_ref, base_commit, remote, created_at = (
        value.get(k) for k in ("baseRef", "baseCommit", "remote", "createdAt")
    )
    if (
        value.get("branch") != branch
        or value.get("commonDir") != info.common_dir
        or not isinstance(base_ref, str)
        or not isinstance(base_commit, str)
        or not base_commit
        or not isinstance(remote, str)
        or not isinstance(created_at, (int, float))
    ):
        raise OSError("invalid branch creation identity")
    return BranchCreation(branch, base_ref, base_commit, remote, info.common_dir, float(created_at))
