"""Object-local access and checkout registration; maintenance does not renew access."""

from __future__ import annotations

import math
import time
from collections.abc import Callable
from pathlib import Path

from ..git.state import CheckoutInfo
from .identity import digest_path
from .store import ObservationStore


def touch_access(store: ObservationStore, directory: Path, owned: Callable[[], None]) -> None:
    record = store.read(directory / "access.json") or {"version": 1, "lastAccessAt": 0}
    previous = record.get("lastAccessAt")
    if (
        record.get("version") != 1
        or isinstance(previous, bool)
        or not isinstance(previous, (int, float))
        or not math.isfinite(previous)
        or previous < 0
    ):
        raise ValueError("invalid resource access record")
    store.write(
        directory / "access.json", {"version": 1, "lastAccessAt": max(previous, time.time())}, owned
    )


def record_access(
    store: ObservationStore,
    key: str,
    checkout: CheckoutInfo | None = None,
    *,
    relative: tuple[str, ...] = (),
) -> None:
    if (
        not key
        or Path(key).name != key
        or key in (".", "..")
        or any(not p or Path(p).name != p or p in (".", "..") for p in relative)
    ):
        raise ValueError("invalid resource path")
    directory = store.root / "repositories" / key
    with (
        store.locked(store.root / "locks" / "activity" / key),
        store.locked(directory / "access") as owned,
    ):
        touch_access(store, directory, owned)
        if len(relative) > 2 and relative[0] in ("prs", "clones"):
            touch_access(store, directory.joinpath(*relative[:2]), owned)
        if relative:
            touch_access(store, directory.joinpath(*relative), owned)
        if checkout is not None:
            clone = directory / "clones" / digest_path(checkout.common_dir)
            path = clone / "checkouts" / digest_path(checkout.root)
            store.write(
                clone / "clone.json", {"version": 1, "commonDir": checkout.common_dir}, owned
            )
            row = store.read(path / "checkout.json") or {}
            row.update(
                version=1,
                clone=digest_path(checkout.common_dir),
                commonDir=checkout.common_dir,
                path=checkout.root,
            )
            store.write(path / "checkout.json", row, owned)
            touch_access(store, clone, owned)
            touch_access(store, path, owned)
