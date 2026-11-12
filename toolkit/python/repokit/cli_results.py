"""Decoders for RepoCLI operation evidence. No process execution or workflow policy.

A matching report preserves confirmed partial effects; it never authorizes replay.
Callers decide session ownership, sensitive paths, continuation and validation gates.
"""

import re
from pathlib import Path
from typing import Any

Data = dict[str, Any]


def oid(value: object) -> bool:
    return isinstance(value, str) and re.fullmatch(r"[0-9a-f]{40}|[0-9a-f]{64}", value) is not None


def branch_result(envelope: Data, repo: str, name: str) -> Data:
    data = envelope.get("data")
    if (
        not isinstance(data, dict)
        or data.get("branch") != name
        or not isinstance(data.get("checkout"), str)
        or Path(data["checkout"]).resolve() != Path(repo).resolve()
        or not all(
            isinstance(data.get(k), bool)
            for k in ("branchConfirmed", "changesRestored", "confirmed", "writeAttempted")
        )
        or not isinstance(data.get("currentBranch"), str)
        or not isinstance(data.get("phase"), str)
        or not isinstance(data.get("stashOid", ""), str)
        or not isinstance(data.get("diagnostics"), list)
        or not all(
            isinstance(d, dict) and isinstance(d.get("message"), str) for d in data["diagnostics"]
        )
        or not isinstance(envelope.get("error") or {}, dict)
    ):
        raise ValueError("invalid branch operation report")
    switched = data["branchConfirmed"]
    if switched and data["currentBranch"] != name:
        raise ValueError("confirmed branch contradicts checkout identity")
    if envelope["outcome"] == "succeeded" and (
        not data["confirmed"]
        or not switched
        or not data["changesRestored"]
        or data.get("action") not in {"created", "reused"}
    ):
        raise ValueError(
            "successful command lacks confirmed branch creation and change restoration"
        )
    return data


def rebase_result(
    envelope: Data, repo: str, action: str, *, branch: str | None = None, target: str | None = None
) -> Data:
    data = envelope["data"]
    phases = {
        "none",
        "unmanaged",
        "prepared",
        "paused",
        "ready",
        "publishing",
        "published",
        "aborted",
        "failed",
        "unknown",
    }
    if (
        not isinstance(data, dict)
        or data.get("confirmed") is not True
        or data.get("action") != action
        or data.get("phase") not in phases
        or not isinstance(data.get("checkout"), str)
        or not data["checkout"]
        or Path(data["checkout"]).resolve() != Path(repo).resolve()
        or not isinstance(data.get("conflicts"), list)
        or not all(isinstance(p, str) for p in data["conflicts"])
        or not isinstance(data.get("diagnostics"), list)
    ):
        raise ValueError("invalid or unconfirmed rebase report")
    phase = data["phase"]
    transaction = data.get("transaction")
    if phase not in {"none", "unmanaged"}:
        if (
            not isinstance(transaction, dict)
            or not all(
                isinstance(transaction.get(k), str) and transaction[k]
                for k in ("branch", "target", "expectedRemote", "targetCommit")
            )
            or (branch is not None and transaction["branch"] != branch)
            or (target is not None and transaction["target"] != target)
        ):
            raise ValueError("mismatching rebase transaction")
    accepted = {
        "start": {"paused", "ready"},
        "continue": {"paused", "ready"},
        "finish": {"published"},
        "abort": {"aborted"},
        "status": phases,
    }
    if phase not in accepted[action]:
        raise ValueError("successful command contradicts rebase phase")
    return data


def worktree_result(envelope: Data, target: Path, branch: str, *, prepared: bool = False) -> Data:
    data = envelope["data"]
    if (
        not isinstance(data, dict)
        or data.get("confirmed") is not True
        or data.get("branch") != branch
        or not isinstance(data.get("checkout"), str)
        or not data["checkout"]
        or Path(data["checkout"]).resolve() != target.resolve()
        or not isinstance(data.get("headSha"), str)
        or not data["headSha"]
        or data.get("action") not in {"created", "resumed", "reused"}
    ):
        raise ValueError("unconfirmed or mismatching checkout result")
    diagnostics = data.get("diagnostics")
    if not isinstance(diagnostics, list) or any(
        not isinstance(d, dict) or not isinstance(d.get("message"), str) for d in diagnostics
    ):
        raise ValueError("invalid checkout diagnostics")
    if prepared:
        preparation = data.get("preparation")
        if (
            not isinstance(preparation, dict)
            or type(data.get("ready")) is not bool
            or type(preparation.get("complete")) is not bool
            or not isinstance(preparation.get("bins"), list)
        ):
            raise ValueError("invalid dependency preparation result")
        for bin in preparation["bins"]:
            if (
                not isinstance(bin, dict)
                or not isinstance(bin.get("path"), str)
                or not isinstance(bin.get("status"), str)
                or not isinstance(bin.get("stderr", ""), str)
                or not isinstance(bin.get("error") or {}, dict)
            ):
                raise ValueError("invalid dependency Bin result")
        if data["ready"] and (
            not preparation["complete"]
            or any(bin["status"] != "ready" for bin in preparation["bins"])
        ):
            raise ValueError("contradictory dependency readiness")
        if envelope["outcome"] == "succeeded" and not data["ready"]:
            raise ValueError("successful preparation is not ready")
    return data


def commit_plan_result(result: Data, repo: str, branch: str, mode: str) -> Data:
    data = result.get("data")
    if (
        not isinstance(data, dict)
        or data.get("action") != "plan"
        or not isinstance(data.get("checkout"), str)
        or Path(data["checkout"]).resolve() != Path(repo).resolve()
        or data.get("branch") != branch
        or data.get("mode") != mode
        or data.get("attempted") is not False
        or data.get("confirmed") is not False
        or not oid(data.get("tree"))
        or not (oid(data.get("head")) or data.get("head") == "unborn")
        or not isinstance(data.get("indexFingerprint"), str)
        or not re.fullmatch(r"[0-9a-f]{64}|missing", data["indexFingerprint"])
        or not isinstance(data.get("paths"), list)
        or not all(isinstance(p, str) for p in data["paths"])
        or not isinstance(data.get("changes"), list)
        or not all(
            isinstance(c, dict)
            and isinstance(c.get("path"), str)
            and isinstance(c.get("newMode"), str)
            and re.fullmatch(r"[0-7]{6}", c["newMode"])
            for c in data["changes"]
        )
        or data.get("empty") is not (len(data["changes"]) == 0)
    ):
        raise ValueError("invalid or mismatching commit plan")
    return data


def commit_result(
    result: Data, repo: str, branch: str, head: str, tree: str, index_fingerprint: str
) -> Data:
    data = result.get("data")
    if (
        not isinstance(data, dict)
        or data.get("action") != "create"
        or not isinstance(data.get("checkout"), str)
        or Path(data["checkout"]).resolve() != Path(repo).resolve()
        or data.get("branch") != branch
        or data.get("head") != head
        or data.get("tree") != tree
        or data.get("indexFingerprint") != index_fingerprint
        or not isinstance(data.get("diagnostics"), list)
        or not all(
            isinstance(d, dict) and isinstance(d.get("message"), str) for d in data["diagnostics"]
        )
    ):
        raise ValueError("invalid or mismatching commit report")
    return data


def push_result(result: Data, repo: str, branch: str, commit: str, remote: str) -> Data:
    data = result.get("data")
    if (
        not isinstance(data, dict)
        or data.get("branch") != branch
        or data.get("target") != branch
        or data.get("remote") != remote
        or data.get("targetCommit") != commit
        or not isinstance(data.get("checkout"), str)
        or Path(data["checkout"]).resolve() != Path(repo).resolve()
        or not isinstance(data.get("confirmed"), bool)
        or not isinstance(data.get("diagnostics"), list)
        or not all(
            isinstance(d, dict) and isinstance(d.get("message"), str) for d in data["diagnostics"]
        )
    ):
        raise ValueError("invalid or mismatching push report")
    return data
