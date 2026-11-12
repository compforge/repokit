"""The shared user home for RepoKit libraries and CLI."""

import os
from pathlib import Path


def resolve_home(home: str | Path | None = None) -> Path:
    """Resolve a location without creating or migrating any files.

    @spec Explicit home wins over REPOKIT_HOME, then ~/.repokit.
    Omission never disables persistence.
    @link component://docs/state.md
    """
    value = (
        home
        if home is not None and str(home) != ""
        else os.environ.get("REPOKIT_HOME") or Path.home() / ".repokit"
    )
    if not str(value).strip():
        raise ValueError("RepoKit home must be nonempty")
    return Path(os.path.abspath(value))
