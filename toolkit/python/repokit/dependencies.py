"""Observe checkout-local dependencies and explicitly materialize locked installations."""

from __future__ import annotations

import hashlib
import json
import os
import stat
import subprocess
import threading
import tomllib
import uuid
from dataclasses import dataclass, replace
from pathlib import Path
from typing import Literal

import yaml

from . import _process
from ._git_environment import checkout_environment
from ._inspect import inspect
from .budget import current_budget, operation
from .model import InspectReport, owner

Status = Literal["ready", "present", "missing", "stale", "unsupported"]
_RECEIPT = ".repokit-dependencies.json"
_NODE_LOCKS = {
    "npm": ("npm-shrinkwrap.json", "package-lock.json"),
    "pnpm": ("pnpm-lock.yaml",),
    "yarn": ("yarn.lock",),
    "bun": ("bun.lock", "bun.lockb"),
}
_LOCKS: dict[Path, threading.Lock] = {}
_LOCKS_GUARD = threading.Lock()


@dataclass(frozen=True, slots=True)
class DependencyEnvironment:
    """An installation belongs to a checkout and may serve multiple workspace Components.

    present means local dependencies exist without a matching preparation receipt;
    it is not proof that a user-managed environment matches the lockfile.
    missing describes an absent directory, independently of whether command provides
    a supported locked installation. unsupported describes an unusable local layout.
    """

    component: Path
    checkout: Path
    root: Path
    manager: str
    directory: Path
    command: tuple[str, ...]
    inputs: tuple[Path, ...]
    fingerprint: str
    status: Status
    reason: str = ""


@dataclass(frozen=True, slots=True)
class DependencyPreparation:
    environment: DependencyEnvironment
    status: Literal["ready", "unsupported", "failed", "uncertain"]
    executed: bool = False
    exit_code: int | None = None
    stdout: str = ""
    stderr: str = ""
    reason: str = ""


def _json(path: Path) -> dict[str, object]:
    value = json.loads(path.read_text())
    if not isinstance(value, dict):
        raise ValueError(f"Expected an object: {path}")
    return value


def _patterns(root: Path, python: bool) -> tuple[str, ...]:
    values: object
    if python:
        manifest = root / "pyproject.toml"
        if not manifest.is_file():
            return ()
        workspace = (
            tomllib.loads(manifest.read_text()).get("tool", {}).get("uv", {}).get("workspace", {})
        )
        values = [*workspace.get("members", []), *("!" + p for p in workspace.get("exclude", []))]
    elif (root / "pnpm-workspace.yaml").is_file():
        value = yaml.safe_load((root / "pnpm-workspace.yaml").read_text())
        values = value.get("packages", []) if isinstance(value, dict) else []
    elif (root / "package.json").is_file():
        value = _json(root / "package.json").get("workspaces", [])
        values = value.get("packages", []) if isinstance(value, dict) else value
    else:
        return ()
    if not isinstance(values, list) or any(not isinstance(p, str) for p in values):
        raise ValueError(f"Invalid workspace members: {root}")
    return tuple(values)


def _members(root: Path, python: bool) -> tuple[Path, ...]:
    included: set[Path] = set()
    excluded: set[Path] = set()
    manifest = "pyproject.toml" if python else "package.json"
    for pattern in _patterns(root, python):
        negative = pattern.startswith("!")
        glob = pattern[1:] if negative else pattern
        if Path(glob).is_absolute() or ".." in Path(glob).parts:
            raise ValueError("Workspace members must remain inside their installation root")
        # Do not silently interpret unsupported shell/YAML glob syntax as a different workspace.
        if any(char in glob for char in "{}()"):
            raise ValueError(f"Unsupported workspace glob: {glob}")
        for path in root.glob(glob):
            if not (path / manifest).is_file():
                continue
            if not path.resolve().is_relative_to(root):
                raise ValueError("Workspace member resolves outside its installation root")
            (excluded if negative else included).add(path.resolve())
            if len(included) + len(excluded) > 4096:
                raise ValueError("Workspace exceeds 4096 dependency manifests")
    return tuple(sorted(included - excluded))


def _root(component: Path, checkout: Path, python: bool) -> Path:
    selected = component
    for parent in component.parents:
        if not parent.is_relative_to(checkout):
            break
        if selected in _members(parent, python):
            selected = parent
    return selected


def _fingerprint(root: Path, inputs: tuple[Path, ...]) -> str:
    digest = hashlib.sha256(str(root).encode())
    for path in inputs:
        digest.update(str(path.relative_to(root)).encode())
        digest.update(b"\0")
        digest.update(path.read_bytes())
        digest.update(b"\0")
    return digest.hexdigest()


def _observe(environment: DependencyEnvironment) -> DependencyEnvironment:
    directory = environment.directory
    # Observe the entry itself: a dangling symlink is an unsupported installation,
    # while absence says nothing about whether a locked installer is available.
    # Permission and other observation errors propagate instead of claiming missing.
    try:
        mode = directory.lstat().st_mode
    except FileNotFoundError:
        return replace(environment, status="missing", reason="Local dependencies are missing")
    if stat.S_ISLNK(mode):
        return replace(
            environment,
            status="unsupported",
            reason="Dependency directory is a symlink; refusing to reuse another installation",
        )
    if not stat.S_ISDIR(mode):
        return replace(
            environment,
            status="unsupported",
            reason="Dependency path is not a directory",
        )
    receipt = directory / _RECEIPT
    if not receipt.exists():
        return replace(
            environment,
            status="present",
            reason="Local dependencies exist without a receipt; consistency is unverified",
        )
    try:
        prepared = _json(receipt)
    except (ValueError, OSError):
        return replace(
            environment, status="stale", reason="Dependency preparation receipt is unreadable"
        )
    if prepared.get("fingerprint") != environment.fingerprint:
        return replace(
            environment,
            status="stale",
            reason="Dependency manifests, lockfile or installation path changed",
        )
    return replace(environment, status="ready", reason="")


def inspect_dependencies(
    path: str | Path, *, inspection: InspectReport | None = None
) -> tuple[DependencyEnvironment, ...]:
    """Read-only dependency observations; never install during inspect or worktree creation.

    An optional complete working-tree inspection avoids repeating repository discovery.
    Installation roots are resolved only within that checkout, never through its parent.
    """
    component = Path(path).resolve(strict=True)
    report = inspection or inspect(component)
    if not report.complete or report.input != "working_tree":
        raise ValueError("Dependency preparation requires complete working-tree inspection")
    checkout = Path(report.checkout).resolve()
    binding = owner(report, component.relative_to(checkout).as_posix())
    if binding is None:
        raise ValueError("No Component owns the requested dependency path")
    component = checkout / binding.root
    environments: list[DependencyEnvironment] = []
    kinds = []
    if (component / "package.json").is_file():
        kinds.append(False)
    if (component / "pyproject.toml").is_file():
        kinds.append(True)
    for python in kinds:
        root = _root(component, checkout, python)
        manifest = root / ("pyproject.toml" if python else "package.json")
        members = _members(root, python)
        inputs = {manifest, *(member / manifest.name for member in members)}
        command: tuple[str, ...] = ()
        if python:
            manager = "uv"
            lock = root / "uv.lock"
            if not lock.is_file():
                # Legacy/unmanaged Python projects do not imply a uv environment.
                continue
            command = (
                ("uv", "sync", "--locked", "--all-packages")
                if members
                else ("uv", "sync", "--locked")
            )
            inputs.add(lock)
            directory = root / ".venv"
        else:
            declared = _json(manifest).get("packageManager", "")
            if not isinstance(declared, str):
                raise ValueError("packageManager must be a string")
            manager = declared.partition("@")[0]
            if not manager:
                candidates = [
                    name
                    for name, locks in _NODE_LOCKS.items()
                    if any((root / lock).is_file() for lock in locks)
                ]
                if len(candidates) > 1:
                    raise ValueError(
                        "Ambiguous package manager; declare packageManager in package.json"
                    )
                manager = candidates[0] if candidates else "node"
            locks = _NODE_LOCKS.get(manager, ())
            node_lock = next((root / name for name in locks if (root / name).is_file()), None)
            if node_lock:
                inputs.add(node_lock)
                if manager == "npm":
                    command = ("npm", "ci", "--prefer-offline")
                elif manager in {"bun", "pnpm"}:
                    command = (manager, "install", "--frozen-lockfile")
                elif manager == "yarn":
                    classic = declared.startswith("yarn@1.") or node_lock.read_text().startswith(
                        "# yarn lockfile v1"
                    )
                    command = ("yarn", "install", "--frozen-lockfile" if classic else "--immutable")
            directory = root / "node_modules"
            if manager == "yarn" and (root / ".pnp.cjs").exists():
                # A PnP installation is not a missing node_modules installation.
                # No receipt directory or repair strategy is claimed for this mode yet.
                raise ValueError("Yarn PnP dependency preparation is not supported")
        for config in ("pnpm-workspace.yaml", ".npmrc", ".yarnrc.yml", "bunfig.toml", "uv.toml"):
            if (root / config).is_file():
                inputs.add(root / config)
        files = tuple(sorted(inputs))
        environment = DependencyEnvironment(
            component,
            checkout,
            root,
            manager,
            directory,
            command,
            files,
            _fingerprint(root, files),
            "missing",
        )
        environments.append(_observe(environment))
    return tuple(environments)


def prepare_dependencies(
    environment: DependencyEnvironment, *, timeout: float = 600
) -> DependencyPreparation:
    """Prepare dependencies while excluding concurrent repository maintenance."""
    from .state.worktrees import worktree_lease

    try:
        with operation(timeout=timeout):
            with worktree_lease(environment.checkout, timeout=timeout):
                from .state.worktrees import _pending_releases

                if environment.directory in _pending_releases(environment.checkout, recover=True):
                    return DependencyPreparation(
                        environment,
                        "failed",
                        reason="environment release pending; run worktree clean",
                    )
                return _prepare_dependencies(environment, timeout=timeout)
    except OSError as exc:
        return DependencyPreparation(environment, "failed", reason=str(exc))


def _prepare_dependencies(
    environment: DependencyEnvironment, *, timeout: float
) -> DependencyPreparation:
    """Explicit locked installation, serialized per installation root within this process.

    Does not install runtimes, upgrade dependencies, edit locks, or own validation policy.
    Failures retain process evidence; interruption after launch is uncertain, never ready.
    """
    budget = current_budget(timeout)
    with _LOCKS_GUARD:
        lock = _LOCKS.setdefault(environment.root, threading.Lock())
    try:
        while not lock.acquire(timeout=min(0.05, budget.remaining())):
            pass
    except (TimeoutError, InterruptedError) as error:
        return DependencyPreparation(environment, "failed", reason=str(error))
    try:
        current = next(
            (
                item
                for item in inspect_dependencies(environment.component)
                if item.manager == environment.manager and item.root == environment.root
            ),
            None,
        )
        if current is None or current.fingerprint != environment.fingerprint:
            return DependencyPreparation(
                environment,
                "failed",
                reason="Dependency inputs changed; inspect again before preparation",
            )
        if current.status == "ready":
            return DependencyPreparation(current, "ready")
        if not current.command or current.status == "unsupported":
            return DependencyPreparation(
                current, "unsupported", reason=current.reason or "No supported locked installation"
            )
        # uv must not inherit a path that redirects this checkout's editable installation.
        env = checkout_environment()
        if current.manager == "uv":
            env.pop("VIRTUAL_ENV", None)
            env.pop("UV_PROJECT_ENVIRONMENT", None)
        try:
            budget.remaining()
        except (TimeoutError, InterruptedError) as error:
            return DependencyPreparation(current, "failed", reason=str(error))
        try:
            result = _process.run_command(
                list(current.command), budget, cwd=current.root, env=env, process_group=True
            )
        except subprocess.CalledProcessError as error:
            return DependencyPreparation(
                current,
                "failed",
                True,
                error.returncode,
                os.fsdecode(error.output or b""),
                os.fsdecode(error.stderr or b""),
                "Locked dependency installation failed",
            )
        except (TimeoutError, InterruptedError, ValueError) as error:
            return DependencyPreparation(current, "uncertain", True, reason=str(error))
        except OSError as error:
            return DependencyPreparation(current, "failed", reason=str(error))
        refreshed = next(
            (
                item
                for item in inspect_dependencies(current.component)
                if item.manager == current.manager and item.root == current.root
            ),
            None,
        )
        if refreshed is None or refreshed.fingerprint != current.fingerprint:
            return DependencyPreparation(
                current,
                "failed",
                True,
                reason="Dependency inputs changed during installation; no ready receipt written",
            )
        if not current.directory.is_dir() or current.directory.is_symlink():
            return DependencyPreparation(
                current,
                "failed",
                True,
                reason="Installer did not materialize checkout-local dependencies",
            )
        receipt = current.directory / _RECEIPT
        receipt.write_text(
            json.dumps(
                {
                    "fingerprint": current.fingerprint,
                    "manager": current.manager,
                    "generation": uuid.uuid4().hex,
                }
            )
            + "\n"
        )
        return DependencyPreparation(
            _observe(current),
            "ready",
            True,
            result.returncode,
            os.fsdecode(result.stdout),
            os.fsdecode(result.stderr),
        )
    finally:
        lock.release()


def dependency_witnesses(path: str | Path) -> dict[str, str] | None:
    """Return receipt-backed input digests, or None when reuse is not justified.

    @spec Receipt names and input fingerprints belong to RepoKit. No environments
    yields an empty mapping; unreadable inputs raise rather than prove readiness.
    @link component://docs/validation.md
    """
    environments = inspect_dependencies(path)
    if any(environment.status != "ready" for environment in environments):
        return None
    witnesses = {}
    for environment in environments:
        for source in (*environment.inputs, environment.directory / _RECEIPT):
            witnesses[str(source.resolve())] = hashlib.sha256(source.read_bytes()).hexdigest()
    if inspect_dependencies(path) != environments:
        return None
    return dict(sorted(witnesses.items()))
