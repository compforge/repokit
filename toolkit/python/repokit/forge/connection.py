"""Explicit Forge access and observation assembly, without application configuration."""

from __future__ import annotations

import hashlib
import json
from dataclasses import dataclass, field
from pathlib import Path
from typing import Literal

from harness_common import Forge as ForgeIdentity
from harness_common import Repository

from ..home import resolve_home
from .github import GitHubForge
from .gitlab import GitLabForge
from .model import Forge, ForgeAuthError
from .observation import PullRequestContext, PullRequestObserver


def detect_provider(host: str, explicit_type: str | None = None) -> Literal["github", "gitlab"]:
    if explicit_type in ("github", "gitlab"):
        return "github" if explicit_type == "github" else "gitlab"
    host = host.lower()
    return "github" if host == "github.com" or host.startswith("github.") else "gitlab"


@dataclass(frozen=True)
class ForgeConnection:
    """Access configuration; never reads a caller's config or persists credentials.

    @spec Provider, Forge identity, endpoint and credential changes isolate caches.
    Query targets are explicit and never inferred from fork provenance.
    @link component://docs/pr.md
    """

    host: str
    token: str = field(repr=False)
    provider: Literal["github", "gitlab"] | None = None
    api_host: str | None = None
    home: Path = field(default_factory=resolve_home)
    timeout: int = 10

    def __post_init__(self) -> None:
        object.__setattr__(self, "host", self.host.lower())
        if not self.host or self.timeout <= 0:
            raise ValueError("Forge host and positive timeout are required")
        if not self.token.strip():
            raise ForgeAuthError(f"PR query unavailable: no credential for {self.host}")
        object.__setattr__(self, "provider", detect_provider(self.host, self.provider))
        object.__setattr__(self, "api_host", (self.api_host or self.host).lower())
        object.__setattr__(self, "home", resolve_home(self.home))

    @property
    def name(self) -> str:
        return {"github.com": "github", "gitlab.com": "gitlab"}.get(self.host, self.host)

    @property
    def identity(self) -> str:
        value = [self.provider, self.name, self.api_host, self.token]
        return hashlib.sha256(
            json.dumps(value, ensure_ascii=False, separators=(",", ":")).encode()
        ).hexdigest()

    def repository(self, path: str) -> Repository:
        if not path or path.startswith("/") or path.endswith("/"):
            raise ValueError("nonempty repository path is required")
        return Repository(ForgeIdentity(self.name), path)

    def client(self, path: str) -> Forge:
        self.repository(path)
        if self.provider == "github":
            owner, separator, name = path.partition("/")
            if not separator or not name or "/" in name:
                raise ValueError("GitHub repository must be owner/name")
            return GitHubForge(
                self.api_host or self.host,
                owner,
                name,
                self.token,
                forge=self.name,
                timeout=self.timeout,
                home=self.home,
            )
        return GitLabForge(
            self.api_host or self.host,
            path,
            self.token,
            forge=self.name,
            timeout=self.timeout,
            home=self.home,
        )

    def pull_request_context(
        self,
        checkout: str | Path,
        *,
        source: str,
        targets: tuple[str, ...] | None = None,
        reader: Forge | None = None,
    ) -> PullRequestContext:
        observer = PullRequestObserver(
            reader if reader is not None else self.client(source),
            forge=self.name,
            home=self.home,
            connection=self.identity,
            timeout=self.timeout,
        )
        return PullRequestContext(
            observer,
            checkout,
            source=self.repository(source),
            targets=tuple(
                self.repository(path) for path in (targets if targets is not None else (source,))
            ),
        )
