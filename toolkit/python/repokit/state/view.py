"""Transient checkout facts assembled from RepoKit-owned sources."""

from dataclasses import dataclass
from pathlib import Path

from ..git import state as git_state
from .metadata import default_branch
from .persistent import BranchCreation, branch_creation
from .repository import RepositoryState


@dataclass(frozen=True)
class CheckoutView:
    root: str
    branch: str | None
    head: str
    default_branch: str | None
    checkouts: tuple[git_state.CheckoutEntry, ...]
    creation: BranchCreation | None


def checkout_view(repo: str | Path, *, home: str | Path | None = None) -> CheckoutView:
    """Read current Git identity and explicit creation provenance, never a caller's cache.

    Creation base is historical provenance, not a selected task or PR target.
    """
    info = git_state.checkout_info(repo)
    branch = git_state.get_current_branch(info.root)
    observation = RepositoryState(home).branch(info.root, branch) if branch else None
    return CheckoutView(
        info.root,
        branch,
        observation.head if observation else git_state.get_head_sha(info.root),
        default_branch(info.root, home=home).branch,
        tuple(git_state.list_checkouts(info.root)),
        branch_creation(info.root, branch, home=home) if branch else None,
    )
