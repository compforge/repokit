"""Public RepoKit failure facts and JSON/text representations."""

from datetime import UTC, datetime


class RepoKitError(Exception):
    """Structured failure; cause stays in-process through Python exception chaining.

    spec: JSON and text share Go's error fields and omit the original cause.
    link: component://docs/errors.md
    """

    def __init__(
        self,
        code: str,
        message: str,
        *,
        op: str = "",
        subject: str = "",
        hint: str = "",
        uncertain: bool = False,
        http_status: int = 0,
        retry_at: str | float | None = None,
        cause: BaseException | None = None,
    ) -> None:
        super().__init__(message)
        self.code = code
        self.message = message
        self.op = op
        self.subject = subject
        self.hint = hint
        self.uncertain = uncertain
        self.http_status = http_status
        self.retry_at = retry_at
        if cause is not None:
            self.__cause__ = cause

    def to_dict(self) -> dict[str, object]:
        result: dict[str, object] = {"code": self.code, "message": self.message}
        for key, value in (
            ("op", self.op),
            ("subject", self.subject),
            ("hint", self.hint),
            ("uncertain", self.uncertain),
            ("httpStatus", self.http_status),
            ("retryAt", self._retry_at_text()),
        ):
            if value:
                result[key] = value
        return result

    def text(self) -> str:
        text = f"[{self.code}]"
        if self.op:
            text += f" {self.op}"
        if self.subject:
            text += f" ({self.subject})"
        text += f": {self.message}"
        if self.uncertain:
            text += "; outcome unknown"
        if self.http_status:
            text += f"; HTTP {self.http_status}"
        if self.retry_at is not None and self.retry_at != "":
            text += f"; retry after {self._retry_at_text()}"
        if self.hint:
            text += f"\nHint: {self.hint}"
        return text

    def _retry_at_text(self) -> str | None:
        if isinstance(self.retry_at, (int, float)):
            return datetime.fromtimestamp(self.retry_at, UTC).isoformat().replace("+00:00", "Z")
        return self.retry_at
