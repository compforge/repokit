"""GitHub implementation of the flat CommentForge domain."""

from __future__ import annotations

from dataclasses import replace
from typing import Any

from .._comment_state import CommentState, observed
from .._rest import RestClient
from ..comment import (
    Comment,
    CommentAnchor,
    CommentResolution,
    DeleteResult,
    confirm_comment,
    validate_anchor,
    validate_body,
)
from ..errors import ForgeUnsupported
from ..model import ForgeError, ForgeNotFound, ForgeOutcomeUnknown


class GitHubComments(CommentState):
    c: RestClient
    g: RestClient
    owner: str
    name: str

    def _comment_graphql(self, query: str, variables: dict[str, Any]) -> dict[str, Any]:
        result = self.g.post("", {"query": query, "variables": variables})
        if (
            not isinstance(result, dict)
            or result.get("errors")
            or not isinstance(result.get("data"), dict)
        ):
            error = ForgeOutcomeUnknown if query.lstrip().startswith("mutation") else ForgeError
            raise error("GitHub GraphQL comment operation returned incomplete data")
        data: dict[str, Any] = result["data"]
        return data

    def _comment_threads(self, number: int) -> dict[str, dict[str, Any]]:
        query = """query CommentThreads($owner:String!,$name:String!,$number:Int!,$cursor:String){
        repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100,after:$cursor){
        nodes{id isResolved isOutdated resolvedBy{login} comments(first:1){nodes{fullDatabaseId}}}
        pageInfo{hasNextPage endCursor}}}}}"""
        cursor: str | None = None
        seen: set[str] = set()
        result: dict[str, dict[str, Any]] = {}
        for _ in range(100):
            data = self._comment_graphql(
                query, {"owner": self.owner, "name": self.name, "number": number, "cursor": cursor}
            )
            conn = ((data.get("repository") or {}).get("pullRequest") or {}).get("reviewThreads")
            if (
                not isinstance(conn, dict)
                or not isinstance(conn.get("nodes"), list)
                or not isinstance(conn.get("pageInfo"), dict)
            ):
                raise ForgeError("GitHub comment thread inventory is incomplete")
            for thread in conn["nodes"]:
                nodes = (thread.get("comments") or {}).get("nodes") or []
                if (
                    len(nodes) != 1
                    or not nodes[0].get("fullDatabaseId")
                    or not thread.get("id")
                    or not isinstance(thread.get("isResolved"), bool)
                ):
                    raise ForgeError("GitHub returned an invalid comment thread")
                root = str(nodes[0]["fullDatabaseId"])
                if root in result:
                    raise ForgeError("duplicate GitHub thread root")
                result[root] = thread
            info = conn["pageInfo"]
            if info.get("hasNextPage") is False:
                return result
            cursor = info.get("endCursor")
            if not cursor or cursor in seen:
                raise ForgeError("GitHub comment pagination cursor is missing or repeated")
            seen.add(cursor)
        raise ForgeError("GitHub comment thread inventory exceeds 100 pages")

    @staticmethod
    def _comment(
        number: int, row: dict[str, Any], kind: str, thread: dict[str, Any] | None = None
    ) -> Comment:
        if not row.get("id") or not isinstance(row.get("body"), str):
            raise ForgeError("GitHub returned an invalid comment")
        anchor = None
        if kind == "review" and row.get("path"):
            anchor = CommentAnchor(
                path=row["path"],
                revision=row.get("commit_id") or "",
                side=row.get("side"),
                line=row.get("line"),
                start_line=row.get("start_line"),
                start_side=row.get("start_side"),
                original_revision=row.get("original_commit_id"),
                original_line=row.get("original_line"),
                original_start_line=row.get("original_start_line"),
                outdated=thread.get("isOutdated") if thread else None,
            )
        resolution = CommentResolution.UNSUPPORTED
        if kind == "review":
            resolution = (
                CommentResolution.UNKNOWN
                if thread is None
                else (
                    CommentResolution.RESOLVED
                    if thread["isResolved"]
                    else CommentResolution.UNRESOLVED
                )
            )
        return Comment(
            id=f"{kind}:{row['id']}",
            pr=number,
            body=row["body"],
            author=(row.get("user") or {}).get("login", ""),
            created_at=row.get("created_at") or row.get("submitted_at") or "",
            updated_at=row.get("updated_at"),
            url=row.get("html_url"),
            reply_to_id=f"review:{row['in_reply_to_id']}" if row.get("in_reply_to_id") else None,
            anchor=anchor,
            resolution=resolution,
            resolved_by=(thread.get("resolvedBy") or {}).get("login") if thread else None,
        )

    def _comment_inventory(self, number: int) -> tuple[list[Comment], dict[str, dict[str, Any]]]:
        if number <= 0:
            raise ForgeError("PR number must be positive")
        threads = self._comment_threads(number)
        result = [
            self._comment(number, row, "conversation")
            for row in self.c.get_all(f"issues/{number}/comments")
        ]
        rows = self.c.get_all(f"pulls/{number}/comments")
        roots = {str(row["id"]) for row in rows if not row.get("in_reply_to_id")}
        if roots != set(threads):
            raise ForgeError("GitHub comments and threads disagree; refresh required")
        targets: dict[str, dict[str, Any]] = {}
        for row in rows:
            root = str(row.get("in_reply_to_id") or row["id"])
            if root not in roots:
                raise ForgeError("GitHub reply has no observed parent; refresh required")
            comment = self._comment(number, row, "review", threads[root])
            result.append(comment)
            targets[comment.id] = threads[root]
        for row in self.c.get_all(f"pulls/{number}/reviews"):
            if row.get("state") != "PENDING" and row.get("submitted_at") and row.get("body"):
                result.append(self._comment(number, row, "summary"))
        if len({c.id for c in result}) != len(result):
            raise ForgeError("GitHub comment inventory contains duplicate IDs")
        return sorted(result, key=lambda c: (c.created_at, c.id)), targets

    @observed
    def list_comments(self, number: int) -> list[Comment]:
        return self._comment_inventory(number)[0]

    @observed
    def get_comment(self, number: int, comment_id: str) -> Comment:
        return self._find_comment(self._comment_inventory(number)[0], comment_id)

    @staticmethod
    def _find_comment(comments: list[Comment], comment_id: str) -> Comment:
        for comment in comments:
            if comment.id == comment_id:
                return comment
        raise ForgeNotFound("comment not found in the specified PR")

    def _written_comment(
        self, number: int, row: Any, kind: str, body: str, expected: str | None = None
    ) -> Comment:
        try:
            if not isinstance(row, dict):
                raise ForgeError("missing comment response")
            return confirm_comment(
                self._comment(number, row, kind), body=body, expected_id=expected
            )
        except ForgeError as error:
            raise ForgeOutcomeUnknown(
                "GitHub did not confirm comment write; read before retrying"
            ) from error

    @observed
    def create_comment(
        self, number: int, body: str, *, anchor: CommentAnchor | None = None
    ) -> Comment:
        validate_body(number, body)
        # Verify PR membership: the issue-comment endpoint also accepts ordinary issues.
        pr = self.c.get(f"pulls/{number}")
        if pr.get("number") != number:
            raise ForgeError("GitHub did not confirm the target PR")
        payload: dict[str, Any] = {"body": body}
        if anchor is None:
            return self._written_comment(
                number, self.c.post(f"issues/{number}/comments", payload), "conversation", body
            )
        validate_anchor(anchor)
        payload.update(commit_id=anchor.revision, path=anchor.path)
        if anchor.line is None:
            payload["subject_type"] = "file"
        else:
            payload.update(line=anchor.line, side=anchor.side)
            if anchor.start_line is not None:
                payload.update(
                    start_line=anchor.start_line, start_side=anchor.start_side or anchor.side
                )
        return self._written_comment(
            number, self.c.post(f"pulls/{number}/comments", payload), "review", body
        )

    @observed
    def reply_comment(self, number: int, comment_id: str, body: str) -> Comment:
        validate_body(number, body)
        target = self.get_comment(number, comment_id)
        if not target.id.startswith("review:"):
            raise ForgeUnsupported("GitHub only supports replies within review comments")
        root_id = target.reply_to_id or target.id
        row = self.c.post(
            f"pulls/{number}/comments/{root_id.split(':')[1]}/replies", {"body": body}
        )
        result = self._written_comment(number, row, "review", body)
        if result.reply_to_id != root_id:
            raise ForgeOutcomeUnknown("GitHub did not confirm the requested reply parent")
        return replace(result, resolution=target.resolution, resolved_by=target.resolved_by)

    @observed
    def update_comment(self, number: int, comment_id: str, body: str) -> Comment:
        validate_body(number, body)
        target = self.get_comment(number, comment_id)
        kind, raw = target.id.split(":", 1)
        path = (
            f"pulls/{number}/reviews/{raw}"
            if kind == "summary"
            else f"{'issues' if kind == 'conversation' else 'pulls'}/comments/{raw}"
        )
        row = self.c.request("PUT" if kind == "summary" else "PATCH", path, body={"body": body})
        result = self._written_comment(number, row, kind, body, comment_id)
        return replace(result, resolution=target.resolution, resolved_by=target.resolved_by)

    @observed
    def delete_comment(self, number: int, comment_id: str) -> DeleteResult:
        comments = self.list_comments(number)
        target = self._find_comment(comments, comment_id)
        kind, raw = target.id.split(":", 1)
        if kind == "summary":
            raise ForgeUnsupported("GitHub does not support deleting a submitted review summary")
        if any(c.reply_to_id == comment_id for c in comments):
            raise ForgeError(
                "deleting this root could affect replies; delete replies explicitly first"
            )
        self.c.request(
            "DELETE", f"{'issues' if kind == 'conversation' else 'pulls'}/comments/{raw}"
        )
        return DeleteResult(number, comment_id, True)

    @observed
    def resolve_comment(self, number: int, comment_id: str, resolved: bool = True) -> Comment:
        comments, targets = self._comment_inventory(number)
        target = self._find_comment(comments, comment_id)
        thread = targets.get(comment_id)
        if thread is None:
            raise ForgeUnsupported("this GitHub comment does not support resolution")
        if (target.resolution == CommentResolution.RESOLVED) == resolved:
            return target
        action = "resolveReviewThread" if resolved else "unresolveReviewThread"
        data = self._comment_graphql(
            (
                "mutation CommentResolution($thread:ID!){"
                f"{action}(input:{{threadId:$thread}})"
                "{thread{id isResolved resolvedBy{login}}}}"
            ),
            {"thread": thread["id"]},
        )
        observed = (data.get(action) or {}).get("thread") or {}
        if observed.get("id") != thread["id"] or observed.get("isResolved") is not resolved:
            raise ForgeOutcomeUnknown("GitHub did not confirm comment resolution")
        return replace(
            target,
            resolution=CommentResolution.RESOLVED if resolved else CommentResolution.UNRESOLVED,
            resolved_by=(observed.get("resolvedBy") or {}).get("login"),
        )
