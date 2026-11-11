"""Repository activity shared with Go/TypeScript cleanup; inventory is not access."""

from __future__ import annotations

import math
import time
from typing import cast

from ..git.state import CheckoutInfo
from .identity import digest_path
from .store import JSON, ObservationStore


def record_access(store: ObservationStore, key: str, checkout: CheckoutInfo | None = None) -> None:
    """Renew only the selected checkout under the repository's external activity lease.

    @spec Maintenance never calls this operation; missing activity sorts oldest.
    Deletion protection remains mandatory.
    @link component://docs/cleanup.md
    """
    directory = store.root / "repositories" / key
    with store.locked(store.root / "locks" / "activity" / key) as owned:
        record = store.read(directory / "activity.json")
        if record is None:
            record = {"version": 1, "lastAccessAt": 0, "checkouts": {}}
        previous = record.get("lastAccessAt")
        checkouts = record.get("checkouts")
        if (
            record.get("version") != 1
            or not isinstance(previous, (int, float))
            or not math.isfinite(previous)
            or previous < 0
            or not isinstance(checkouts, dict)
        ):
            raise ValueError("Invalid repository activity record")
        now = time.time()
        record["lastAccessAt"] = max(previous, now)
        if checkout is not None:
            cast(JSON, checkouts)[digest_path(checkout.root)] = {
                "path": checkout.root,
                "commonDir": checkout.common_dir,
                "lastAccessAt": now,
            }
        store.write(directory / "activity.json", record, owned)
