"""Repository metadata observations with toolkit-owned caching and refresh."""

from __future__ import annotations

import logging
import time
from dataclasses import dataclass
from pathlib import Path

from . import git
from ._state_paths import local_key, repository_key
from .forge.model import Forge, ForgeError
from .forge.observation.store import ObservationStore, digest
from .git import state as git_state
from .git.remote import parse_remote_url

logger = logging.getLogger(__name__)


@dataclass(frozen=True)
class DefaultBranch:
    branch: str | None
    observed_at: float
    fresh: bool
    diagnostic: str = ""


def default_branch(
    repo: str | Path,
    *,
    forge: Forge | None = None,
    state_dir: str | Path | None = None,
    max_age: float = 86400,
) -> DefaultBranch:
    """Read default-branch metadata, refreshing stale observations at most once per lease.

    @spec RepoKit owns cache and transport fallback; no main/master guess is a repository fact.
    Failed refresh retains an explicitly stale observation and its diagnostic. Local Git
    HEAD cache may be used but is not promoted to fresh without a successful remote read.
    @link component://docs/repository.md
    """
    if max_age < 0:
        raise ValueError("max_age must be nonnegative")
    info = git_state.checkout_info(repo)
    remote = git.git(repo, "config", "--get", "remote.origin.url", raw=True)
    if not remote.ok and not (remote.rc == 1 and not remote.err):
        raise OSError(f"cannot read origin identity: {remote.err}")
    # Include remote configuration so repointing origin never reuses another repository's fact.
    store = ObservationStore(state_dir or Path.home() / ".repocli")
    identity = parse_remote_url(remote.out.strip()) if remote.ok else None
    key = (
        repository_key(
            {"github.com": "github", "gitlab.com": "gitlab"}.get(identity.host, identity.host),
            identity.path,
        )
        if identity
        else local_key(info.common_dir)
    )
    path = store.root / "repositories" / key / "metadata.json"
    legacy = store.root / "repositories" / digest([info.common_dir, remote.out]) / "metadata.json"

    def read() -> DefaultBranch:
        value = store.read(path) or store.read(legacy)
        if value is None:
            return DefaultBranch(None, 0, False)
        branch, at = value.get("default_branch"), value.get("observed_at")
        if not isinstance(branch, str) or not branch or not isinstance(at, (int, float)):
            raise OSError("invalid default-branch observation")
        return DefaultBranch(branch, float(at), 0 <= time.time() - at < max_age)

    with store.locked(path) as assert_owned:
        if store.read(path) is None:
            old = store.read(legacy)
            if old is not None:
                store.write(path, old, assert_owned)
        cached = read()
        if cached.fresh:
            return cached
        diagnostic = ""
        branch = ""
        if forge is not None:
            try:
                branch = forge.default_branch()
            except ForgeError as error:
                diagnostic = str(error)
        if branch:
            git_state.set_local_default_head(repo, branch)
        elif remote.ok and git_state.refresh_remote_head(repo):
            branch = git_state.local_default_branch(repo) or ""
        if branch:
            at = time.time()
            store.write(path, {"default_branch": branch, "observed_at": at}, assert_owned)
            logger.info("default branch refreshed repository=%s branch=%s", info.root, branch)
            return DefaultBranch(branch, at, True)
        local = git_state.local_default_branch(repo)
        diagnostic = diagnostic or "default branch could not be refreshed from origin"
        logger.warning("default branch unavailable repository=%s detail=%s", info.root, diagnostic)
        return DefaultBranch(cached.branch or local, cached.observed_at, False, diagnostic)
