"""Explicit CLI adapter for repository queries and operations exposed by repocli.

Import explicitly to query an installed repocli. Native SDK APIs never invoke this
module or fall back to it. No credentials, caches or retries belong here.
"""

from __future__ import annotations

import json
import logging
import math
import subprocess
from typing import Any

from ._process import run_command
from .budget import current_budget
from .cli_results import branch_result as branch_result
from .cli_results import commit_plan_result as commit_plan_result
from .cli_results import commit_result as commit_result
from .cli_results import push_result as push_result
from .cli_results import rebase_result as rebase_result
from .cli_results import worktree_result as worktree_result
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
