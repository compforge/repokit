"""Real Make processes prove the public validation contract, including cancellation."""

import json
import os
import subprocess
import time
from pathlib import Path
from unittest.mock import patch

import pytest

from repokit import execute_validation, plan_validation, validate


def fixture(tmp_path: Path, makefile: str) -> Path:
    root = tmp_path.resolve()
    subprocess.run(["git", "init", "-q", str(root)], check=True)
    (root / "pyproject.toml").write_text("[project]\nname='fixture'\nversion='0.1.0'\n")
    (root / "Makefile").write_text(makefile)
    (root / ".gitignore").write_text("*.out\n")
    return root


CASES = json.loads((Path(__file__).parents[3] / "conformance/validation/cases.json").read_text())


@pytest.mark.parametrize("case", CASES, ids=lambda case: case["name"])
def test_make_conformance(tmp_path: Path, case: dict) -> None:
    root = fixture(tmp_path, case["makefile"])
    for file in case["files"]:
        (root / file).write_text("# fixture\n")
    plan = plan_validation(root, checks=(case["check"],), files=case["selected"])
    spec = plan.components[0].checks[0]
    assert spec.scope == case["scope"]
    assert spec.command == tuple(case["command"])


def test_frozen_environment_and_scope(tmp_path: Path, monkeypatch: pytest.MonkeyPatch) -> None:
    root = fixture(
        tmp_path,
        "test:\n\t@printf '%s|%s|%s' '$(TEST_FILES)' '$(LINT_FILES)' "
        '"$$VALIDATION_TOKEN" > result.out\n',
    )
    (root / "GNUmakefile").write_text("test:\n\t@echo wrong > result.out\n")
    monkeypatch.setenv("MAKEFLAGS", "-n")
    monkeypatch.setenv("TEST_FILES", "unexpected.py")
    monkeypatch.setenv("VALIDATION_TOKEN", "original")
    plan = plan_validation(root, checks=("test",))
    monkeypatch.setenv("VALIDATION_TOKEN", "changed")
    report = execute_validation(plan)
    assert report.complete and report.passed
    assert (root / "result.out").read_text() == "||original"
    assert "original" not in repr(plan)


@pytest.mark.parametrize("phase", ["before", "during"])
def test_round_consistency(tmp_path: Path, phase: str) -> None:
    target = "test:\n\t@echo ran > result.out\n"
    if phase == "during":
        target += "\t@echo changed >> source.py\n"
    root = fixture(tmp_path, target)
    (root / "source.py").write_text("# source\n")
    plan = plan_validation(root, checks=("test",))
    if phase == "before":
        (root / "source.py").write_text("# changed\n")
    report = execute_validation(plan)
    assert report.complete == (phase == "during")
    assert not report.inputs_consistent and not report.passed and report.diagnostics
    if phase == "before":
        assert not (root / "result.out").exists()


def test_failure_and_missing_target(tmp_path: Path) -> None:
    root = fixture(tmp_path, "lint:\n\t@echo failure >&2\n\t@exit 7\n")
    report = validate(root)
    assert not report.passed and report.complete and report.inputs_consistent
    lint, test = report.components[0].checks
    assert lint.status == "failed" and "failure" in lint.stderr
    assert test.status == "unavailable"


def test_timeout_kills_descendants(tmp_path: Path) -> None:
    root = fixture(
        tmp_path,
        "test:\n\t@(while [ ! -e release.out ]; do sleep 0.05; done; "
        "echo escaped > escaped.out) & wait\n",
    )
    report = execute_validation(plan_validation(root, checks=("test",)), timeout=0.5)
    assert not report.passed and not report.complete
    assert report.components[0].checks[0].status == "uncertain"
    # Release the child only after cancellation returned: no wall-clock race
    # between a delayed timeout observer and a child finishing its fixed sleep.
    (root / "release.out").write_text("release")
    time.sleep(0.3)
    assert not (root / "escaped.out").exists()


def test_round_capture_count_independent_of_check_count(tmp_path: Path) -> None:
    from repokit.validation import engine as validation

    root = fixture(tmp_path, "lint:\n\t@true\ntest:\n\t@true\n")
    plan = plan_validation(root)
    with patch.object(validation, "snapshot", wraps=validation.snapshot) as capture:
        report = execute_validation(plan)
    assert report.passed
    assert capture.call_count == 2


def test_explicit_selection_and_unknown_component(tmp_path: Path) -> None:
    root = fixture(tmp_path, "test:\n\t@true\n")
    assert not plan_validation(root, components=()).components
    with pytest.raises(ValueError, match="unknown Component"):
        plan_validation(root, components=("missing",))


def test_plan_uses_supplied_environment(tmp_path: Path) -> None:
    root = fixture(tmp_path, 'test:\n\t@echo "$$KEY"\n')
    plan = plan_validation(
        root, checks=("test",), environment={"PATH": os.environ["PATH"], "KEY": "value"}
    )
    report = execute_validation(plan)
    assert report.passed
    assert report.components[0].checks[0].stdout == "value\n"


def test_modified_plan_rejected(tmp_path: Path) -> None:
    from dataclasses import replace

    root = fixture(tmp_path, "test:\n\t@echo ran > ran.out\n")
    plan = plan_validation(root, checks=("test",))
    group = plan.components[0]
    spec = replace(group.checks[0], scope="focused", files=("invented.py",))
    modified = replace(plan, components=(replace(group, checks=(spec,)),))
    with pytest.raises(ValueError, match="modified"):
        execute_validation(modified)
    assert not (root / "ran.out").exists()


def test_impact_selection_and_staleness(tmp_path: Path) -> None:
    from repokit import snapshot

    root = fixture(tmp_path, "lint:\n\t@echo $(LINT_FILES)\ntest:\n\t@true\n")
    (root / "a.py").write_text("# source\n")
    impact = {
        "snapshot": snapshot(root).digest,
        "input": "working_tree",
        "checkout": str(root),
        "components": [
            {
                "root": ".",
                "snapshot": "after",
                "affectedFileCounts": {"source": 1},
                "affectedFiles": ["a.py"],
                "testFiles": [],
            }
        ],
    }
    plan = plan_validation(root, impact=impact)
    assert [check.scope for check in plan.components[0].checks] == ["focused", "full"]
    skipped = plan_validation(root, impact=impact, tags=())
    assert all(check.scope == "skipped" for check in skipped.components[0].checks)
    (root / "a.py").write_text("# changed\n")
    with pytest.raises(ValueError, match="current checkout"):
        plan_validation(root, impact=impact)


def test_nested_ownership(tmp_path: Path) -> None:
    root = fixture(tmp_path, "test:\n\t@echo $(TEST_FILES)\n")
    child = root / "child"
    child.mkdir()
    (child / "pyproject.toml").write_text("[project]\nname='child'\nversion='0.1.0'\n")
    (child / "Makefile").write_text("test:\n\t@echo $(TEST_FILES)\n")
    (child / "a.py").write_text("# source\n")
    with pytest.raises(ValueError, match="outside selected Components"):
        plan_validation(root, components=(".",), checks=("test",), files=("child/a.py",))


def test_output_budget_and_prestart_cancellation(tmp_path: Path) -> None:
    from threading import Event

    from repokit import operation

    root = fixture(tmp_path, "test:\n\t@yes error | head -c 70000 >&2\n")
    plan = plan_validation(root, checks=("test",))
    report = execute_validation(plan)
    assert not report.complete and report.components[0].checks[0].status == "uncertain"
    cancel = Event()
    with operation(cancel=cancel):
        cancel.set()
        with pytest.raises(InterruptedError):
            execute_validation(plan)


def test_inherited_git_context_does_not_redirect_make(tmp_path, monkeypatch):
    other = tmp_path / "other"
    other.mkdir()
    root = fixture(tmp_path / "target", "test:\n\t@git rev-parse --show-toplevel > actual.out\n")
    subprocess.run(["git", "init", "-q", str(other)], check=True)
    monkeypatch.setenv("GIT_DIR", str(other / ".git"))
    monkeypatch.setenv("GIT_WORK_TREE", str(other))
    report = validate(root, checks=("test",))
    assert report.passed
    assert (root / "actual.out").read_text().strip() == str(root)


EXECUTION_CASES = json.loads(
    (Path(__file__).parents[3] / "conformance/validation/execution.json").read_text()
)


@pytest.mark.parametrize("case", EXECUTION_CASES, ids=lambda case: case["name"])
def test_shared_execution_contract(tmp_path, case):
    """+case=Native Go/Python execute the same selection with equivalent terminal evidence."""
    root = fixture(tmp_path, case["makefile"])
    for name, text in case["files"].items():
        path = root / name
        path.parent.mkdir(parents=True, exist_ok=True)
        path.write_text(text)
    if case.get("withoutRoot"):
        (root / "Makefile").unlink()
        (root / "pyproject.toml").unlink()
    if "error" in case:
        with pytest.raises(ValueError, match=case["error"]):
            validate(
                root,
                components=case.get("components"),
                checks=case["checks"],
                files=case.get("selected", ()),
            )
        assert not (root / "runs.out").exists()
        return
    report = validate(
        root,
        components=case.get("components"),
        checks=case["checks"],
        files=case.get("selected", ()),
    )
    actual = {
        "complete": report.complete,
        "passed": report.passed,
        "inputsConsistent": report.inputs_consistent,
        "checks": [
            {
                "component": group.component.root,
                "check": item.spec.check,
                "scope": item.spec.scope,
                "status": item.status,
                "requestedComponents": list(item.spec.requested_components),
                "files": list(item.spec.files),
            }
            for group in report.components
            for item in group.checks
        ],
    }
    assert actual == case["expected"]
    if "reason" in case:
        assert report.components[0].checks[0].spec.reason == case["reason"]
    output = root / "runs.out"
    if output.exists():
        assert len(output.read_text().splitlines()) == 1


@pytest.mark.parametrize("explicit", [False, True])
def test_environment_identity_contract(tmp_path, monkeypatch, explicit):
    """+case=Inherited tool-call identity is removed; explicit identity is retained."""
    root = fixture(tmp_path, "test:\n\t@printf '%s' \"$${CODEX_TOOL_CALL_ID-unset}\"\n")
    monkeypatch.setenv("CODEX_TOOL_CALL_ID", "per-invocation")
    environment = dict(os.environ) if explicit else None
    plan = plan_validation(root, checks=("test",), environment=environment)
    assert not set(dict(plan._environment)) & {"_", "SHLVL", "PWD", "OLDPWD"}
    report = execute_validation(plan)
    assert report.passed and report.inputs_consistent
    assert report.components[0].checks[0].stdout == ("per-invocation" if explicit else "unset")
