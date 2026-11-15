"""Provider-neutral repository collaboration objects and operations."""

from __future__ import annotations

import abc
import re
from dataclasses import dataclass
from typing import Any, Generic, TypeVar

from harness_common import Repository

from .comment import CommentForge
from .errors import ForgeAuthError as ForgeAuthError
from .errors import ForgeError as ForgeError
from .errors import ForgeNotFound as ForgeNotFound
from .errors import ForgeOutcomeUnknown as ForgeOutcomeUnknown
from .errors import ForgeRateLimited as ForgeRateLimited
from .pr import MergeReadiness as MergeReadiness
from .pr import PullRequest as PullRequest
from .pr import PullRequestForge


def parse_pr_number(s: str) -> int | None:
    """Extract a PR/MR number from a URL or a bare ref. Knows both forges' URL shapes
    (GitHub `/pull/N`, GitLab `/merge_requests/N`) so callers (CLI scripts) don't carry
    provider URL knowledge. Accepts a bare `N`, `#N`, or `!N` too."""
    m = re.search(r"/(?:pull|merge_requests)/(\d+)", s) or re.fullmatch(r"[!#]?(\d+)", s.strip())
    return int(m.group(1)) if m else None


T = TypeVar("T")


@dataclass(frozen=True, slots=True)
class Page(Generic[T]):
    """One page; next_cursor is an opaque continuation, not an inventory guarantee."""

    items: tuple[T, ...]
    next_cursor: str | None = None


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


class Forge(PullRequestForge, CommentForge, abc.ABC):
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
