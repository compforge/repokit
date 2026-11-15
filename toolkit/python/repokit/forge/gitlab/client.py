"""GitLabForge — the GitLab adapter for the `Forge` port.

Maps GitLab's merge-request REST surface onto the neutral domain: `iid`→`number`,
`opened`→`open`, `/merge_requests` paths. All GitLab-specific shape lives here; callers
see only `PullRequest` / `Comment`. The recent-window *policy* is not here — it's
the caller, composed over `recent` + `get`.
"""

from __future__ import annotations

import urllib.parse
from pathlib import Path
from typing import Any

from harness_common import Forge as ForgeIdentity
from harness_common import Repository

from ...state.home import resolve_home
from .._http_state import HttpState
from .._page import page_continuation, page_position
from .._rest import RestClient
from ..model import (
    Forge,
    ForgeError,
    MergeReadiness,
    Page,
    PullRequest,
    Release,
)
from .comments import GitLabComments

# GitLab persisted state → neutral.
_STATE_IN = {"opened": "open", "merged": "merged", "closed": "closed", "locked": "closed"}

# GitLab `detailed_merge_status` → neutral MergeReadiness. Anything unlisted (checking,
# preparing, unchecked, need_rebase, broken_status, …) falls through to UNKNOWN — the safe
# value while GitLab is still computing, or for unknown provider statuses.
_READINESS_IN = {
    "mergeable": MergeReadiness.READY,
    "conflict": MergeReadiness.CONFLICT,
    "discussions_not_resolved": MergeReadiness.DISCUSSIONS_UNRESOLVED,
    "draft_status": MergeReadiness.DRAFT,
    "not_approved": MergeReadiness.NEEDS_APPROVAL,
    "ci_must_pass": MergeReadiness.CI_BLOCKED,
    "ci_still_running": MergeReadiness.CI_BLOCKED,
}


class GitLabForge(GitLabComments, Forge):
    provider = "gitlab"

    def __init__(
        self,
        host: str,
        path: str,
        token: str,
        *,
        forge: str | None = None,
        timeout: int = 10,
        home: str | Path | None = None,
    ):
        host = host.lower()
        self.forge = ForgeIdentity(
            forge if forge is not None else ("gitlab" if host == "gitlab.com" else host)
        )
        self.host, self.path, self._token, self.timeout = host, path, token, timeout
        self.home = resolve_home(home)
        enc = urllib.parse.quote(path, safe="")
        self.c = RestClient(
            f"https://{host}/api/v4/projects/{enc}",
            {"PRIVATE-TOKEN": token},
            timeout=timeout,
            state=HttpState(self.home / "forge-http", host, token),
        )
        self._instance = RestClient(
            f"https://{host}/api/v4", {"PRIVATE-TOKEN": token}, timeout=timeout, state=self.c.state
        )

    def list_repositories(
        self, *, page_size: int | None = None, cursor: str | None = None
    ) -> Page[Repository]:
        scope = f"gitlab:{self.forge.name}:{self.host}:repositories"
        page, size = page_position(scope, page_size, cursor)
        rows = self._instance.get(
            "projects",
            membership="true",
            simple="true",
            order_by="id",
            sort="asc",
            per_page=size,
            page=page,
        )
        if not isinstance(rows, list):
            raise ForgeError("GitLab repository page must be a list")
        if len(rows) > size:
            raise ForgeError("Repository response exceeds requested page size")
        items = []
        for row in rows:
            path = row.get("path_with_namespace") if isinstance(row, dict) else None
            if not isinstance(path, str) or any(p in ("", ".", "..") for p in path.split("/")):
                raise ForgeError("Invalid GitLab repository path_with_namespace")
            items.append(Repository(self.forge, path))
        return Page(tuple(items), page_continuation(scope, page, size, len(rows) >= size))

    def repository(self) -> Repository:
        data = self.c.get("")
        parent = (data.get("forked_from_project") or {}).get("path_with_namespace") or None
        return Repository(
            self.forge,
            data["path_with_namespace"],
            forked_from=parent,
        )

    def for_repository(self, path: str) -> GitLabForge:
        if not path or ":" in path or path.startswith("/"):
            raise ValueError(f"invalid GitLab repository path: {path!r}")
        return GitLabForge(
            self.host,
            path,
            self._token,
            forge=self.forge.name,
            timeout=self.timeout,
            home=self.home,
        )

    def _repository_client(self, repository: Repository) -> GitLabForge:
        forge = self.forge
        if repository.forge != forge:
            raise ValueError(
                f"repository {repository.id()!r} does not belong to Forge {forge.name!r}"
            )
        return self.for_repository(repository.path)

    def _to_pr(self, d: dict[str, Any]) -> PullRequest:
        return PullRequest(
            number=int(d["iid"]),
            title=d.get("title", ""),
            state=_STATE_IN.get(d.get("state", ""), d.get("state", "")),
            source_branch=d.get("source_branch", ""),
            target_branch=d.get("target_branch", ""),
            web_url=d.get("web_url", ""),
            sha=d.get("sha", "") or "",
            updated_at=d.get("updated_at"),
        )

    def _list(self, **params: Any) -> list[PullRequest]:
        params.setdefault("order_by", "created_at")
        params.setdefault("sort", "desc")
        out = self.c.get("merge_requests", **params)
        return [self._to_pr(d) for d in out] if isinstance(out, list) else []

    def prs_for_branch(
        self, branch: str, *, source_repository: Repository | None = None
    ) -> list[PullRequest]:
        source_client = self._repository_client(source_repository) if source_repository else self
        source = source_client.c.get("")
        rows = self.c.get_all(
            "merge_requests", source_branch=branch, state="all", order_by="created_at", sort="desc"
        )
        # The project endpoint includes incoming forks; branch names alone are ambiguous.
        result = []
        for row in rows:
            if row.get("source_project_id") == source["id"]:
                pr = self._to_pr(row)
                pr.source_repository = source_client.path
                pr.target_repository = self.path
                result.append(pr)
        return result

    def recent(self, limit: int) -> list[PullRequest]:
        return self._list(state="all", per_page=limit)

    def open_pull_requests(self) -> list[PullRequest]:
        source = self.c.get("")
        rows = self.c.get_all("merge_requests", state="opened", order_by="created_at", sort="desc")
        result = []
        for row in rows:
            if row.get("source_project_id") == source["id"]:
                pr = self._to_pr(row)
                pr.source_repository = source["path_with_namespace"]
                pr.target_repository = self.path
                result.append(pr)
        return result

    def request_counts(self) -> dict[str, int]:
        return {
            "requests": self.c.requests + self._instance.requests,
            "not_modified": self.c.not_modified + self._instance.not_modified,
        }

    def merge(self, number: int, *, expected_sha: str) -> PullRequest:
        if not expected_sha:
            raise ValueError("merge requires expected_sha")
        return self._to_pr(self.c.put(f"merge_requests/{number}/merge", {"sha": expected_sha}))

    def get(self, number: int) -> PullRequest:
        data = self.c.get(f"merge_requests/{number}")
        pr = self._to_pr(data)
        pr.target_repository = self.path
        source_id = data.get("source_project_id")
        if source_id:
            pr.source_repository = self.for_repository(str(source_id)).c.get("")[
                "path_with_namespace"
            ]
        return pr

    def default_branch(self) -> str:
        return (self.c.get("") or {}).get("default_branch") or ""  # GET /projects/{id}

    def merge_readiness(self, number: int) -> MergeReadiness:
        return self._readiness(self.c.get(f"merge_requests/{number}"))

    @staticmethod
    def _readiness(d: dict[str, Any]) -> MergeReadiness:
        """Map a raw MR dict to neutral readiness. Prefer the modern `detailed_merge_status`;
        fall back to the boolean `has_conflicts` (older GitLab, or an unmapped status). Anything
        else → UNKNOWN, which includes the async 'checking'/'unchecked' window."""
        status = _READINESS_IN.get(d.get("detailed_merge_status") or "")
        if status is not None:
            return status
        if d.get("has_conflicts"):
            return MergeReadiness.CONFLICT
        return MergeReadiness.UNKNOWN

    def description(self, number: int) -> str:
        return self.c.get(f"merge_requests/{number}").get("description") or ""

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
        source = self._repository_client(source_repository)
        target_client = self._repository_client(target_repository)
        target = target_client.c.get("")
        result = self._to_pr(
            source.c.post(
                "merge_requests",
                {
                    "source_branch": source_branch,
                    "target_branch": target_branch,
                    "target_project_id": target["id"],
                    "title": title,
                    "description": body,
                    "remove_source_branch": True,
                    "squash": False,
                },
            )
        )
        result.source_repository = source.path
        result.target_repository = target_client.path
        return result

    def update(self, number: int, **fields: str) -> PullRequest:
        body = {}
        if "title" in fields:
            body["title"] = fields["title"]
        if "body" in fields:
            body["description"] = fields["body"]
        if "target_branch" in fields:
            body["target_branch"] = fields["target_branch"]
        return self._to_pr(self.c.put(f"merge_requests/{number}", body))

    def close(self, number: int) -> PullRequest:
        return self._to_pr(self.c.put(f"merge_requests/{number}", {"state_event": "close"}))

    def _to_release(self, d: dict[str, Any]) -> Release:
        links = d.get("_links") or {}
        commit = d.get("commit") or {}
        return Release(
            tag=d.get("tag_name", ""),
            name=d.get("name") or d.get("tag_name", "") or "",
            target=commit.get("id", "") or "",  # GitLab resolves ref → the tagged commit sha
            web_url=links.get("self", "") or "",
            created_at=d.get("released_at") or d.get("created_at"),
        )

    def create_release(self, *, tag: str, target: str, name: str = "", notes: str = "") -> Release:
        # `ref` creates the tag server-side when it doesn't exist yet (branch name or sha).
        return self._to_release(
            self.c.post(
                "releases",
                {
                    "tag_name": tag,
                    "ref": target,
                    "name": name or tag,
                    "description": notes,
                },
            )
        )

    def latest_release(self) -> Release | None:
        # GitLab lists releases released_at-desc by default → first entry is the latest.
        out = self.c.get("releases", per_page=1)
        rels = out if isinstance(out, list) else []
        return self._to_release(rels[0]) if rels else None
