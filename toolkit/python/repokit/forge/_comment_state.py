"""Disposable comment observations. Provider reads remain authoritative."""

from __future__ import annotations

import logging
from collections.abc import Callable
from dataclasses import asdict
from datetime import UTC, datetime
from functools import wraps
from pathlib import Path
from typing import TypeVar, cast

from harness_common import Forge

from ..state.activity import record_access
from ..state.identity import name_part, repository_key
from ..state.store import ObservationStore, digest
from .comment import Comment, DeleteResult

F = TypeVar("F", bound=Callable[..., Comment | list[Comment] | DeleteResult])


def observed(operation: F) -> F:
    """Record successful results without converting remote success into a retry."""

    @wraps(operation)
    def invoke(
        self: CommentState, number: int, *args: object, **kwargs: object
    ) -> Comment | list[Comment] | DeleteResult:
        result = operation(self, number, *args, **kwargs)
        try:
            self._capture_comments(number, result)
        except (OSError, ValueError):
            logging.getLogger(__name__).warning(
                "comment observation publication failed repository=%s number=%s",
                self.path,
                number,
                exc_info=True,
            )
        return result

    return cast(F, invoke)


class CommentState:
    home: Path
    forge: Forge
    path: str
    provider: str
    host: str
    _token: str

    def _capture_comments(
        self, number: int, result: Comment | list[Comment] | DeleteResult
    ) -> None:
        store = ObservationStore(self.home)
        key = repository_key(self.forge.name, self.path)
        directory = store.root / "repositories" / key
        root = directory / "prs" / str(number) / "comments"
        connection = digest(
            [
                self.forge.name,
                digest([self.provider, self.forge.name, self.host.lower(), self._token]),
            ]
        )
        repository: dict[str, object] = {"forge": {"name": self.forge.name}, "path": self.path}
        record_access(store, key, relative=("prs", str(number), "comments"))
        with store.locked(root) as owned:
            store.write(directory / "repository.json", repository, owned)
            if isinstance(result, list):
                for path in root.glob(f"*/{connection}.json"):
                    if not path.parent.is_symlink():
                        owned()
                        path.unlink(missing_ok=True)
            if isinstance(result, DeleteResult):
                owned()
                (root / name_part(result.comment_id) / f"{connection}.json").unlink(missing_ok=True)
                return
            rows = result if isinstance(result, list) else [result]
            for row in rows:
                value = _camel(asdict(row))
                value["pr"] = {"repository": repository, "number": number}
                store.write(
                    root / name_part(row.id) / f"{connection}.json",
                    {
                        "version": 1,
                        "connection": connection,
                        "observedAt": datetime.now(UTC).isoformat(),
                        "comment": value,
                    },
                    owned,
                )


def _camel(value: dict[str, object]) -> dict[str, object]:
    return {
        name.split("_")[0] + "".join(part.title() for part in name.split("_")[1:]): _camel(item)
        if isinstance(item, dict)
        else item
        for name, item in value.items()
        if item is not None
    }
