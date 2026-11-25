"""CLI results own supported PR queries; unavailable results never become absence."""

import json
import subprocess
from unittest.mock import patch

import pytest

from repokit import RepoKitError
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


@pytest.mark.parametrize("head", ["unborn", "a" * 40])
def test_create_commit_confirms_candidate_and_keeps_recovery_identity(tmp_path, head):
    repo, tree, index, sha = str(tmp_path), "b" * 40, "c" * 64, "d" * 40
    data = dict(
        checkout=repo,
        action="create",
        branch="topic",
        head=head,
        tree=tree,
        indexFingerprint=index,
        confirmed=True,
        commit=sha,
        actualTree=tree,
        parents=[] if head == "unborn" else [head],
        indexInstalled=True,
        diagnostics=[],
    )

    def create():
        return repocli.create_commit(
            repo,
            "topic",
            head=head,
            tree=tree,
            index_fingerprint=index,
            mode="files",
            files=["a file"],
            message="message",
        )

    with patch.object(repocli, "run", return_value=dict(outcome="succeeded", data=data)) as call:
        assert create() is data
        call.assert_called_once_with(
            repo,
            ("git", "commit", "create"),
            [
                "--mode",
                "files",
                "--branch",
                "topic",
                "--head",
                head,
                "--tree",
                tree,
                "--index-fingerprint",
                index,
                "--message",
                "message",
                "--file",
                "a file",
            ],
            timeout=180,
        )
    for changed in [
        dict(branch="other"),
        dict(confirmed=False),
        dict(actualTree=sha),
        dict(parents=[sha]),
        dict(commit="bad"),
    ]:
        with patch.object(
            repocli, "run", return_value=dict(outcome="succeeded", data=data | changed)
        ) as call:
            with pytest.raises(RepoKitError) as error:
                create()
            assert "git commit check" in error.value.hint and tree in error.value.hint
            assert call.call_count == 1
    with patch.object(repocli, "run", side_effect=TimeoutError("no response")) as call:
        with pytest.raises(RepoKitError) as error:
            create()
        assert error.value.uncertain and head in error.value.hint
        assert call.call_count == 1
    with patch.object(
        repocli,
        "run",
        return_value=dict(
            outcome="succeeded", data=data | dict(indexInstalled=False, recoveryPath="/recovery")
        ),
    ):
        with pytest.raises(RepoKitError) as error:
            create()
        assert error.value.code == "git.commit_index_uninstalled"
        assert sha in error.value.message and "/recovery" in error.value.hint


def test_push_commit_confirms_destination_or_returns_read_only_check(tmp_path):
    repo, sha = str(tmp_path), "a" * 40
    data = dict(
        checkout=repo,
        branch="topic",
        target="release",
        remote="fork",
        targetCommit=sha,
        confirmed=True,
        remoteCommit=sha,
        diagnostics=[{"message": "upstream setup failed"}],
    )

    def push():
        return repocli.push_commit(
            repo, "topic", sha, remote="fork", target="release", set_upstream=True
        )

    with patch.object(repocli, "run", return_value=dict(outcome="succeeded", data=data)) as call:
        assert push() is data
        call.assert_called_once_with(
            repo,
            ("git", "push"),
            [
                "--branch",
                "topic",
                "--commit",
                sha,
                "--remote",
                "fork",
                "--target",
                "release",
                "--set-upstream",
            ],
            timeout=300,
        )
    for outcome, update in [
        ("unknown", {}),
        ("succeeded", {"remoteCommit": "b" * 40}),
        ("succeeded", {"confirmed": False}),
        ("succeeded", {"target": "other"}),
    ]:
        with patch.object(
            repocli, "run", return_value=dict(outcome=outcome, data=data | update)
        ) as call:
            with pytest.raises(RepoKitError) as error:
                push()
            assert error.value.uncertain
            assert f"--commit {sha} --remote fork --target release --check" in error.value.hint
            assert call.call_count == 1
