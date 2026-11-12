"""Bounded remote-ref observations, separate from local remote-tracking refs."""

import time
from dataclasses import dataclass
from pathlib import Path

from ..git import operations, state
from .home import resolve_home
from .repository import repository_location
from .store import ObservationStore, digest


@dataclass(frozen=True)
class RemoteBranches:
    tips: tuple[tuple[str, str], ...]
    observed_at: float


def remote_branches(
    repo: str | Path,
    branches: tuple[str, ...],
    *,
    remote: str = "origin",
    home: str | Path | None = None,
    max_age: float = 60,
) -> RemoteBranches:
    """Observe the requested server refs; failures never become verified empty results.

    The cache includes the actual fetch URL and requested scope, so clones with
    different remotes cannot share another remote's tips. No objects are fetched.
    """
    if max_age < 0 or not branches:
        raise ValueError("branches and nonnegative max_age are required")
    key, _, _ = repository_location(repo)
    address = operations.git(repo, "remote", "get-url", "--", remote)
    if not address.ok:
        raise OSError(address.err or "remote unavailable")
    names = tuple(sorted(set(branches)))
    store = ObservationStore(resolve_home(home))
    path = store.root / "repositories" / key / "remotes" / (digest([address.out, names]) + ".json")
    with store.locked(path) as owned:
        value = store.read(path)
        if value:
            at, tips = value.get("observedAt"), value.get("tips")
            if (
                not isinstance(at, (int, float))
                or not isinstance(tips, dict)
                or not all(isinstance(k, str) and isinstance(v, str) for k, v in tips.items())
            ):
                raise OSError("invalid remote observation")
            if 0 <= time.time() - at < max_age:
                return RemoteBranches(tuple(sorted(tips.items())), float(at))
        tips = state.ls_remote_tips(repo, *("refs/heads/" + name for name in names), remote=remote)
        at = time.time()
        store.write(path, {"observedAt": at, "tips": tips}, owned)
        return RemoteBranches(tuple(sorted(tips.items())), at)
