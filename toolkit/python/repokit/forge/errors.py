"""Structured errors shared by Forge domain interfaces and transports."""

from ..errors import RepoKitError


class ForgeError(RepoKitError):
    """Any forge transport/API failure."""

    _error_code = "forge.request_failed"

    def __init__(self, message: str = "") -> None:
        super().__init__(
            self._error_code, message, uncertain=self._error_code == "forge.outcome_unknown"
        )


class ForgeOutcomeUnknown(ForgeError):
    """A request may have taken effect; inspect remote state before retrying."""

    _error_code = "forge.outcome_unknown"


class ForgeAuthError(ForgeError):
    """No usable token or insufficient permission (excluding rate limits)."""

    _error_code = "forge.authentication_required"


class ForgeRateLimited(ForgeError):
    """Requests must wait until retry_at (UTC epoch seconds); never an auth failure."""

    retry_at: float
    _error_code = "forge.rate_limited"

    def __init__(self, resource: str, retry_at: float, *, secondary: bool = False):
        super().__init__(f"forge rate limited: resource={resource}, retry_at={retry_at:.0f}")
        self.resource = resource
        self.retry_at = retry_at
        self.secondary = secondary

    def __str__(self) -> str:
        return f"forge rate limited: resource={self.resource}, retry_at={self.retry_at:.0f}"


class ForgeNotFound(ForgeError):
    """404 from the API."""

    _error_code = "forge.not_found"


class ForgeUnsupported(ForgeError):
    """The provider cannot express the requested operation without changing its meaning."""

    _error_code = "forge.unsupported_operation"
