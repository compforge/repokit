"""PR comment facts and domain contract; provider discussion handles stay private."""

from __future__ import annotations

import logging
from dataclasses import dataclass
from enum import StrEnum
from typing import Protocol

from .errors import ForgeError, ForgeOutcomeUnknown

# Stable within a PR: <resource>:<native-id>. Only provider adapters parse it.
CommentID = str


class CommentResolution(StrEnum):
    UNRESOLVED = "unresolved"
    RESOLVED = "resolved"
    UNSUPPORTED = "unsupported"
    UNKNOWN = "unknown"


@dataclass(frozen=True)
class CommentAnchor:
    path: str
    revision: str
    side: str | None = None
    line: int | None = None
    start_line: int | None = None
    start_side: str | None = None
    original_revision: str | None = None
    original_line: int | None = None
    original_start_line: int | None = None
    old_path: str | None = None
    outdated: bool | None = None


@dataclass(frozen=True)
class Comment:
    """One published comment. Count includes every reply and review summary.

    reply_to_id is the platform-recoverable structural parent; grouped replies
    point to their conversation root. None means no known parent.
    Resolution is projected from the containing discussion and is not acceptance.
    IDs are opaque and unique within this repository-scoped PR.
    """

    id: CommentID
    pr: int
    body: str
    author: str
    created_at: str
    reply_to_id: CommentID | None = None
    updated_at: str | None = None
    url: str | None = None
    anchor: CommentAnchor | None = None
    resolution: CommentResolution = CommentResolution.UNKNOWN
    resolved_by: str | None = None
    resolved_at: str | None = None


@dataclass(frozen=True)
class DeleteResult:
    pr: int
    comment_id: CommentID
    deleted: bool


class CommentForge(Protocol):
    """Repository-scoped comment CRUD; failure never becomes an empty inventory.

    Writes are sent once. Unconfirmed outcomes require read-side reconciliation.
    Reply appends to the target conversation; its returned parent may be the root.
    Resolution can affect
    the whole containing discussion. Deletion never silently cascades to replies.
    """

    def list_comments(self, number: int) -> list[Comment]: ...
    def get_comment(self, number: int, comment_id: CommentID) -> Comment: ...
    def create_comment(
        self, number: int, body: str, *, anchor: CommentAnchor | None = None
    ) -> Comment: ...
    def reply_comment(self, number: int, comment_id: CommentID, body: str) -> Comment: ...
    def update_comment(self, number: int, comment_id: CommentID, body: str) -> Comment: ...
    def delete_comment(self, number: int, comment_id: CommentID) -> DeleteResult: ...
    def resolve_comment(
        self, number: int, comment_id: CommentID, resolved: bool = True
    ) -> Comment: ...


def validate_body(number: int, body: str) -> None:
    if number <= 0 or not body.strip():
        raise ForgeError("a positive PR number and non-empty comment body are required")


def validate_anchor(anchor: CommentAnchor) -> None:
    if not anchor.path or not anchor.revision or anchor.side not in ("LEFT", "RIGHT"):
        raise ForgeError("anchor requires path, revision and LEFT/RIGHT side")
    if anchor.line is not None and anchor.line <= 0:
        raise ForgeError("anchor line must be positive")
    if anchor.start_line is not None and (anchor.line is None or anchor.start_line <= 0):
        raise ForgeError("anchor range requires positive start and end lines")
    if anchor.start_side is not None and anchor.start_side not in ("LEFT", "RIGHT"):
        raise ForgeError("anchor start side must be LEFT or RIGHT")


def confirm_comment(comment: Comment, *, body: str, expected_id: str | None = None) -> Comment:
    if (expected_id is not None and comment.id != expected_id) or comment.body != body:
        raise ForgeOutcomeUnknown(
            "Forge did not confirm the requested comment; read before retrying"
        )
    logging.getLogger(__name__).info(
        "comment write confirmed pr=%s comment=%s", comment.pr, comment.id
    )
    return comment
