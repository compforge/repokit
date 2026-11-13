"""Shared public error representations and Python cause chaining."""

import json
from pathlib import Path

import pytest

from repokit import RepoKitError

CASES = json.loads((Path(__file__).parents[3] / "conformance/errors.json").read_text())


@pytest.mark.parametrize("row", CASES)
def test_shared_error(row: dict) -> None:
    wire = row["wire"]
    cause = OSError("private-cause-detail")
    error = RepoKitError(
        wire["code"],
        wire["message"],
        op=wire.get("op", ""),
        subject=wire.get("subject", ""),
        hint=wire.get("hint", ""),
        uncertain=wire.get("uncertain", False),
        http_status=wire.get("httpStatus", 0),
        retry_at=wire.get("retryAt"),
        cause=cause,
    )
    assert json.loads(json.dumps(error.to_dict())) == wire
    assert error.text() == row["text"]
    assert str(error) == wire["message"]
    assert error.__cause__ is cause
    assert "private-cause-detail" not in error.text()
    with pytest.raises(RepoKitError) as raised:
        raise error
    assert raised.value.__cause__ is cause


def test_domain_error_hierarchy() -> None:
    from repokit.config import ConfigError
    from repokit.forge.model import (
        ForgeAuthError,
        ForgeError,
        ForgeNotFound,
        ForgeOutcomeUnknown,
        ForgeRateLimited,
    )
    from repokit.forge.observation.model import PullRequestStatusUnavailable
    from repokit.state.repository import _IncompleteObservation
    from repokit.state.store import ObservationStorageError

    errors = [
        ConfigError("invalid"),
        ForgeError("failed"),
        ForgeAuthError("auth"),
        ForgeNotFound("absent"),
        ForgeOutcomeUnknown("unknown"),
        ForgeRateLimited("core", 1767225600),
        ObservationStorageError("storage"),
        PullRequestStatusUnavailable("unavailable"),
        _IncompleteObservation("partial"),
    ]
    for error in errors:
        assert isinstance(error, RepoKitError), type(error)
        assert error.to_dict()["message"] == error.message
    assert isinstance(errors[0], ValueError)
    assert isinstance(errors[6], OSError)
    assert isinstance(errors[8], OSError)
    assert isinstance(errors[2], ForgeError)
    assert errors[4].uncertain is True
    limited = ForgeRateLimited("core", 1767225600, secondary=True)
    assert limited.retry_at == 1767225600
    assert limited.secondary is True
    assert limited.to_dict()["retryAt"] == "2026-01-01T00:00:00Z"
    assert str(limited) == "forge rate limited: resource=core, retry_at=1767225600"


def test_implicit_exception_context_is_not_suppressed() -> None:
    from repokit.config import ConfigError

    with pytest.raises(ConfigError) as raised:
        try:
            raise OSError("original failure")
        except OSError:
            raise ConfigError("invalid")
    assert isinstance(raised.value.__context__, OSError)
    assert raised.value.__suppress_context__ is False


def test_epoch_zero_is_preserved_in_json_and_text() -> None:
    error = RepoKitError("forge.rate_limited", "wait", retry_at=0)
    assert error.to_dict()["retryAt"] == "1970-01-01T00:00:00Z"
    assert "1970-01-01T00:00:00Z" in error.text()
