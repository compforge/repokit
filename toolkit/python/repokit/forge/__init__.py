"""Forge clients accept explicit endpoint and credentials; no application configuration."""

from .comment import Comment as Comment
from .comment import CommentAnchor as CommentAnchor
from .comment import CommentForge as CommentForge
from .comment import CommentID as CommentID
from .comment import CommentResolution as CommentResolution
from .comment import DeleteResult as DeleteResult
from .connection import ForgeConnection as ForgeConnection
from .connection import detect_provider as detect_provider
from .creation import PullRequestCreation as PullRequestCreation
from .creation import ensure_pull_request as ensure_pull_request
from .github import GitHubForge as GitHubForge
from .gitlab import GitLabForge as GitLabForge
from .model import Page as Page
from .observation import BranchPullRequests as BranchPullRequests
from .observation import PullRequestContext as PullRequestContext
from .observation import PullRequestKey as PullRequestKey
from .observation import PullRequestLifecycle as PullRequestLifecycle
from .observation import PullRequestObserver as PullRequestObserver
from .observation import PullRequestStatus as PullRequestStatus
from .observation import PullRequestStatusUnavailable as PullRequestStatusUnavailable
from .pr import PullRequestForge as PullRequestForge
