"""Explicit CLI adapter for repository queries and operations exposed by repocli.

Import explicitly to query an installed repocli. Native SDK APIs never invoke this
module or fall back to it. No credentials, caches or retries belong here.
"""

from __future__ import annotations

import json
import logging
import math
import shlex
import subprocess
from collections.abc import Sequence
from typing import Any

from ._process import run_command
from .budget import current_budget
from .cli_results import branch_result as branch_result
from .cli_results import commit_plan_result as commit_plan_result
from .cli_results import commit_result as commit_result
from .cli_results import oid as _oid
from .cli_results import push_result as push_result
from .cli_results import rebase_result as rebase_result
from .cli_results import worktree_result as worktree_result
from .errors import RepoKitError
from .forge.model import ForgeError, PullRequest

logger = logging.getLogger(__name__)


def run(
    repo: str, operation: tuple[str, ...], arguments: list[str], *, timeout: float = 120
) -> dict[str, Any]:
    """Explicit bounded CLI invocation; failed and partial domain results remain available.

    Native SDK methods never fall back to this process adapter. No retry is performed.
    """
    if not math.isfinite(timeout) or timeout <= 0 or not operation:
        raise ValueError("operation and positive finite timeout are required")
    command = [
        "repocli",
        *operation,
        *arguments,
        "--repo",
        repo,
        "--timeout",
        f"{timeout}s",
        "--json",
    ]
    try:
        result = run_command(command, current_budget(timeout + 10), process_group=True)
    except subprocess.CalledProcessError as error:
        result = subprocess.CompletedProcess(command, error.returncode, error.output, error.stderr)
    envelope = json.loads(result.stdout)
    if (
        not isinstance(envelope, dict)
        or envelope.get("schemaVersion") != "1.0"
        or envelope.get("command") != "repocli " + " ".join(operation)
        or envelope.get("outcome") not in {"succeeded", "failed", "partial", "unknown"}
        or (result.returncode == 0) != (envelope["outcome"] == "succeeded")
    ):
        raise ValueError("unsupported or contradictory repocli result")
    if envelope.get("error") is not None and not isinstance(envelope["error"], dict):
        raise ValueError("invalid repocli error")
    logger.info(
        "RepoCLI operation=%s repo=%s outcome=%s cost=%s",
        operation,
        repo,
        envelope["outcome"],
        envelope.get("cost"),
    )
    return envelope


def create_commit(
    repo: str,
    branch: str,
    *,
    head: str,
    tree: str,
    index_fingerprint: str,
    mode: str,
    files: Sequence[str],
    message: str,
    timeout: float = 180,
) -> dict[str, Any]:
    """Create the planned tree once. Return only after both commit and index are confirmed."""
    identity = ["--branch", branch, "--head", head, "--tree", tree]
    check = shlex.join(["repocli", "git", "commit", "check", "--repo", repo, *identity])
    args = [
        "--mode",
        mode,
        *identity,
        "--index-fingerprint",
        index_fingerprint,
        "--message",
        message,
    ]
    for path in files:
        args.extend(["--file", path])
    try:
        result = run(repo, ("git", "commit", "create"), args, timeout=timeout)
        data = commit_result(result, repo, branch, head, tree, index_fingerprint)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        raise RepoKitError(
            "git.commit_unverified",
            f"commit result unavailable: {error}",
            op="git.commit.create",
            subject=branch,
            uncertain=True,
            hint=check,
        ) from error

    commit = data.get("commit")
    if (
        data.get("confirmed") is not True
        or not _oid(commit)
        or data.get("actualTree") != tree
        or data.get("parents") != ([] if head == "unborn" else [head])
    ):
        detail = (result.get("error") or {}).get(
            "message"
        ) or "expected parent and tree not confirmed"
        raise RepoKitError(
            "git.commit_unverified",
            f"commit not confirmed: {detail}",
            op="git.commit.create",
            subject=branch,
            hint=check + (f"; inspect {data['recoveryPath']}" if data.get("recoveryPath") else ""),
            uncertain=result["outcome"] != "failed"
            or bool((result.get("error") or {}).get("uncertain")),
        )
    if data.get("indexInstalled") is not True:
        raise RepoKitError(
            "git.commit_index_uninstalled",
            f"commit {commit} confirmed, but index installation is unconfirmed",
            op="git.commit.create",
            subject=branch,
            hint=f"Inspect {data.get('recoveryPath') or 'the index'} before another commit.",
        )
    if result["outcome"] != "succeeded":
        raise RepoKitError(
            "git.commit_unverified",
            f"commit {commit} reported with outcome {result['outcome']}",
            op="git.commit.create",
            subject=branch,
            hint=check,
            uncertain=True,
        )
    return data


def push_commit(
    repo: str,
    branch: str,
    commit: str,
    *,
    remote: str = "origin",
    target: str = "",
    set_upstream: bool = False,
    timeout: float = 300,
) -> dict[str, Any]:
    """Publish a frozen commit once. A read-only check can reconcile a missing response."""
    if not _oid(commit):
        raise RepoKitError(
            "git.push_invalid_argument",
            "push requires a full commit ID",
            op="git.push",
            subject=branch,
        )
    args = ["--branch", branch, "--commit", commit, "--remote", remote]
    if target:
        args.extend(["--target", target])
    check = shlex.join(["repocli", "git", "push", "--repo", repo, *args, "--check"])
    if set_upstream:
        args.append("--set-upstream")
    try:
        result = run(repo, ("git", "push"), args, timeout=timeout)
        data = push_result(result, repo, branch, commit, remote, target=target)
    except (OSError, ValueError, KeyError, TypeError, subprocess.SubprocessError) as error:
        raise RepoKitError(
            "git.push_unverified",
            f"push result unavailable: {error}",
            op="git.push",
            subject=branch,
            hint=check,
            uncertain=True,
        ) from error
    if (
        result["outcome"] != "succeeded"
        or not data["confirmed"]
        or data.get("remoteCommit") != commit
    ):
        detail = (result.get("error") or {}).get("message") or "remote commit not confirmed"
        raise RepoKitError(
            "git.push_unverified",
            detail,
            op="git.push",
            subject=branch,
            hint=check,
            uncertain=result["outcome"] != "failed",
        )
    return data


def _query(repo: str, operation: str, arguments: list[str]) -> object:
    try:
        envelope = run(repo, ("pr", operation), arguments, timeout=10)
    except (OSError, subprocess.SubprocessError, ValueError) as error:
        raise ForgeError(f"repocli pr {operation} unavailable: {type(error).__name__}") from error
    if envelope["outcome"] != "succeeded":
        detail = envelope.get("error") or {}
        raise ForgeError(detail.get("message") or envelope.get("summary") or "repocli query failed")
    return envelope.get("data")


def _target(target: str | None) -> list[str]:
    return ["--target-repository", target] if target else []


def list_pull_requests(
    repo: str, *, branch: str, state: str, target_repository: str | None = None
) -> list[PullRequest]:
    data = _query(repo, "list", ["--head", branch, "--state", state, *_target(target_repository)])
    if not isinstance(data, list):
        raise ForgeError("Invalid repocli PR list")
    rows = []
    try:
        for row in data:
            key = row["key"]
            if (
                type(key["number"]) is not int
                or key["number"] <= 0
                or row["state"] not in ("open", "closed", "merged")
            ):
                raise ValueError("invalid PR identity or state")
            fields = [
                row[name] for name in ("title", "sourceBranch", "targetBranch", "url", "headSha")
            ]
            fields.extend((row["sourceRepository"]["path"], key["repository"]["path"]))
            if not all(isinstance(value, str) for value in fields):
                raise ValueError("invalid PR fields")
            rows.append(
                PullRequest(
                    number=key["number"],
                    title=row["title"],
                    state=row["state"],
                    source_branch=row["sourceBranch"],
                    target_branch=row["targetBranch"],
                    web_url=row["url"],
                    sha=row["headSha"],
                    source_repository=row["sourceRepository"]["path"],
                    target_repository=key["repository"]["path"],
                )
            )
    except (KeyError, TypeError, ValueError) as error:
        raise ForgeError("Invalid repocli PR data") from error
    return rows


def pull_request_description(
    repo: str, number: int, *, target_repository: str | None = None
) -> str:
    data = _query(repo, "show", [str(number), *_target(target_repository)])
    if not isinstance(data, dict) or not isinstance(data.get("body"), str):
        raise ForgeError("Invalid repocli PR description")
    body = data["body"]
    assert isinstance(body, str)
    return body
