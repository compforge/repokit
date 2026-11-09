"""Native PR observations with durable terminal facts and bounded maintenance."""

from __future__ import annotations

import logging
import time
from collections.abc import Callable
from datetime import UTC, datetime
from pathlib import Path
from threading import Event, Thread
from typing import Literal, cast

from harness_common import Forge as ForgeIdentity
from harness_common import Repository

from ..._state_paths import repository_key
from ...budget import current_budget, operation
from ..model import Forge, ForgeError, ForgeRateLimited, PullRequest
from .model import PullRequestKey, PullRequestStatus, PullRequestStatusUnavailable
from .store import JSON, ObservationStorageError, ObservationStore, digest

logger = logging.getLogger(__name__)


def repository_json(repo: Repository) -> JSON:
    return {"forge": {"name": repo.forge.name}, "path": repo.path}


def key_json(key: PullRequestKey) -> JSON:
    return {"repository": repository_json(key.repository), "number": key.number}


def key_from_json(value: JSON) -> PullRequestKey:
    repo = cast(JSON, value["repository"])
    forge = cast(JSON, repo["forge"])
    return PullRequestKey(
        Repository(ForgeIdentity(str(forge["name"])), str(repo["path"])), int(str(value["number"]))
    )


def iso(at: float) -> str:
    return datetime.fromtimestamp(at, UTC).isoformat(timespec="milliseconds").replace("+00:00", "Z")


def epoch(value: object) -> float:
    return datetime.fromisoformat(str(value).replace("Z", "+00:00")).timestamp() if value else 0


class PullRequestObserver:
    """Query state; the observer owns persistence, freshness, retry and maintenance.

    @spec Merged is immutable and is excluded from routine maintenance.
    @spec Closed remains observable and can reopen. Failures do not erase successful facts.
    @link component://docs/pr.md
    @case tests/test_pr_observation.py
    """

    def __init__(
        self,
        reader: Forge,
        *,
        forge: str,
        state_dir: str | Path,
        connection: str,
        open_interval: float = 60,
        closed_interval: float = 600,
        retry_interval: float = 30,
        timeout: float = 20,
        now: Callable[[], float] = time.time,
    ):
        if (
            not forge
            or not connection
            or min(open_interval, closed_interval, retry_interval, timeout) <= 0
        ):
            raise ValueError("nonempty identities and positive intervals are required")
        self.reader, self.forge = reader, forge
        self.store = ObservationStore(state_dir)
        self.connection = digest([forge, connection])
        self.open_interval, self.closed_interval = open_interval, closed_interval
        self.retry_interval, self.timeout, self.now = retry_interval, timeout, now
        self._stop = Event()
        self._thread: Thread | None = None

    def path(self, key: PullRequestKey) -> Path:
        if key.repository.forge.name != self.forge or not key.repository.path or key.number < 1:
            raise ValueError("invalid PR identity for this observer")
        return (
            self.store.root
            / "repositories"
            / repository_key(self.forge, key.repository.path)
            / "prs"
            / str(key.number)
            / "observation.json"
        )

    def _stored(self, path: Path) -> JSON | None:
        record = self.store.read(path)
        if record and (
            record.get("version") != 1
            or not isinstance(record.get("observation"), dict)
            or not isinstance(record.get("scopeIds"), list)
            or type(record.get("failures")) is not int
        ):
            raise ObservationStorageError(f"Invalid PR observation {path}")
        return record if record and record.get("connection") == self.connection else None

    def _get(self, key: PullRequestKey, *, verify: bool = False, refresh: bool = False) -> JSON:
        with operation(timeout=self.timeout, cancel=self._stop):
            budget = current_budget(self.timeout)
            path = self.path(key)
            with self.store.locked(path) as owned:
                previous = self._stored(path)
                if previous is None:
                    legacy = (
                        self.store.root
                        / "repositories"
                        / digest([self.forge, key.repository.path])
                        / "prs"
                        / str(key.number)
                        / "observation.json"
                    )
                    previous = self._stored(legacy)
                    if previous is not None:
                        self.store.write(path, previous, owned)
                old = cast(JSON, previous["observation"]) if previous else {}
                if old and old.get("key") != key_json(key):
                    raise ObservationStorageError(f"PR identity mismatch at {path}")
                snapshot = cast(JSON, old.get("snapshot", {}))
                # The terminal lifecycle survives even explicit association verification.
                if snapshot.get("state") == "merged" and not refresh:
                    return old
                if not verify and not refresh and epoch(old.get("nextRefreshAt")) > self.now():
                    if old.get("lastError"):
                        raise PullRequestStatusUnavailable(
                            f"PR retry pending: {key.repository.path}#{key.number}"
                        )
                    if snapshot:
                        return old
                attempted = iso(self.now())
                try:
                    pr = self.reader.for_repository(key.repository.path).get(key.number)
                    budget.remaining()
                    if (
                        pr.number != key.number
                        or pr.state not in {"open", "closed", "merged"}
                        or pr.target_repository != key.repository.path
                    ):
                        raise ForgeError("Forge returned invalid PR identity or lifecycle")
                    if snapshot.get("state") == "merged" and pr.state != "merged":
                        raise ForgeError("Forge contradicted a confirmed merged lifecycle")
                    snapshot = {
                        **key_json(key),
                        "state": pr.state,
                        "title": pr.title,
                        "url": pr.web_url,
                        "headSha": pr.sha,
                        "sourceBranch": pr.source_branch,
                        "targetBranch": pr.target_branch,
                    }
                    if pr.source_repository:
                        snapshot["sourceRepository"] = repository_json(
                            Repository(key.repository.forge, pr.source_repository)
                        )
                    observation: JSON = {
                        "key": key_json(key),
                        "snapshot": snapshot,
                        "observedAt": iso(self.now()),
                        "attemptedAt": attempted,
                    }
                    if pr.state != "merged":
                        interval = (
                            self.closed_interval if pr.state == "closed" else self.open_interval
                        )
                        observation["nextRefreshAt"] = iso(self.now() + interval)
                    failures = 0
                except (ForgeError, OSError) as exc:
                    if self._stop.is_set() or isinstance(exc, InterruptedError):
                        raise
                    failures = int(str(previous["failures"])) + 1 if previous else 1
                    delay = min(3600, self.retry_interval * 2 ** min(failures - 1, 10))
                    if isinstance(exc, ForgeRateLimited):
                        delay = max(delay, exc.retry_at - self.now())
                    observation = {
                        **old,
                        "key": key_json(key),
                        "attemptedAt": attempted,
                        "nextRefreshAt": iso(self.now() + delay),
                        "lastError": {
                            "stage": "refresh",
                            "kind": "rate_limit"
                            if isinstance(exc, ForgeRateLimited)
                            else "transport",
                            "message": str(exc),
                            "key": key_json(key),
                        },
                    }
                    logger.warning(
                        "PR observation failed repository=%s number=%s error=%s",
                        key.repository.path,
                        key.number,
                        exc,
                    )
                record: JSON = {
                    "version": 1,
                    "connection": self.connection,
                    "observation": observation,
                    "scopeIds": previous["scopeIds"] if previous else [],
                    "failures": failures,
                }
                self.store.write(
                    path.parent.parent.parent / "repository.json",
                    repository_json(key.repository),
                    owned,
                )
                self.store.write(path, record, owned)
                if observation.get("lastError"):
                    raise PullRequestStatusUnavailable(
                        f"PR lifecycle unavailable: {key.repository.path}#{key.number}"
                    )
                return observation

    def get_pull_request(self, key: PullRequestKey, *, refresh: bool = False) -> PullRequestStatus:
        observed = self._get(key, refresh=refresh)
        snapshot = cast(JSON, observed["snapshot"])
        return PullRequestStatus(
            key,
            cast(Literal["open", "closed", "merged"], snapshot["state"]),
            str(observed["observedAt"]),
        )

    def _snapshot(self, key: PullRequestKey, *, verify: bool = False) -> PullRequest:
        value = cast(JSON, self._get(key, verify=verify)["snapshot"])
        source = cast(JSON, value.get("sourceRepository", {}))
        return PullRequest(
            number=key.number,
            state=str(value["state"]),
            title=str(value.get("title", "")),
            source_branch=str(value.get("sourceBranch", "")),
            target_branch=str(value.get("targetBranch", "")),
            web_url=str(value.get("url", "")),
            sha=str(value.get("headSha", "")),
            source_repository=str(source.get("path", "")),
            target_repository=key.repository.path,
        )

    def maintain(self, *, limit: int = 5) -> tuple[str, ...]:
        """One bounded round; failed objects cannot monopolize healthy observations."""
        if limit < 1:
            raise ValueError("limit must be positive")
        due: list[tuple[float, PullRequestKey]] = []
        issues: list[str] = []
        for path in self.store.root.glob("repositories/*/prs/*/observation.json"):
            try:
                record = self._stored(path)
                if not record:
                    continue
                observation = cast(JSON, record["observation"])
                canonical = self.path(key_from_json(cast(JSON, observation["key"])))
                if path != canonical and self._stored(canonical) is not None:
                    continue
                if cast(JSON, observation.get("snapshot", {})).get("state") == "merged":
                    continue
                at = epoch(observation.get("nextRefreshAt"))
                if at <= self.now():
                    due.append((at, key_from_json(cast(JSON, observation["key"]))))
            except (OSError, ValueError, KeyError) as exc:
                issues.append(str(exc))
        for _, key in sorted(
            due, key=lambda item: (item[0], item[1].repository.path, item[1].number)
        )[:limit]:
            try:
                self.get_pull_request(key)
            except (ForgeError, OSError) as exc:
                if self._stop.is_set():
                    break
                issues.append(str(exc))
        return tuple(issues)

    def start(self) -> None:
        """Opt in to periodic maintenance; close cancels cooperative work and joins it."""
        if self._stop.is_set():
            raise RuntimeError("observer is closed")
        if self._thread is not None:
            return

        def run() -> None:
            while not self._stop.is_set():
                try:
                    self.maintain()
                except (ForgeError, OSError) as exc:
                    logger.warning("PR maintenance failed: %s", exc)
                self._stop.wait(min(self.open_interval, self.closed_interval, self.retry_interval))

        self._thread = Thread(target=run, name="repokit-pr-observer", daemon=True)
        self._thread.start()

    def close(self) -> None:
        self._stop.set()
        if self._thread:
            self._thread.join()
