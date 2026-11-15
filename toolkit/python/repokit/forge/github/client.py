"""GitHubForge — the GitHub adapter for the `Forge` port.

Maps GitHub's pull-request REST surface onto the neutral domain: PR `number` (already
neutral), `/pulls` paths, `Authorization: Bearer` auth. GitHub's state model (open/closed +
a separate `merged`/`merged_at`) collapses to the neutral `open|merged|closed` here — that
normalization is exactly what this adapter exists for. The recent-window *policy* is not
here — it's the caller, composed over `recent` + `get`.
"""

from __future__ import annotations

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
    ForgeNotFound,
    Page,
    PullRequest,
    Release,
)
from .comments import GitHubComments


class GitHubForge(GitHubComments, Forge):
    provider = "github"

    def __init__(
        self,
        host: str,
        owner: str,
        name: str,
        token: str,
        *,
        forge: str | None = None,
        timeout: int = 10,
        home: str | Path | None = None,
    ):
        host = host.lower()
        self.forge = ForgeIdentity(
            forge if forge is not None else ("github" if host == "github.com" else host)
        )
        # github.com → api.github.com; GitHub Enterprise → https://<host>/api/v3
        api = "https://api.github.com" if host == "github.com" else f"https://{host}/api/v3"
        graphql_api = (
            "https://api.github.com/graphql"
            if host == "github.com"
            else f"https://{host}/api/graphql"
        )
        self.owner, self.name = owner, name
        self.host, self._token, self.timeout = host, token, timeout
        self.home = resolve_home(home)
        self.path = f"{owner}/{name}"
        headers = {
            "Authorization": f"Bearer {token}",
            "Accept": "application/vnd.github+json",
            "X-GitHub-Api-Version": "2022-11-28",
        }
        state = HttpState(self.home / "forge-http", host, token)
        self.c = RestClient(f"{api}/repos/{owner}/{name}", headers, timeout=timeout, state=state)
        self.g = RestClient(graphql_api, headers, timeout=timeout, state=state, resource="graphql")
        self._instance = RestClient(api, headers, timeout=timeout, state=state)

    def list_repositories(
        self, *, page_size: int | None = None, cursor: str | None = None
    ) -> Page[Repository]:
        scope = f"github:{self.forge.name}:{self.host}:repositories"
        page, size = page_position(scope, page_size, cursor)
        rows = self._instance.get(
            "user/repos", sort="full_name", direction="asc", per_page=size, page=page
        )
        if not isinstance(rows, list):
            raise ForgeError("GitHub repository page must be a list")
        if len(rows) > size:
            raise ForgeError("Repository response exceeds requested page size")
        items = []
        for row in rows:
            path = row.get("full_name") if isinstance(row, dict) else None
            if (
                not isinstance(path, str)
                or len(path.split("/")) != 2
                or any(p in ("", ".", "..") for p in path.split("/"))
            ):
                raise ForgeError("Invalid GitHub repository full_name")
            items.append(Repository(self.forge, path))
        return Page(tuple(items), page_continuation(scope, page, size, len(rows) >= size))

    def repository(self) -> Repository:
        data = self.c.get("")
        parent = (data.get("parent") or {}).get("full_name") or None
        return Repository(
            self.forge,
            data["full_name"],
            forked_from=parent,
        )

    def for_repository(self, path: str) -> GitHubForge:
        owner, sep, name = path.partition("/")
        if not sep or not owner or not name or "/" in name:
            raise ValueError(f"invalid GitHub repository path: {path!r}")
        return GitHubForge(
            self.host,
            owner,
            name,
            self._token,
            forge=self.forge.name,
            timeout=self.timeout,
            home=self.home,
        )

    def _repository_client(self, repository: Repository) -> GitHubForge:
        forge = self.forge
        if repository.forge != forge:
            raise ValueError(
                f"repository {repository.id()!r} does not belong to Forge {forge.name!r}"
            )
        return self.for_repository(repository.path)

    def _to_pr(self, d: dict[str, Any]) -> PullRequest:
        # `merged` is only on the single-PR response; list items carry `merged_at`.
        merged = bool(d.get("merged") or d.get("merged_at"))
        gh_state = d.get("state", "")
        state = "merged" if merged else ("open" if gh_state == "open" else "closed")
        return PullRequest(
            number=int(d["number"]),
            title=d.get("title", ""),
            state=state,
            source_branch=(d.get("head") or {}).get("ref", ""),
            target_branch=(d.get("base") or {}).get("ref", ""),
            web_url=d.get("html_url", ""),
            sha=(d.get("head") or {}).get("sha", "") or "",
            updated_at=d.get("updated_at"),
            source_repository=((d.get("head") or {}).get("repo") or {}).get("full_name", ""),
            target_repository=((d.get("base") or {}).get("repo") or {}).get("full_name", ""),
        )

    def _list(self, **params: Any) -> list[PullRequest]:
        params.setdefault("state", "all")
        params.setdefault("sort", "created")
        params.setdefault("direction", "desc")
        out = self.c.get("pulls", **params)
        return [self._to_pr(d) for d in out] if isinstance(out, list) else []

    def prs_for_branch(
        self, branch: str, *, source_repository: Repository | None = None
    ) -> list[PullRequest]:
        source = self._repository_client(source_repository) if source_repository else self
        owner = source.path.split("/", 1)[0]
        rows = self.c.get_all(
            "pulls", head=f"{owner}:{branch}", state="all", sort="created", direction="desc"
        )
        # GitHub filters by owner, not repository; same-owner forks can share branch names.
        return [
            self._to_pr(row)
            for row in rows
            if ((row.get("head") or {}).get("repo") or {}).get("full_name", "").lower()
            == source.path.lower()
        ]

    def recent(self, limit: int) -> list[PullRequest]:
        return self._list(per_page=limit)

    def open_pull_requests(self) -> list[PullRequest]:
        rows = self.c.get_all("pulls", state="open", sort="created", direction="desc")
        return [
            self._to_pr(row)
            for row in rows
            if ((row.get("head") or {}).get("repo") or {}).get("full_name", "").lower()
            == self.path.lower()
        ]

    def request_counts(self) -> dict[str, int]:
        return {
            "requests": self.c.requests + self.g.requests + self._instance.requests,
            "not_modified": self.c.not_modified + self.g.not_modified + self._instance.not_modified,
        }

    def merge(self, number: int, *, expected_sha: str) -> PullRequest:
        if not expected_sha:
            raise ValueError("merge requires expected_sha")
        result = self.c.put(f"pulls/{number}/merge", {"sha": expected_sha})
        if not isinstance(result, dict) or result.get("merged") is not True:
            raise ForgeError("GitHub did not confirm merge")
        return self.get(number)

    def get(self, number: int) -> PullRequest:
        return self._to_pr(self.c.get(f"pulls/{number}"))

    def default_branch(self) -> str:
        return (self.c.get("") or {}).get("default_branch") or ""  # GET /repos/{owner}/{name}

    def description(self, number: int) -> str:
        return self.c.get(f"pulls/{number}").get("body") or ""

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
        target = self._repository_client(target_repository)
        return self._to_pr(
            target.c.post(
                "pulls",
                {
                    "title": title,
                    "head": f"{source.path.split('/', 1)[0]}:{source_branch}",
                    "head_repo": source.path.split("/", 1)[1],
                    "base": target_branch,
                    "body": body,
                },
            )
        )

    def update(self, number: int, **fields: str) -> PullRequest:
        body = {}
        if "title" in fields:
            body["title"] = fields["title"]
        if "body" in fields:
            body["body"] = fields["body"]
        if "target_branch" in fields:
            body["base"] = fields["target_branch"]
        return self._to_pr(self.c.patch(f"pulls/{number}", body))

    def close(self, number: int) -> PullRequest:
        return self._to_pr(self.c.patch(f"pulls/{number}", {"state": "closed"}))

    def _to_release(self, d: dict[str, Any]) -> Release:
        return Release(
            tag=d.get("tag_name", ""),
            name=d.get("name") or d.get("tag_name", "") or "",
            target=d.get("target_commitish", "") or "",
            web_url=d.get("html_url", ""),
            created_at=d.get("published_at") or d.get("created_at"),
        )

    def create_release(self, *, tag: str, target: str, name: str = "", notes: str = "") -> Release:
        return self._to_release(
            self.c.post(
                "releases",
                {
                    "tag_name": tag,
                    "target_commitish": target,
                    "name": name or tag,
                    "body": notes,
                },
            )
        )

    def latest_release(self) -> Release | None:
        # /releases/latest is the newest full release (excludes drafts + prereleases) — the
        # right baseline for an increment check. 404 = no releases yet → the first release.
        try:
            return self._to_release(self.c.get("releases/latest"))
        except ForgeNotFound:
            return None
