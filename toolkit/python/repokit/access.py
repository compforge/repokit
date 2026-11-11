"""Compose checkout identity and user configuration into native Forge access."""

from __future__ import annotations

from dataclasses import replace
from pathlib import Path

from .config import load_config
from .forge.connection import ForgeConnection
from .forge.model import Forge, ForgeError, PullRequestIdentity
from .forge.observation import PullRequestContext
from .git.remote import remote


def _connection(
    checkout: str | Path, home: str | Path | None, timeout: int
) -> tuple[str, ForgeConnection] | None:
    origin = remote(checkout)
    if origin is None:
        return None
    try:
        return origin.path, replace(load_config(home).forge(origin.host), timeout=timeout)
    except (ValueError, OSError) as error:
        raise ForgeError(str(error)) from error


def forge_client(
    checkout: str | Path,
    *,
    home: str | Path | None = None,
    timeout: int = 10,
    target_repository: str | None = None,
) -> Forge | None:
    """Resolve origin access; an explicit target selects another repository on that Forge.

    +spec=Only an absent Forge origin is inapplicable; missing access raises an error.
    +link=component://docs/configuration.md
    """
    access = _connection(checkout, home, timeout)
    if access is None:
        return None
    source, connection = access
    return connection.client(target_repository if target_repository is not None else source)


def pull_request_context(
    checkout: str | Path,
    *,
    home: str | Path | None = None,
    timeout: int = 10,
    targets: tuple[str, ...] | None = None,
    reader: Forge | None = None,
) -> PullRequestContext | None:
    """Build a native context; callers select targets and own the observer lifecycle."""
    access = _connection(checkout, home, timeout)
    if access is None:
        return None
    source, connection = access
    return connection.pull_request_context(checkout, source=source, targets=targets, reader=reader)


def pull_request_identity(
    checkout: str | Path, number: int, *, target_repository: str | None = None
) -> PullRequestIdentity | None:
    """Resolve PR identity without loading credentials or querying the network."""
    if type(number) is not int or number <= 0:
        raise ValueError("PR number must be a positive integer")
    origin = remote(checkout)
    if origin is None:
        return None
    return PullRequestIdentity(
        source=origin.host,
        repository=target_repository if target_repository is not None else origin.path,
        number=number,
    )
