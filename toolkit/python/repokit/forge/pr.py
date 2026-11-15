"""PR lifecycle facts and domain interface, independent of comments."""

from __future__ import annotations

import abc
from dataclasses import dataclass
from enum import StrEnum
from typing import Any

from harness_common import Repository

from .errors import ForgeError


@dataclass
class PullRequest:
    """A code-review proposal returned by a forge — neutral across forges.

    `number` is the per-repo sequential id (the number in the URL: GitLab `iid`,
    GitHub PR number). `state` is normalized to 'open' | 'merged' | 'closed' by the
    adapter (GitHub's open/closed + a `merged` flag collapses to these three). No
    `provider` field — that's a repo-level fact (see module docstring); display labels
    are built with `pr_label(provider, number)` at render time. 'inactive' (merged/closed)
    is derived, never stored.
    """

    number: int
    title: str = ""
    state: str = ""  # neutral: "open" | "merged" | "closed"
    source_branch: str = ""
    target_branch: str = ""
    web_url: str = ""
    sha: str = ""  # source-branch tip the monitor ancestry-checks against HEAD
    updated_at: str | None = None
    source_repository: str = ""
    target_repository: str = ""

    @property
    def inactive(self) -> bool:
        return self.state in ("merged", "closed")

    @property
    def is_open(self) -> bool:
        """Whether the forge reports the proposal as open."""
        return self.state == "open"

    @classmethod
    def from_dict(cls, d: dict[str, Any]) -> PullRequest:
        return cls(
            number=int(d["number"]),
            title=d.get("title", ""),
            state=d.get("state", ""),
            source_branch=d.get("source_branch", ""),
            target_branch=d.get("target_branch", ""),
            web_url=d.get("web_url", ""),
            sha=d.get("sha", ""),
            updated_at=d.get("updated_at"),
            source_repository=d.get("source_repository", ""),
            target_repository=d.get("target_repository", ""),
        )


class MergeReadiness(StrEnum):
    """Why a PR/MR can't be merged yet — the neutral form of GitLab's `detailed_merge_status`
    / GitHub's `mergeable_state`. Forges surface one blocking reason at a time; plus READY and
    UNKNOWN.

    UNKNOWN is the *safe* value, and first-class on purpose: forges compute mergeability
    ASYNCHRONOUSLY, so a just-pushed PR reads as "still checking" — that must never collapse to
    READY or to a real blocker.

    Fetched on demand via `Forge.merge_readiness` (a primitive, like `description`/`comments`),
    deliberately NOT a `PullRequest` field: it's a derived verdict over the source×target tips
    that goes stale the moment either branch moves (a merge into target can block YOUR PR with
    your branch unchanged), so consumers should re-read it at the relevant decision boundary.
    """

    READY = "ready"
    CONFLICT = "conflict"
    DISCUSSIONS_UNRESOLVED = "discussions_unresolved"
    CI_BLOCKED = "ci_blocked"
    NEEDS_APPROVAL = "needs_approval"
    DRAFT = "draft"
    UNKNOWN = "unknown"


class PullRequestForge(abc.ABC):
    """Repository-scoped PR operations implemented by each provider."""

    provider: str = ""

    @abc.abstractmethod
    def create(
        self,
        *,
        source_repository: Repository,
        source_branch: str,
        target_repository: Repository,
        target_branch: str,
        title: str,
        body: str = "",
    ) -> PullRequest:
        """Create from an explicit repository/branch pair on each side.

        @spec Both repositories belong to this Forge instance; reject mismatch before I/O.
        Repository scope on the client never overrides either explicit repository.
        @link component://docs/pr.md
        """
        ...

    def merge(self, number: int, *, expected_sha: str) -> PullRequest:
        """Merge only the reviewed source tip; authorization belongs to the caller."""
        raise ForgeError("merge is not supported")

    @abc.abstractmethod
    def get(self, number: int) -> PullRequest: ...

    @abc.abstractmethod
    def description(self, number: int) -> str:
        """The PR/MR body text. A separate primitive rather than a `PullRequest` field:
        bodies can be large, so they are fetched
        only at the moment a caller syncs the description (see commit_flow)."""

    @abc.abstractmethod
    def update(self, number: int, **fields: str) -> PullRequest: ...

    @abc.abstractmethod
    def close(self, number: int) -> PullRequest:
        """Close the PR/MR without merging. A distinct primitive, not `update(state=...)`: the
        two forges spell it incompatibly (GitLab `state_event=close`, GitHub `state=closed`), so
        the neutral verb hides that split instead of leaking either spelling into callers."""

    @abc.abstractmethod
    def prs_for_branch(
        self, branch: str, *, source_repository: Repository | None = None
    ) -> list[PullRequest]:
        """All PRs whose source is `branch`, newest first (an old finished one + a new
        open one can coexist after a branch is reused — callers pick).

        Query this client's repository, filtering by the explicit source repository
        (defaults to this repository). Target-branch selection belongs to the caller.
        """

    @abc.abstractmethod
    def recent(self, limit: int) -> list[PullRequest]:
        """The `limit` most-recently-created PRs in the repo, newest first."""

    def open_pull_requests(self) -> list[PullRequest]:
        """Complete open PR inventory sourced from this repository, newest first.

        A failed page raises; partial inventories cannot establish absence. Foreign
        source repositories are excluded, just as in prs_for_branch's default scope.
        """
        raise ForgeError(f"{self.provider}: open PR inventory is not supported")

    def merge_readiness(self, number: int) -> MergeReadiness:
        """Why MR/PR `number` can't merge yet (CONFLICT / DISCUSSIONS_UNRESOLVED / …), or READY.
        A fetch primitive (peer of `description`), NOT a `PullRequest` field — see `MergeReadiness`.
        Concrete-with-default rather than abstract: an adapter that hasn't implemented it (today:
        GitHub) inherits the safe UNKNOWN instead of being forced to lie; GitLab overrides."""
        return MergeReadiness.UNKNOWN
