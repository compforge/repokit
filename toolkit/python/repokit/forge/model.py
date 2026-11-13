"""Provider-neutral repository collaboration objects and operations."""

from __future__ import annotations

import abc
import re
from dataclasses import dataclass, field
from enum import StrEnum
from typing import Any, Generic, TypeVar

from harness_common import Repository

from ..errors import RepoKitError


def parse_pr_number(s: str) -> int | None:
    """Extract a PR/MR number from a URL or a bare ref. Knows both forges' URL shapes
    (GitHub `/pull/N`, GitLab `/merge_requests/N`) so callers (CLI scripts) don't carry
    provider URL knowledge. Accepts a bare `N`, `#N`, or `!N` too."""
    m = re.search(r"/(?:pull|merge_requests)/(\d+)", s) or re.fullmatch(r"[!#]?(\d+)", s.strip())
    return int(m.group(1)) if m else None


class ForgeError(RepoKitError):
    """Any forge transport/API failure."""

    _error_code = "forge.request_failed"

    def __init__(self, message: str = "") -> None:
        super().__init__(
            self._error_code, message, uncertain=self._error_code == "forge.outcome_unknown"
        )


T = TypeVar("T")


@dataclass(frozen=True, slots=True)
class Page(Generic[T]):
    """One page; next_cursor is an opaque continuation, not an inventory guarantee."""

    items: tuple[T, ...]
    next_cursor: str | None = None


class ForgeOutcomeUnknown(ForgeError):
    """A request may have taken effect; inspect remote state before retrying."""

    _error_code = "forge.outcome_unknown"


class ForgeAuthError(ForgeError):
    """No usable token or insufficient permission (excluding rate limits)."""

    _error_code = "forge.authentication_required"


class ForgeRateLimited(ForgeError):
    """Requests must wait until retry_at (UTC epoch seconds); never an auth failure."""

    retry_at: float
    _error_code = "forge.rate_limited"

    def __init__(self, resource: str, retry_at: float, *, secondary: bool = False):
        super().__init__(f"forge rate limited: resource={resource}, retry_at={retry_at:.0f}")
        self.resource = resource
        self.retry_at = retry_at
        self.secondary = secondary

    def __str__(self) -> str:
        return f"forge rate limited: resource={self.resource}, retry_at={self.retry_at:.0f}"


class ForgeNotFound(ForgeError):
    """404 from the API."""

    _error_code = "forge.not_found"


@dataclass(frozen=True)
class PullRequestIdentity:
    """Stable cross-repo identity for a PR/MR.

    `source` is the origin host used as the `forges` config key; `repository`
    is the provider-neutral path under that host.
    """

    source: str
    repository: str
    number: int

    def to_dict(self) -> dict[str, Any]:
        return {
            "source": self.source,
            "repository": self.repository,
            "number": self.number,
        }


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


class CommentResolution(StrEnum):
    """Resolution state of the provider interaction backing a top-level comment."""

    UNSUPPORTED = "unsupported"
    UNRESOLVED = "unresolved"
    RESOLVED = "resolved"


@dataclass
class Comment:
    """One top-level PR/MR comment, optionally backed by a replyable review interaction.

    `replies` keeps provider threading inside the Forge adapter: callers consume one neutral
    Comment whether GitHub supplied a review-comment thread or GitLab supplied a discussion.
    `reply_ref` / `resolve_ref` are opaque adapter handles and must only be passed back to the
    Forge that produced them.

    Carries no label/finding vocabulary: `ccr:fp=` and `ccr:label=` remain review-layer body
    conventions. The forge is the durable source of truth, so comment refs are never persisted
    locally.
    """

    author: str = ""
    body: str = ""
    id: str = ""  # opaque, adapter-scoped; "" when the adapter can't supply one
    path: str = ""  # diff anchor (new side); "" for plain conversation comments
    line: int | None = None
    created_at: str = ""
    replies: list[Comment] = field(default_factory=list)
    reply_ref: str = ""
    resolve_ref: str = ""
    resolution: CommentResolution = CommentResolution.UNSUPPORTED

    @property
    def replyable(self) -> bool:
        return bool(self.reply_ref)


@dataclass
class Release:
    """A published release returned by a forge — neutral across forges.

    `tag` is the git tag the release points to; `target` is the commit-ish the tag was cut
    at (a branch name or sha). Like `PullRequest` it carries no provider — the tag is created
    server-side by the forge, so a release never requires a local `git push --tags`.
    """

    tag: str
    name: str = ""
    target: str = ""  # target_commitish (GitHub) / commit the tag resolved to (GitLab)
    web_url: str = ""
    created_at: str | None = None


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


class Forge(abc.ABC):
    """Operations provided by a code-review host — defined in the domain's terms, NOT
    mirroring any one SDK/REST surface. GitLab/GitHub adapters implement this as peers.

    Methods return neutral PullRequest / Comment objects. Callers own workflow policy.
    """

    provider: str = ""

    def list_repositories(
        self, *, page_size: int | None = None, cursor: str | None = None
    ) -> Page[Repository]:
        """Read one page of repositories the authenticated account owns or participates in.

        Default page_size is 50 (maximum 100). Resume with the same account and page size.
        An inventory is not an atomic snapshot and does not cover every authored PR.
        """
        raise ForgeError("repository listing is not supported")

    def repository(self) -> Repository:
        """Read repository identity and optional direct same-Forge fork parent.

        Missing provenance is not a failed lookup; transport errors propagate.
        """
        raise ForgeError("repository metadata is not supported")

    def for_repository(self, path: str) -> Forge:
        """Scope operations to a repository on this Forge instance.

        PR numbers, queries and updates belong to path. No parent-selection policy.
        """
        raise ForgeError("repository scoping is not supported")

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

    def request_counts(self) -> dict[str, int]:
        """Transport calls made by this client, including conditional validations."""
        return {"requests": 0, "not_modified": 0}

    @abc.abstractmethod
    def default_branch(self) -> str:
        """The repo's **forge-configured** default branch, e.g. 'main' / 'master' (one
        repo-level GET). This is the remote source of truth, fresher and more reliable than
        the local `refs/remotes/origin/HEAD` cache (which `git fetch` never updates). It is
        NOT necessarily the branch a team merges into — a repo may default to `main` yet treat
        `release` as trunk — so callers still layer a per-repo config override on top."""

    @abc.abstractmethod
    def create_release(self, *, tag: str, target: str, name: str = "", notes: str = "") -> Release:
        """Publish a release `name` at `tag`, creating the tag at `target` (a branch name or
        sha) on the server; this needs no local checkout or Git push.
        GitHub uses target_commitish; GitLab uses ref.
        A write primitive; version/increment policy lives in the release orchestrator, not here."""

    @abc.abstractmethod
    def latest_release(self) -> Release | None:
        """The most recent published release, or None when the repo has none yet (its first
        release). Read primitive — the orchestrator uses it to check the new version is an
        increment; callers use the tag as the baseline for semantic release notes."""

    @abc.abstractmethod
    def comments(self, number: int) -> list[Comment]:
        """Top-level human comments on PR/MR `number`, with review replies nested.

        The adapter merges the provider's plain-comment and review surfaces. A replyable
        review interaction is one Comment with `replies` and opaque action refs; callers do
        not group flat provider notes or understand GitHub threads versus GitLab discussions.
        System/bot activity notes are excluded.
        """

    @abc.abstractmethod
    def comment(
        self,
        number: int,
        body: str,
        *,
        replyable: bool = False,
        path: str = "",
        line: int | None = None,
    ) -> None:
        """Post one comment, optionally requiring a replyable provider interaction.

        The default is a standalone GitHub issue comment / GitLab MR note. With
        `replyable=True`, `path`/`line` optionally request a new-side diff anchor; adapters
        raise when that shape is unsupported rather than silently publishing a standalone
        comment. The caller owns any fallback policy.
        """

    def reply(self, number: int, target: Comment, body: str) -> None:
        """Reply to a Comment returned by this Forge's `comments()`.

        Adapters consume `target.reply_ref`. A standalone comment raises rather than silently
        creating a detached response.
        """
        raise ForgeError(f"{self.provider or 'forge'}: replies not supported")

    def resolve_comment(self, number: int, target: Comment) -> None:
        """Resolve the review interaction backing `target` after its finding is handled.

        Resolution is a separate, best-effort
        forge transition. Adapters consume `target.resolve_ref`; unsupported comments raise so
        callers keep the verdict while reporting that the merge blocker may remain.
        """
        raise ForgeError(f"{self.provider or 'forge'}: resolving comments not supported")

    def merge_readiness(self, number: int) -> MergeReadiness:
        """Why MR/PR `number` can't merge yet (CONFLICT / DISCUSSIONS_UNRESOLVED / …), or READY.
        A fetch primitive (peer of `description`), NOT a `PullRequest` field — see `MergeReadiness`.
        Concrete-with-default rather than abstract: an adapter that hasn't implemented it (today:
        GitHub) inherits the safe UNKNOWN instead of being forced to lie; GitLab overrides."""
        return MergeReadiness.UNKNOWN
