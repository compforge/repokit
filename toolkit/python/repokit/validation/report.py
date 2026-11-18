"""Decode the Go validation engine's wire report; does not execute a second engine."""

from __future__ import annotations

import json
from collections.abc import Iterable
from pathlib import Path
from typing import Any, cast


def decode_validation_report(
    payload: str, *, checkout: str | Path, checks: Iterable[str] = ("lint", "test")
) -> dict[str, Any]:
    """Validate one report against its checkout and requested checks.

    @spec Failed checks remain valid reports. Malformed or incomplete protocol
    evidence raises ValueError; consumers decide admission and report storage.
    @link component://docs/validation.md
    """
    try:
        return _decode(payload, str(checkout), checks)
    except (KeyError, TypeError, AttributeError) as exc:
        raise ValueError("malformed ValidationReport structure") from exc


def _decode(payload: str, repo: str, checks: Iterable[str]) -> dict[str, Any]:
    data = json.loads(payload)
    if not isinstance(data, dict) or data.get("schemaVersion") != 2:
        raise ValueError("unsupported ValidationReport; upgrade repocli")
    if data.get("checkout") != str(Path(repo).resolve()):
        raise ValueError("ValidationReport checkout mismatch")
    if any(type(data.get(key)) is not bool for key in ("inputsConsistent", "complete", "passed")):
        raise ValueError("invalid ValidationReport outcome")
    if not isinstance(data.get("snapshot"), str) or not data["snapshot"].startswith("sha256:"):
        raise ValueError("ValidationReport snapshot missing")
    if not isinstance(data.get("components"), list) or not isinstance(
        data.get("diagnostics"), list
    ):
        raise ValueError("ValidationReport results missing")
    requested = set(checks)
    selected = requested - {"format"}
    if not selected <= {"lint", "test", "build"}:
        raise ValueError("invalid requested validation checks")
    seen: set[tuple[str, str]] = set()
    coverage: dict[str, set[str]] = {}
    statuses: list[str] = []
    for group in data["components"]:
        if not isinstance(group["checks"], list) or not group["checks"]:
            raise ValueError("ValidationReport omitted requested checks in Component")
        root = group["component"]["root"]
        if not isinstance(root, str) or not (Path(repo) / root).resolve().is_relative_to(
            Path(repo).resolve()
        ):
            raise ValueError("ValidationReport Component escapes checkout")
        for check in group["checks"]:
            name = check["check"]
            if name not in selected or (root, name) in seen:
                raise ValueError("invalid or duplicate ValidationReport check")
            seen.add((root, name))
            sources = check.get("requestedComponents")
            if (
                not isinstance(sources, list)
                or not sources
                or any(not isinstance(source, str) for source in sources)
                or len(set(sources)) != len(sources)
            ):
                raise ValueError("ValidationReport requestedComponents missing or duplicated")
            for source in sources:
                if not isinstance(source, str) or not (
                    Path(repo) / source
                ).resolve().is_relative_to(Path(repo).resolve()):
                    raise ValueError("ValidationReport requested Component escapes checkout")
                if name in coverage.setdefault(source, set()):
                    raise ValueError("duplicate requested Component check")
                coverage[source].add(name)
            if check["scope"] not in {"full", "focused", "skipped", "unavailable"} or check[
                "status"
            ] not in {"passed", "failed", "skipped", "unavailable", "cancelled", "uncertain"}:
                raise ValueError("invalid validation check status or scope")
            statuses.append(check["status"])
            if type(check["reused"]) is not bool or not isinstance(check["witnesses"], dict):
                raise ValueError("invalid validation evidence")
            if any(
                not isinstance(check.get(key), str)
                for key in (
                    "checkedAt",
                    "runID",
                    "requestedAt",
                    "environment",
                    "reason",
                    "reuseReason",
                    "stdout",
                    "stderr",
                )
            ):
                raise ValueError("incomplete validation check result")
            if any(
                not isinstance(check.get(key), list)
                or any(not isinstance(x, str) for x in check[key])
                for key in ("command", "files", "inputFiles")
            ):
                raise ValueError("invalid validation command or file list")
    if any(checks != selected for checks in coverage.values()):
        raise ValueError("ValidationReport omitted requested checks")
    preparation = data.get("preparation", [])
    if not isinstance(preparation, list):
        raise ValueError("invalid ValidationReport preparation")
    prepared: list[str] = []
    for group in preparation:
        if (
            "format" not in requested
            or not isinstance(group["checks"], list)
            or not group["checks"]
        ):
            raise ValueError("invalid ValidationReport preparation checks")
        for check in group["checks"]:
            if check["check"] != "format" or check["status"] not in {
                "passed",
                "failed",
                "skipped",
                "unavailable",
                "cancelled",
                "uncertain",
            }:
                raise ValueError("invalid ValidationReport preparation status")
            prepared.append(check["status"])
    if data["components"] and any(
        status in {"failed", "unavailable", "cancelled", "uncertain"} for status in prepared
    ):
        raise ValueError("ValidationReport checks follow unsuccessful preparation")
    # ExecuteValidation can reject a stale plan before producing any check results.
    if (
        not data["components"]
        and not data["complete"]
        and not data["passed"]
        and not data["inputsConsistent"]
    ):
        return cast(dict[str, Any], data)
    # Format can stop before lint/test; its resulting snapshot is not validation evidence.
    preparation_only = (
        not data["components"]
        and "format" in requested
        and (
            not selected
            or not data["complete"]
            or any(status in {"failed", "unavailable"} for status in prepared)
        )
    )
    outcomes = prepared if preparation_only else statuses
    complete = all(status in {"passed", "failed", "skipped", "unavailable"} for status in outcomes)
    passed = all(status in {"passed", "skipped"} for status in outcomes)
    if not preparation_only:
        passed = passed and data["inputsConsistent"]
        if data["complete"] != complete or data["passed"] != passed:
            raise ValueError("ValidationReport outcome contradicts check results")
    elif (data["complete"] and not complete) or (
        data["passed"] and (not passed or not data["complete"])
    ):
        raise ValueError("ValidationReport outcome contradicts preparation results")
    return cast(dict[str, Any], data)
