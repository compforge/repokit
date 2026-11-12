"""Repository state business API: observations and checkout lifecycle.

Git adapters execute operations; this package owns reuse, publication and recovery.
"""

from .checkout import add_worktree as add_worktree
from .checkout import remove_worktree as remove_worktree
from .home import resolve_home as resolve_home
from .metadata import DefaultBranch as DefaultBranch
from .metadata import default_branch as default_branch
from .persistent import BranchCreation as BranchCreation
from .persistent import branch_creation as branch_creation
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
