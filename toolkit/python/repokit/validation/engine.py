"""Plan and execute Component checks under an explicit Makefile contract.

Preparation/formatting happens before planning. No workflow state is persisted;
callers own reuse storage, branch references and commit/merge policy.
"""

from __future__ import annotations

import hashlib
import json
import logging
import os
import re
import subprocess
import time
from collections.abc import Mapping, Sequence
from dataclasses import dataclass, field, replace
from pathlib import Path

from .. import _process
from .._git_environment import checkout_environment
from .._inspect import inspect
from ..budget import current_budget
from ..model import ComponentBinding, Layout, owner
from ..snapshot import snapshot

_LOG = logging.getLogger(__name__)
_FILES = re.compile(r"[A-Za-z0-9_./@+][A-Za-z0-9_./@+:-]*")
_TAGS = ("source", "test", "config", "manifest", "lockfile")


@dataclass(frozen=True, slots=True)
class CheckSpec:
    check: str
    scope: str
    command: tuple[str, ...]
    files: tuple[str, ...]
    reason: str
    environment: str


@dataclass(frozen=True, slots=True)
class ComponentValidationPlan:
    component: ComponentBinding
    checks: tuple[CheckSpec, ...]


@dataclass(frozen=True, slots=True)
class ValidationPlan:
    checkout: str
    snapshot: str
    components: tuple[ComponentValidationPlan, ...]
    _environment: tuple[tuple[str, str], ...] = field(repr=False)
    _seal: str = field(default="", repr=False)


@dataclass(frozen=True, slots=True)
class CheckResult:
    spec: CheckSpec
    status: str
    exit_code: int | None = None
    duration_seconds: float = 0
    stdout: str = ""
    stderr: str = ""
    diagnostic: str = ""


@dataclass(frozen=True, slots=True)
class ComponentValidationResult:
    component: ComponentBinding
    checks: tuple[CheckResult, ...]


@dataclass(frozen=True, slots=True)
class ValidationReport:
    checkout: str
    snapshot: str
    components: tuple[ComponentValidationResult, ...]
    complete: bool
    passed: bool
    diagnostics: tuple[str, ...] = ()


def _environment(values: Mapping[str, str] | None) -> tuple[tuple[str, str], ...]:
    source = checkout_environment() if values is None else values
    excluded = {
        "MAKEFILES",
        "MAKEFLAGS",
        "MFLAGS",
        "MAKEOVERRIDES",
        "GNUMAKEFLAGS",
        "MAKELEVEL",
        "LINT_FILES",
        "TEST_FILES",
    }
    pairs = []
    for key, value in source.items():
        if not key or "=" in key or "\0" in key or "\0" in value:
            raise ValueError("invalid execution environment")
        if key not in excluded:
            pairs.append((key, value))
    return tuple(sorted(pairs))


def _environment_digest(values: tuple[tuple[str, str], ...]) -> str:
    # Match Go's sorted KEY=VALUE representation; never include values in reports/logs.
    text = json.dumps([f"{k}={v}" for k, v in values], ensure_ascii=False, separators=(",", ":"))
    text = text.replace("&", "\\u0026").replace("<", "\\u003c").replace(">", "\\u003e")
    text = text.replace("\u2028", "\\u2028").replace("\u2029", "\\u2029")
    return hashlib.sha256(text.encode()).hexdigest()


def _target(text: str, check: str) -> str:
    targets = set()
    deferred = []
    for line in text.replace("\\\n", " ").splitlines():
        if line.startswith("\t"):
            continue
        declaration = line.split("#", 1)[0].strip()
        word = declaration.split(maxsplit=1)[0] if declaration else ""
        if word in {"ifeq", "ifneq", "ifdef", "ifndef", "define"}:
            deferred.append("endef" if word == "define" else "endif")
            continue
        if deferred:
            if word == deferred[-1]:
                deferred.pop()
            continue
        head, separator, rest = declaration.partition(":")
        if separator and "=" not in head and "$" not in head and not rest.startswith("="):
            targets.update(head.split())
    candidates = ("lint-ci", "lint") if check == "lint" else ("test", "test-ci", "test-local")
    return next((name for name in candidates if name in targets), "")


def _files(
    root: Path, component: str, layout: Layout, candidates: Sequence[str]
) -> tuple[str, ...]:
    files = set()
    for path in candidates:
        relative = Path(path)
        if relative.is_absolute() or relative.as_posix() != path or ".." in relative.parts:
            raise ValueError("selected path is not normalized and repository-relative")
        binding = owner(layout, path)
        if binding is None or binding.root != component:
            raise ValueError("selected path no longer belongs to Component")
        absolute = root / path
        if not absolute.is_file() or absolute.is_symlink():
            raise ValueError("selected path is missing or not a regular file")
        if absolute.resolve() != absolute:
            raise ValueError("selected path crosses a symlink")
        file = absolute.relative_to(root / component).as_posix()
        if _FILES.fullmatch(file) is None:
            raise ValueError("selected path cannot be represented by Make file-list contract")
        files.add(file)
    return tuple(sorted(files))


def _strings(value: object) -> tuple[str, ...]:
    if not isinstance(value, list) or any(not isinstance(item, str) for item in value):
        raise ValueError("impact file lists must contain strings")
    return tuple(value)


def _impact_component(impact: Mapping[str, object], root: str) -> Mapping[str, object]:
    groups = impact.get("components")
    if not isinstance(groups, list):
        raise ValueError("impact components must be a list")
    for group in groups:
        if (
            isinstance(group, dict)
            and group.get("root") == root
            and group.get("snapshot") == "after"
        ):
            return group
    raise ValueError(f"impact missing current Component {root!r}")


def plan_validation(
    repository: str | Path,
    *,
    components: Sequence[str] | None = None,
    checks: Sequence[str] = ("lint", "test"),
    files: Sequence[str] = (),
    impact: Mapping[str, object] | None = None,
    tags: Sequence[str] = _TAGS,
    environment: Mapping[str, str] | None = None,
    timeout: float = 60,
) -> ValidationPlan:
    """Read a frozen execution plan; never run Make or implicitly obtain impact.

    +spec=`Makefile targets and file-list variables are the only check execution contract`
    +spec=`Deleted and before-side paths fall back to full checks in their current Component`
    +link=component://docs/validation.md
    """
    budget = current_budget(timeout)
    observed = snapshot(repository, timeout=budget.remaining())
    if not observed.complete:
        raise ValueError("validation snapshot is incomplete")
    root = Path(observed.checkout)
    layout = inspect(root, timeout=budget.remaining())
    if not layout.complete:
        raise ValueError("validation layout is incomplete")
    names = sorted(set(checks))
    if any(name not in {"lint", "test"} for name in names):
        raise ValueError("unsupported check")
    if files and impact is not None:
        raise ValueError("files and impact are alternative selections")
    if impact is not None and (
        impact.get("snapshot") != observed.digest
        or impact.get("input") != "working_tree"
        or impact.get("checkout") != observed.checkout
    ):
        raise ValueError("impact does not describe current checkout contents")
    values = _environment(environment)
    environment_digest = _environment_digest(values)
    selected = None if components is None else set(components)
    known = {component.root for component in layout.components}
    if selected is not None and selected - known:
        raise ValueError(f"unknown Components: {sorted(selected - known)}")
    groups = []
    for component in layout.components:
        if selected is not None and component.root not in selected:
            continue
        directory = root / component.root
        if directory.resolve() != directory:
            raise ValueError(f"Component {component.root!r} crosses a symlink")
        if (directory / "Makefile").is_symlink():
            raise ValueError("Component Makefile must be a regular file")
        try:
            text = (directory / "Makefile").read_text()
        except FileNotFoundError:
            text = ""
        contract_text = "\n".join(
            line if line.startswith("\t") else line.split("#", 1)[0] for line in text.splitlines()
        )
        specs = []
        for check in names:
            candidates = tuple(files)
            scope, reason = "full", "full Component check"
            if impact is not None:
                group = _impact_component(impact, component.root)
                counts = group.get("affectedFileCounts")
                if not isinstance(counts, dict) or any(
                    not isinstance(tag, str) or type(count) is not int or count < 0
                    for tag, count in counts.items()
                ):
                    raise ValueError("invalid affectedFileCounts")
                if not any(counts.get(tag, 0) > 0 for tag in tags):
                    specs.append(
                        CheckSpec(
                            check,
                            "skipped",
                            (),
                            (),
                            "no selected validation tags",
                            environment_digest,
                        )
                    )
                    continue
                candidates = _strings(
                    group.get("affectedFiles" if check == "lint" else "testFiles")
                )
                reason = "affected Component; canonical full check"
            target = _target(text, check)
            if not target:
                specs.append(
                    CheckSpec(
                        check,
                        "unavailable",
                        (),
                        (),
                        f"Makefile does not declare a supported {check} target",
                        environment_digest,
                    )
                )
                continue
            variable = check.upper() + "_FILES"
            chosen: tuple[str, ...] = ()
            if candidates:
                try:
                    chosen = _files(root, component.root, layout, candidates)
                    if (
                        f"$({variable})" not in contract_text
                        and "${" + variable + "}" not in contract_text
                    ):
                        raise ValueError(f"{variable} contract missing")
                except ValueError as error:
                    chosen = ()
                    reason = f"{error}; canonical full check"
                else:
                    scope, reason = "focused", f"selected files under Makefile {variable} contract"
            args = ["make", "-f", "Makefile", target, "LINT_FILES=", "TEST_FILES="]
            if scope == "focused":
                args[4 if check == "lint" else 5] = variable + "=" + " ".join(chosen)
            specs.append(CheckSpec(check, scope, tuple(args), chosen, reason, environment_digest))
        groups.append(ComponentValidationPlan(component, tuple(specs)))
    plan = ValidationPlan(str(root), observed.digest, tuple(groups), values)
    return replace(plan, _seal=_plan_seal(plan))


def _execute_check(
    directory: Path, spec: CheckSpec, plan: ValidationPlan, budget: _process.Budget
) -> CheckResult:
    if spec.scope in {"skipped", "unavailable"}:
        return CheckResult(spec, spec.scope)
    try:
        budget.remaining()
    except (TimeoutError, InterruptedError) as error:
        return CheckResult(spec, "cancelled", diagnostic=str(error))
    if not dict(plan._environment).get("PATH"):
        return CheckResult(
            spec, "unavailable", diagnostic="make is not executable in the check environment PATH"
        )
    started = time.monotonic()
    _LOG.info(
        "validation check started component=%s check=%s scope=%s", directory, spec.check, spec.scope
    )
    try:
        process = _process.run_command(
            list(spec.command),
            budget,
            cwd=directory,
            env=dict(plan._environment),
            process_group=True,
        )
        result = CheckResult(
            spec,
            "passed",
            process.returncode,
            stdout=os.fsdecode(process.stdout),
            stderr=os.fsdecode(process.stderr),
        )
    except subprocess.CalledProcessError as error:
        result = CheckResult(
            spec,
            "failed",
            error.returncode,
            stdout=os.fsdecode(error.output or b""),
            stderr=os.fsdecode(error.stderr or b""),
        )
    except (TimeoutError, InterruptedError, ValueError) as error:
        result = CheckResult(spec, "uncertain", diagnostic=str(error))
    except OSError as error:
        result = CheckResult(spec, "unavailable", diagnostic=str(error))

    result = replace(result, duration_seconds=time.monotonic() - started)
    _LOG.info(
        "validation check finished component=%s check=%s status=%s",
        directory,
        spec.check,
        result.status,
    )
    return result


def _plan_seal(plan: ValidationPlan) -> str:
    fields = (
        plan.checkout,
        plan.snapshot,
        tuple(
            (
                group.component.root,
                tuple(
                    (
                        spec.check,
                        spec.scope,
                        spec.command,
                        spec.files,
                        spec.reason,
                        spec.environment,
                    )
                    for spec in group.checks
                ),
            )
            for group in plan.components
        ),
    )
    return hashlib.sha256(json.dumps(fields, ensure_ascii=False).encode()).hexdigest()


def execute_validation(plan: ValidationPlan, *, timeout: float = 600) -> ValidationReport:
    """Execute only the frozen plan; two round-level captures bind all check results.

    A command can pass while the report is incomplete because source changed.
    Only a complete report may be used as reusable validation evidence.
    """
    budget = current_budget(timeout)
    if not plan._seal or plan._seal != _plan_seal(plan):
        raise ValueError("validation plan was modified; plan again")
    before = snapshot(plan.checkout, timeout=budget.remaining())
    if not before.complete or before.digest != plan.snapshot:
        return ValidationReport(
            plan.checkout,
            plan.snapshot,
            (),
            False,
            False,
            ("contents changed after planning; no checks executed",),
        )
    if any(
        spec.environment != _environment_digest(plan._environment)
        for group in plan.components
        for spec in group.checks
    ):
        raise ValueError("validation environment does not match plan")
    groups = tuple(
        ComponentValidationResult(
            group.component,
            tuple(
                _execute_check(Path(plan.checkout) / group.component.root, spec, plan, budget)
                for spec in group.checks
            ),
        )
        for group in plan.components
    )
    complete = all(
        result.status in {"passed", "failed", "skipped"}
        for group in groups
        for result in group.checks
    )
    passed = all(
        result.status in {"passed", "skipped"} for group in groups for result in group.checks
    )
    diagnostics: tuple[str, ...] = ()
    try:
        after = snapshot(plan.checkout, timeout=budget.remaining())
        if not after.complete or after.digest != plan.snapshot:
            diagnostics = ("contents changed during validation",)
    except (OSError, ValueError, subprocess.SubprocessError) as error:
        diagnostics = (f"final content capture failed: {error}",)
    return ValidationReport(
        plan.checkout,
        plan.snapshot,
        groups,
        complete and not diagnostics,
        passed and not diagnostics,
        diagnostics,
    )


def validate(
    repository: str | Path,
    *,
    components: Sequence[str] | None = None,
    checks: Sequence[str] = ("lint", "test"),
    files: Sequence[str] = (),
    impact: Mapping[str, object] | None = None,
    tags: Sequence[str] = _TAGS,
    environment: Mapping[str, str] | None = None,
    timeout: float = 600,
) -> ValidationReport:
    from ..budget import operation

    with operation(timeout=timeout):
        plan = plan_validation(
            repository,
            components=components,
            checks=checks,
            files=files,
            impact=impact,
            tags=tags,
            environment=environment,
            timeout=timeout,
        )
        return execute_validation(plan, timeout=timeout)


def lint(
    repository: str | Path,
    *,
    components: Sequence[str] | None = None,
    files: Sequence[str] = (),
    impact: Mapping[str, object] | None = None,
    tags: Sequence[str] = _TAGS,
    environment: Mapping[str, str] | None = None,
    timeout: float = 600,
) -> ValidationReport:
    return validate(
        repository,
        components=components,
        checks=("lint",),
        files=files,
        impact=impact,
        tags=tags,
        environment=environment,
        timeout=timeout,
    )


def test(
    repository: str | Path,
    *,
    components: Sequence[str] | None = None,
    files: Sequence[str] = (),
    impact: Mapping[str, object] | None = None,
    tags: Sequence[str] = _TAGS,
    environment: Mapping[str, str] | None = None,
    timeout: float = 600,
) -> ValidationReport:
    return validate(
        repository,
        components=components,
        checks=("test",),
        files=files,
        impact=impact,
        tags=tags,
        environment=environment,
        timeout=timeout,
    )
