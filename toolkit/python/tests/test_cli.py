"""CLI results own supported PR queries; unavailable results never become absence."""

import json
import subprocess
from unittest.mock import patch

from repokit import cli as repocli
from repokit.forge.model import ForgeError


def result(data, **overrides):
    return subprocess.CompletedProcess(
        [],
        0,
        json.dumps(
            {
                "schemaVersion": "1.0",
                "command": "repocli pr list",
                "outcome": "succeeded",
                "data": data,
                **overrides,
            }
        ),
        "",
    )


def pull_request():
    return {
        "key": {"repository": {"forge": {"name": "github"}, "path": "upstream/repo"}, "number": 7},
        "title": "change",
        "state": "open",
        "headSha": "abc",
        "sourceBranch": "topic",
        "targetBranch": "main",
        "sourceRepository": {"forge": {"name": "github"}, "path": "fork/repo"},
        "url": "https://example.invalid/pr/7",
    }


def test_cli_pr_queries_preserve_destination_and_identity():
    with patch.object(repocli, "run_command", return_value=result([pull_request()])) as run:
        pr = repocli.list_pull_requests(
            "/repo", branch="topic", state="open", target_repository="upstream/repo"
        )[0]
        assert (pr.number, pr.target_repository, pr.source_repository) == (
            7,
            "upstream/repo",
            "fork/repo",
        )
        args = run.call_args.args[0]
        assert args[:3] == ["repocli", "pr", "list"]
        assert args[args.index("--target-repository") + 1] == "upstream/repo"
        assert "env" not in run.call_args.kwargs and "shell" not in run.call_args.kwargs
        assert run.call_args.kwargs["process_group"] is True
    with patch.object(
        repocli, "run_command", return_value=result({"body": "details"}, command="repocli pr show")
    ):
        assert (
            repocli.pull_request_description("/repo", 7, target_repository="upstream/repo")
            == "details"
        )


def test_cli_failures_are_not_empty_pr_lists():
    malformed = pull_request()
    malformed["title"] = None
    invalid_number = pull_request()
    invalid_number["key"]["number"] = True
    outputs = [
        result(None),
        result([], outcome="unknown"),
        result([], outcome="partial"),
        result([], schemaVersion="99"),
        result([], command="different"),
        result([{}]),
        result([malformed]),
        result([invalid_number]),
        subprocess.CompletedProcess([], 1, result([]).stdout, "failed"),
        subprocess.CompletedProcess([], 0, "not JSON", ""),
    ]
    for output in outputs:
        with patch.object(repocli, "run_command", return_value=output):
            try:
                repocli.list_pull_requests("/repo", branch="topic", state="open")
            except ForgeError:
                pass
            else:
                raise AssertionError("Unavailable PR results became verified absence")
    with patch.object(repocli, "run_command", return_value=result([])):
        assert repocli.list_pull_requests("/repo", branch="topic", state="open") == []


def test_missing_or_timed_out_cli_does_not_fall_back_to_credentials_or_sdk():
    for failure in (FileNotFoundError("repocli"), subprocess.TimeoutExpired("repocli", 12)):
        with patch.object(repocli, "run_command", side_effect=failure):
            try:
                repocli.list_pull_requests("/repo", branch="topic", state="open")
            except ForgeError as error:
                assert "unavailable" in str(error)
            else:
                raise AssertionError("Failed CLI query became successful absence")


def test_nonzero_exit_preserves_structured_remote_error():
    output = json.dumps(
        {
            "schemaVersion": "1.0",
            "command": "repocli pr list",
            "outcome": "failed",
            "error": {"message": "access unavailable"},
        }
    ).encode()
    failure = subprocess.CalledProcessError(1, ["repocli"], output, b"")
    with patch.object(repocli, "run_command", side_effect=failure):
        import pytest

        with pytest.raises(ForgeError, match="access unavailable"):
            repocli.list_pull_requests("/repo", branch="topic", state="open")


def test_cancelled_operation_never_launches_cli():
    """+case=Parent cancellation rejects CLI queries before any process is created."""
    from threading import Event

    import pytest

    from repokit import operation

    cancel = Event()
    with (
        operation(timeout=10, cancel=cancel),
        patch(
            "repokit._process.subprocess.Popen", side_effect=AssertionError("CLI process launched")
        ) as launch,
    ):
        cancel.set()
        with pytest.raises(ForgeError) as failure:
            repocli.list_pull_requests("/repo", branch="topic", state="open")
        assert isinstance(failure.value.__cause__, InterruptedError)
        launch.assert_not_called()


def test_operation_partial_effects_survive_nonzero_exit_without_retry():
    envelope = {
        "schemaVersion": "1.0",
        "command": "repocli worktree ensure",
        "outcome": "partial",
        "data": {"confirmed": True, "ready": False},
        "error": {"message": "installer timed out"},
    }
    with patch.object(
        repocli,
        "run_command",
        side_effect=subprocess.CalledProcessError(1, "repocli", json.dumps(envelope), ""),
    ) as invoke:
        assert repocli.run("/repo", ("worktree", "ensure"), ["/checkout", "--prepare"]) == envelope
        assert invoke.call_count == 1


def test_invalid_operation_timeout_is_rejected_before_process_start():
    import pytest

    for timeout in (-1, 0, float("nan"), float("inf")):
        with patch.object(repocli, "run_command") as invoke, pytest.raises(ValueError):
            repocli.run("/repo", ("git", "push"), [], timeout=timeout)
        invoke.assert_not_called()


def test_prepared_worktree_decoder_retains_partial_bins_and_rejects_contradictions():
    from pathlib import Path

    import pytest

    data = {
        "checkout": "/checkout",
        "branch": "topic",
        "headSha": "a" * 40,
        "action": "created",
        "confirmed": True,
        "diagnostics": [],
        "ready": False,
        "preparation": {
            "complete": False,
            "bins": [
                {
                    "path": "node_modules",
                    "status": "unknown",
                    "stderr": "registry unavailable",
                    "error": {"message": "timed out"},
                }
            ],
        },
    }
    envelope = {"outcome": "partial", "data": data}
    assert repocli.worktree_result(envelope, Path("/checkout"), "topic", prepared=True) is data
    data["ready"] = True
    with pytest.raises(ValueError, match="readiness"):
        repocli.worktree_result(envelope, Path("/checkout"), "topic", prepared=True)
    data["ready"] = False
    envelope["outcome"] = "succeeded"
    with pytest.raises(ValueError):
        repocli.worktree_result(envelope, Path("/checkout"), "topic", prepared=True)


def test_push_decoder_matches_source_and_destination_independently(tmp_path):
    """+case=Push decoding accepts explicit destinations and rejects mismatched evidence."""
    import pytest

    repo = str(tmp_path)
    commit = "a" * 40
    data = {
        "checkout": repo,
        "branch": "topic",
        "target": "topic",
        "remote": "origin",
        "targetCommit": commit,
        "confirmed": True,
        "diagnostics": [],
    }
    envelope = {"outcome": "succeeded", "data": data}
    assert repocli.push_result(envelope, repo, "topic", commit, "origin") is data
    assert repocli.push_result(envelope, repo, "topic", commit, "origin", target="topic") is data
    data["target"] = "release"
    assert repocli.push_result(envelope, repo, "topic", commit, "origin", target="release") is data
    with pytest.raises(ValueError, match="mismatching push report"):
        repocli.push_result(envelope, repo, "topic", commit, "origin")
    with pytest.raises(ValueError, match="mismatching push report"):
        repocli.push_result(envelope, repo, "topic", commit, "origin", target="other")
    data["branch"] = "other"
    with pytest.raises(ValueError, match="mismatching push report"):
        repocli.push_result(envelope, repo, "topic", commit, "origin", target="release")
