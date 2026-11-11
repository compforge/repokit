"""Repository state business API: observations and checkout lifecycle.

Git adapters execute operations; this package owns reuse, publication and recovery.
"""

from .checkout import add_worktree as add_worktree
from .checkout import remove_worktree as remove_worktree
from .home import resolve_home as resolve_home
from .metadata import DefaultBranch as DefaultBranch
from .metadata import default_branch as default_branch
from .repository import BranchObservation as BranchObservation
from .repository import RepositoryState as RepositoryState
from .store import ObservationStorageError as ObservationStorageError
from .worktrees import WorktreeCleanReport as WorktreeCleanReport
from .worktrees import WorktreePolicy as WorktreePolicy
from .worktrees import clean_worktrees as clean_worktrees
