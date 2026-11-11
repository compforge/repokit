"""RepoKit user configuration; callers explicitly load it once per operation."""

from __future__ import annotations

import json
import logging
import os
import stat
from dataclasses import dataclass, field
from pathlib import Path
from typing import Literal
from urllib.parse import urlsplit

from .forge.connection import ForgeConnection
from .forge.model import ForgeAuthError
from .state.home import resolve_home

_log = logging.getLogger(__name__)


class ConfigError(ValueError):
    """Invalid RepoKit configuration; messages never contain configuration values."""


def _host(value: str) -> str:
    host = {"github": "github.com", "gitlab": "gitlab.com"}.get(value.lower(), value.lower())
    try:
        parsed = urlsplit("https://" + host)
        valid = bool(host and parsed.hostname and parsed.netloc == host)
    except ValueError:
        valid = False
    if not valid or host.strip() != host or any(c in host for c in "/@?#\\"):
        raise ConfigError("Invalid Forge host")
    return host


@dataclass(frozen=True)
class _Entry:
    provider: str = ""
    api_host: str = ""
    token: str = field(default="", repr=False)


@dataclass(frozen=True)
class Config:
    """Read-only settings, separate from repository state and workflow policy."""

    home: Path
    _forges: dict[str, _Entry] = field(default_factory=dict, repr=False)

    def forge(self, name: str) -> ForgeConnection:
        """Resolve one Forge without network I/O.

        @spec Credentials come only from the selected host in config.json. Environment
        variables and caller configuration never supply or override credentials.
        @link component://docs/configuration.md
        """
        host = _host(name)
        entry = self._forges.get(host, _Entry())
        provider = entry.provider or {"github.com": "github", "gitlab.com": "gitlab"}.get(host)
        if provider not in ("github", "gitlab"):
            raise ConfigError(
                f"Configure a provider for Forge {host} in {self.home / 'config.json'}"
            )
        kind: Literal["github", "gitlab"] = "github" if provider == "github" else "gitlab"
        token = entry.token.strip()
        if not token:
            raise ForgeAuthError(
                f"no {provider} credential for {host}; configure {self.home / 'config.json'}"
            )
        _log.debug("Forge access resolved: host=%s provider=%s", host, provider)
        return ForgeConnection(
            host=host, provider=kind, token=token, api_host=entry.api_host or None, home=self.home
        )


def load_config(home: str | Path | None = None) -> Config:
    """Load config.json without creating files or reading caller-owned configuration.

    @spec Missing config is empty; invalid or non-private config fails without exposing contents.
    @link component://docs/configuration.md
    """
    root = resolve_home(home)
    path = root / "config.json"
    try:
        stream = path.open()
    except FileNotFoundError:
        return Config(root)
    with stream:
        mode = os.fstat(stream.fileno()).st_mode
        if not stat.S_ISREG(mode) or stat.S_IMODE(mode) & 0o077:
            raise ConfigError(f"RepoKit config must be a private regular file (chmod 600): {path}")
        try:
            document = json.load(stream)
        except (ValueError, UnicodeError):
            raise ConfigError(f"Invalid RepoKit JSON configuration: {path}") from None
    if not isinstance(document, dict) or document.keys() - {"forges"}:
        raise ConfigError(f"Expected a JSON object with a forges map: {path}")
    entries = document.get("forges")
    if entries is None:
        entries = {}
    if not isinstance(entries, dict):
        raise ConfigError(f"Expected a forges map: {path}")
    forges = {}
    for name, value in entries.items():
        host = _host(name)
        if host in forges:
            raise ConfigError(f"Duplicate normalized Forge host: {path}")
        if value is None:
            value = {}
        if not isinstance(value, dict) or value.keys() - {
            "provider",
            "apiHost",
            "token",
        }:
            raise ConfigError(f"Invalid Forge configuration fields: {path}")
        if any(v is not None and not isinstance(v, str) for v in value.values()):
            raise ConfigError(f"Forge configuration fields must be strings: {path}")
        provider = value.get("provider") or ""
        if provider not in ("", "github", "gitlab"):
            raise ConfigError(f"Provider must be github or gitlab: {path}")
        api_host = value.get("apiHost") or ""
        forges[host] = _Entry(
            provider, _host(api_host) if api_host else "", value.get("token") or ""
        )
    _log.debug("RepoKit configuration loaded: path=%s forges=%d", path, len(forges))
    return Config(root, forges)
