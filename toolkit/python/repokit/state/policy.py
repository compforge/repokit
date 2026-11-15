"""Home defaults and repository overrides for soft resource budgets."""

from __future__ import annotations

from dataclasses import dataclass
from pathlib import Path

from .home import resolve_home
from .store import JSON, ObservationStore


@dataclass(frozen=True)
class CleanupPolicy:
    repository: int = 100
    pr: int = 100
    comment: int = 5000
    branch: int = 200
    validation: int = 1000

    def __post_init__(self) -> None:
        for value in (self.repository, self.pr, self.comment, self.branch, self.validation):
            if type(value) is not int or value < 0:
                raise ValueError("cleanup max_count must be a nonnegative integer")


def policy_documents(home: str | Path | None, key: str = "") -> tuple[JSON, ...]:
    store = ObservationStore(resolve_home(home))
    if key and (Path(key).name != key or key in (".", "..")):
        raise ValueError("invalid repository key")
    paths = [store.root / "config.json"]
    if key:
        paths.append(store.root / "repositories" / key / "config.json")
    return tuple(store.read(path) or {} for path in paths)


def load_cleanup_policy(home: str | Path | None = None, key: str = "") -> CleanupPolicy:
    values = dict(repository=100, pr=100, comment=5000, branch=200, validation=1000)
    for document in policy_documents(home, key):
        section = document.get("cleanup", {})
        if not isinstance(section, dict) or section.keys() - values.keys():
            raise ValueError("invalid cleanup policy")
        for name, count in section.items():
            if not isinstance(count, dict) or count.keys() - {"max_count"}:
                raise ValueError("resource policy must be an object with max_count")
            if "max_count" in count:
                value = count["max_count"]
                if type(value) is not int or value < 0:
                    raise ValueError("max_count must be a nonnegative integer")
                values[name] = value
    return CleanupPolicy(**values)
