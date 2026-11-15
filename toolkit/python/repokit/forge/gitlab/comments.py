"""GitLab CommentForge implementation; discussion membership stays private."""

from __future__ import annotations

import hashlib
from typing import Any
from urllib.parse import quote

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


class GitLabComments:
    c: RestClient

    @staticmethod
    def _resolution(discussion: dict[str, Any]) -> CommentResolution:
        if discussion.get("individual_note") is True:
            return CommentResolution.UNSUPPORTED
        candidates = [
            n
            for n in discussion["notes"]
            if not n.get("system") and n.get("resolvable") is not False
        ]
        if not candidates:
            return CommentResolution.UNSUPPORTED
        if any(n.get("resolved") is False for n in candidates):
            return CommentResolution.UNRESOLVED
        if all(n.get("resolved") is True for n in candidates):
            return CommentResolution.RESOLVED
        return CommentResolution.UNKNOWN

    @staticmethod
    def _comment(
        number: int, row: dict[str, Any], discussion: dict[str, Any] | None = None
    ) -> Comment:
        if not row.get("id") or not isinstance(row.get("body"), str):
            raise ForgeError("GitLab returned an invalid comment")
        pos = row.get("position")
        anchor = None
        if isinstance(pos, dict):
            side = (
                "RIGHT"
                if pos.get("new_line") is not None
                else "LEFT"
                if pos.get("old_line") is not None
                else None
            )
            start = (pos.get("line_range") or {}).get("start") or {}
            anchor = CommentAnchor(
                path=pos.get("new_path") or pos.get("old_path") or "",
                revision=pos.get("head_sha") or "",
                side=side,
                line=pos.get("new_line") if side == "RIGHT" else pos.get("old_line"),
                start_line=start.get("new_line")
                if start.get("type") == "new"
                else start.get("old_line"),
                start_side={"new": "RIGHT", "old": "LEFT"}.get(start.get("type", "")),
                original_revision=pos.get("base_sha"),
                old_path=pos.get("old_path"),
            )
        return Comment(
            id=f"note:{row['id']}",
            pr=number,
            body=row["body"],
            author=(row.get("author") or {}).get("username", ""),
            created_at=row.get("created_at") or "",
            updated_at=row.get("updated_at"),
            url=row.get("url"),
            anchor=anchor,
            resolution=GitLabComments._resolution(discussion)
            if discussion
            else CommentResolution.UNKNOWN,
            resolved_by=(row.get("resolved_by") or {}).get("username"),
            resolved_at=row.get("resolved_at"),
        )

    def _comment_inventory(self, number: int) -> tuple[list[Comment], dict[str, dict[str, Any]]]:
        if number <= 0:
            raise ForgeError("MR number must be positive")
        comments: list[Comment] = []
        targets: dict[str, dict[str, Any]] = {}
        for discussion in self.c.get_all(f"merge_requests/{number}/discussions"):
            if (
                not discussion.get("id")
                or not isinstance(discussion.get("notes"), list)
                or not isinstance(discussion.get("individual_note"), bool)
            ):
                raise ForgeError("GitLab returned an incomplete comment discussion")
            for note in discussion["notes"]:
                if note.get("system"):
                    continue
                comment = self._comment(number, note, discussion)
                if comment.id in targets:
                    raise ForgeError("GitLab comment inventory contains duplicate IDs")
                comments.append(comment)
                targets[comment.id] = discussion
        return sorted(comments, key=lambda c: (c.created_at, c.id)), targets

    def list_comments(self, number: int) -> list[Comment]:
        return self._comment_inventory(number)[0]

    def _target(self, number: int, comment_id: str) -> tuple[Comment, dict[str, Any]]:
        comments, targets = self._comment_inventory(number)
        for comment in comments:
            if comment.id == comment_id:
                return comment, targets[comment_id]
        raise ForgeNotFound("comment not found in the specified MR")

    def get_comment(self, number: int, comment_id: str) -> Comment:
        return self._target(number, comment_id)[0]

    def _written_comment(
        self,
        number: int,
        row: Any,
        body: str,
        expected: str | None = None,
        discussion: dict[str, Any] | None = None,
    ) -> Comment:
        try:
            if not isinstance(row, dict):
                raise ForgeError("missing comment response")
            return confirm_comment(
                self._comment(number, row, discussion), body=body, expected_id=expected
            )
        except ForgeError as error:
            raise ForgeOutcomeUnknown(
                "GitLab did not confirm comment write; read before retrying"
            ) from error

    def create_comment(
        self, number: int, body: str, *, anchor: CommentAnchor | None = None
    ) -> Comment:
        validate_body(number, body)
        payload: dict[str, Any] = {"body": body}
        if anchor:
            validate_anchor(anchor)
            assert anchor.side is not None
            versions = self.c.get_all(f"merge_requests/{number}/versions")
            version = next(
                (v for v in versions if v.get("head_commit_sha") == anchor.revision), None
            )
            if (
                version is None
                or not version.get("base_commit_sha")
                or not version.get("start_commit_sha")
            ):
                raise ForgeError("anchor revision is not an observed MR diff version")
            position: dict[str, Any] = {
                "position_type": "file" if anchor.line is None else "text",
                "head_sha": anchor.revision,
                "base_sha": version["base_commit_sha"],
                "start_sha": version["start_commit_sha"],
                "new_path": anchor.path,
                "old_path": anchor.old_path or anchor.path,
            }
            if anchor.line is not None:
                position["new_line" if anchor.side == "RIGHT" else "old_line"] = anchor.line
                if anchor.start_line is not None:

                    def endpoint(line: int, side: str) -> dict[str, Any]:
                        old, new = (line, 0) if side == "LEFT" else (0, line)
                        digest = hashlib.sha1(anchor.path.encode()).hexdigest()
                        return {
                            "line_code": f"{digest}_{old}_{new}",
                            "type": "old" if side == "LEFT" else "new",
                            "old_line": old or None,
                            "new_line": new or None,
                        }

                    position["line_range"] = {
                        "start": endpoint(anchor.start_line, anchor.start_side or anchor.side),
                        "end": endpoint(anchor.line, anchor.side),
                    }
            payload["position"] = position
        row = self.c.post(f"merge_requests/{number}/discussions", payload)
        if (
            not isinstance(row, dict)
            or not row.get("id")
            or not isinstance(row.get("notes"), list)
            or len(row["notes"]) != 1
            or not isinstance(row.get("individual_note"), bool)
        ):
            raise ForgeOutcomeUnknown("GitLab did not confirm a new comment")
        return self._written_comment(number, row["notes"][0], body, discussion=row)

    def reply_comment(self, number: int, comment_id: str, body: str) -> Comment:
        validate_body(number, body)
        target, discussion = self._target(number, comment_id)
        notes = [n for n in discussion["notes"] if not n.get("system")]
        if not notes or target.id != f"note:{notes[0]['id']}":
            raise ForgeUnsupported("GitLab cannot preserve a direct reply to a non-root comment")
        path = f"merge_requests/{number}/discussions/{quote(str(discussion['id']), safe='')}/notes"
        row = self.c.post(path, {"body": body})
        # No direct parent is returned by GitLab, even after an explicit reply.
        return self._written_comment(number, row, body, discussion=discussion)

    def _note_path(self, number: int, comment_id: str, discussion: dict[str, Any]) -> str:
        raw = quote(comment_id.split(":", 1)[1], safe="")
        if discussion["individual_note"]:
            return f"merge_requests/{number}/notes/{raw}"
        thread = quote(str(discussion["id"]), safe="")
        return f"merge_requests/{number}/discussions/{thread}/notes/{raw}"

    def update_comment(self, number: int, comment_id: str, body: str) -> Comment:
        validate_body(number, body)
        _, discussion = self._target(number, comment_id)
        row = self.c.put(self._note_path(number, comment_id, discussion), {"body": body})
        return self._written_comment(number, row, body, comment_id, discussion)

    def delete_comment(self, number: int, comment_id: str) -> DeleteResult:
        _, discussion = self._target(number, comment_id)
        notes = [n for n in discussion["notes"] if not n.get("system")]
        if len(notes) > 1 and comment_id == f"note:{notes[0]['id']}":
            raise ForgeError(
                "deleting this root could affect replies; delete replies explicitly first"
            )
        self.c.request("DELETE", self._note_path(number, comment_id, discussion))
        return DeleteResult(number, comment_id, True)

    def resolve_comment(self, number: int, comment_id: str, resolved: bool = True) -> Comment:
        target, discussion = self._target(number, comment_id)
        if target.resolution == CommentResolution.UNSUPPORTED:
            raise ForgeUnsupported("this GitLab comment does not support resolution")
        desired = CommentResolution.RESOLVED if resolved else CommentResolution.UNRESOLVED
        if target.resolution == desired:
            return target
        result = self.c.put(
            f"merge_requests/{number}/discussions/{quote(str(discussion['id']), safe='')}",
            {"resolved": resolved},
        )
        if (
            not isinstance(result, dict)
            or result.get("id") != discussion["id"]
            or not result.get("notes")
            or self._resolution(result) != desired
        ):
            raise ForgeOutcomeUnknown("GitLab did not confirm comment resolution")
        note = next((n for n in result["notes"] if f"note:{n.get('id')}" == comment_id), None)
        if note is None:
            raise ForgeOutcomeUnknown("GitLab resolution response omitted the requested comment")
        return self._comment(number, note, result)
