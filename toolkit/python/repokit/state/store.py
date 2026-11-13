"""Atomic records and the shared proper-lockfile directory lease protocol."""

from __future__ import annotations

import hashlib
import json
import os
import time
import uuid
from collections.abc import Callable, Iterator
from contextlib import contextmanager
from pathlib import Path
from threading import Event, RLock, Thread
from typing import cast

from ..budget import current_budget
from ..errors import RepoKitError

JSON = dict[str, object]


def digest(value: object) -> str:
    return hashlib.sha256(
        json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode()
    ).hexdigest()


class ObservationStorageError(RepoKitError, OSError):
    """A successful observation could not be durably read or published."""

    def __init__(self, message: str = "") -> None:
        super().__init__("state.storage_failed", message)


class ObservationStore:
    def __init__(self, root: str | Path):
        if not str(root).strip():
            raise ValueError("home must be nonempty")
        self.root = Path(root).expanduser().resolve()

    def read(self, path: Path) -> JSON | None:
        try:
            value = json.loads(path.read_text())
            if not isinstance(value, dict):
                raise ValueError("expected an object")
            return cast(JSON, value)
        except FileNotFoundError:
            return None
        except (ValueError, OSError) as exc:
            raise ObservationStorageError(f"Cannot read repository state {path}") from exc

    def write(self, path: Path, value: JSON, assert_owned: Callable[[], None]) -> None:
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        temporary = path.with_name(f"{path.name}.{uuid.uuid4()}.tmp")
        try:
            fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
            with os.fdopen(fd, "w") as stream:
                json.dump(value, stream, ensure_ascii=False, separators=(",", ":"))
                stream.flush()
                os.fsync(stream.fileno())
            assert_owned()
            os.replace(temporary, path)
            fd = os.open(path.parent, os.O_RDONLY)
            try:
                os.fsync(fd)
            finally:
                os.close(fd)
        except OSError as exc:
            raise ObservationStorageError(f"Cannot persist repository state {path}") from exc
        finally:
            temporary.unlink(missing_ok=True)

    @contextmanager
    def locked(self, path: Path) -> Iterator[Callable[[], None]]:
        """An empty .lock directory, stale after 30s, renewed every 10s.

        @spec Python and TypeScript serialize refresh and publication using the same lease.
        @link component://docs/pr.md
        No owner files: proper-lockfile releases with rmdir. The inode and last
        mtime fence this owner after a lease has been reclaimed by another process.
        """
        path.parent.mkdir(parents=True, exist_ok=True, mode=0o700)
        lock = Path(f"{path}.lock")
        budget = current_budget(30)
        while True:
            budget.remaining()
            try:
                lock.mkdir(mode=0o700)
                break
            except FileExistsError:
                try:
                    if time.time() - lock.stat().st_mtime > 30:
                        lock.rmdir()
                        continue
                except FileNotFoundError:
                    continue
                time.sleep(min(0.025, budget.remaining()))
        identity = lock.stat()
        stamp = identity.st_mtime_ns
        stopped = Event()
        compromised: list[OSError] = []
        mutex = RLock()

        def owned() -> None:
            with mutex:
                if compromised:
                    raise ObservationStorageError(
                        "repository state lease compromised"
                    ) from compromised[0]
                current = lock.stat()
                if (
                    current.st_ino != identity.st_ino
                    or current.st_mtime_ns != stamp
                    or time.time() - current.st_mtime > 30
                ):
                    raise ObservationStorageError("repository state lease replaced or expired")

        def renew() -> None:
            nonlocal stamp
            while not stopped.wait(10):
                try:
                    with mutex:
                        owned()
                        os.utime(lock, None)
                        stamp = lock.stat().st_mtime_ns
                except OSError as exc:
                    compromised.append(exc)
                    return

        thread = Thread(target=renew, name="repokit-pr-lease", daemon=True)
        thread.start()
        try:
            yield owned
        finally:
            stopped.set()
            thread.join()
            try:
                owned()
                lock.rmdir()
            except OSError:
                # A reclaimed lease belongs to the next owner; never remove it.
                pass
