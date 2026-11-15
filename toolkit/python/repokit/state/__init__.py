"""Repository state business API: observations and checkout lifecycle.

Git adapters execute operations; this package owns reuse, publication and recovery.
"""

from .checkout import add_worktree as add_worktree
from .checkout import remove_worktree as remove_worktree
from .cleanup import CacheCleanOptions as CacheCleanOptions
from .cleanup import CacheCleanReport as CacheCleanReport
from .cleanup import (
    clean_branches as clean_branches,
)
from .cleanup import (
    clean_clones as clean_clones,
)
from .cleanup import (
    clean_comments as clean_comments,
)
from .cleanup import (
    clean_pull_requests as clean_pull_requests,
)
from .cleanup import (
    clean_repository as clean_repository,
)
from .home import resolve_home as resolve_home
from .metadata import DefaultBranch as DefaultBranch
from .metadata import default_branch as default_branch
from .persistent import BranchCreation as BranchCreation
from .persistent import branch_creation as branch_creation
from .policy import CleanupPolicy as CleanupPolicy
from .policy import load_cleanup_policy as load_cleanup_policy
from .remote import RemoteBranches as RemoteBranches
from .remote import remote_branches as remote_branches
from .repository import BranchObservation as BranchObservation
from .repository import RepositoryState as RepositoryState
from .store import ObservationStorageError as ObservationStorageError
from .view import CheckoutView as CheckoutView
from .view import checkout_view as checkout_view
from .worktrees import WorktreeCleanReport as WorktreeCleanReport
from .worktrees import WorktreePolicy as WorktreePolicy
from .worktrees import clean_worktrees as clean_worktrees
