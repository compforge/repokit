"""Static checkout-local Depot declarations; directory presence is a runtime observation."""

import fnmatch
import json
import posixpath
import tomllib
from collections.abc import Mapping

import yaml
from harness_common import Bin, Depot


def _object(value: object) -> Mapping[str, object]:
    if not isinstance(value, dict):
        raise ValueError("expected an object")
    return value


def _strings(value: object) -> list[str]:
    if not isinstance(value, list) or any(not isinstance(p, str) for p in value):
        raise ValueError("expected workspace patterns")
    return value


def _patterns(files: Mapping[str, bytes | None], root: str, python: bool) -> list[str]:
    def data(name: str) -> bytes:
        return files.get(posixpath.normpath(posixpath.join(root, name))) or b""

    def exists(name: str) -> bool:
        return posixpath.normpath(posixpath.join(root, name)) in files

    values: list[str] = []
    if python:
        if exists("pyproject.toml"):
            metadata = tomllib.loads(data("pyproject.toml").decode())
            workspace = _object(
                _object(_object(metadata.get("tool", {})).get("uv", {})).get("workspace", {})
            )
            values = _strings(workspace.get("members", [])) + [
                "!" + p for p in _strings(workspace.get("exclude", []))
            ]
    elif exists("pnpm-workspace.yaml"):
        metadata = yaml.safe_load(data("pnpm-workspace.yaml"))
        values = _strings(_object({} if metadata is None else metadata).get("packages", []))
    elif exists("package.json"):
        metadata = json.loads(data("package.json"))
        # Match the existing cross-language package.json field semantics.
        fields = _object({} if metadata is None else metadata)
        workspaces = next(
            (fields[k] for k in reversed(list(fields)) if k.lower() == "workspaces"), []
        )
        if isinstance(workspaces, dict):
            workspaces = next(
                (workspaces[k] for k in reversed(list(workspaces)) if k.lower() == "packages"), []
            )
        values = _strings(workspaces) if workspaces is not None else []
    for value in values:
        pattern = value.removeprefix("!")
        if any(c in pattern for c in "\\\x00?[]{}()") or any(
            p in ("", ".", "..") for p in pattern.split("/")
        ):
            raise ValueError("unsupported workspace pattern")
    return values


def _match(pattern: str, name: str) -> bool:
    parts = name.split("/")
    states = [True] + [False] * len(parts)
    for segment in pattern.split("/"):
        following = [False] * len(states)
        for i in range(len(states)):
            if segment == "**":
                following[i] = states[i] or (i > 0 and following[i - 1])
            elif i > 0 and states[i - 1]:
                following[i] = fnmatch.fnmatchcase(parts[i - 1], segment)
        states = following
    return states[-1]


def _root(files: Mapping[str, bytes | None], component: str, python: bool) -> str:
    selected = ancestor = component
    while True:
        patterns = _patterns(files, ancestor, python)
        relative = posixpath.relpath(selected, ancestor)
        if (
            selected != ancestor
            and any(_match(p, relative) for p in patterns if not p.startswith("!"))
            and not any(_match(p[1:], relative) for p in patterns if p.startswith("!"))
        ):
            selected = ancestor
        if ancestor == ".":
            return selected
        ancestor = posixpath.dirname(ancestor) or "."


def _node_installation_required(files: Mapping[str, bytes | None], root: str) -> bool:
    """@spec Node packages need a Bin only with dependency, lockfile or workspace evidence."""
    if any(
        posixpath.normpath(posixpath.join(root, name)) in files
        for name in (
            "npm-shrinkwrap.json",
            "package-lock.json",
            "pnpm-lock.yaml",
            "yarn.lock",
            "bun.lock",
            "bun.lockb",
        )
    ) or _patterns(files, root, False):
        return True
    metadata = _object(
        json.loads(files[posixpath.normpath(posixpath.join(root, "package.json"))] or b"null") or {}
    )
    for name in (
        "dependencies",
        "devDependencies",
        "optionalDependencies",
        "peerDependencies",
        "bundledDependencies",
        "bundleDependencies",
    ):
        value = metadata.get(name)
        if value is not None and (not isinstance(value, (dict, list)) or len(value) > 0):
            return True
    return False


def discover_depot(files: Mapping[str, bytes | None], component: str) -> Depot | None:
    """One logical Depot per Component; shared workspace bins retain the same path.

    @spec Declarations survive physical reclamation and never authorize deletion.
    @link component://docs/depot.md
    """
    bins = []
    for python, manifest, directory in (
        (False, "package.json", "node_modules"),
        (True, "pyproject.toml", ".venv"),
    ):
        if posixpath.normpath(posixpath.join(component, manifest)) not in files:
            continue
        try:
            root = _root(files, component, python)

            def name(value: str) -> str:
                return posixpath.normpath(posixpath.join(root, value))

            if python:
                if name("uv.lock") not in files:
                    continue
            else:
                if not _node_installation_required(files, root):
                    continue
                if name(".pnp.cjs") in files:
                    continue
                if name(".yarnrc.yml") in files:
                    config = yaml.safe_load(files[name(".yarnrc.yml")] or b"")
                    if _object({} if config is None else config).get("nodeLinker") == "pnp":
                        continue
            bins.append(Bin(name(directory), "dependencies"))
        except (ValueError, UnicodeError, yaml.YAMLError):
            continue  # No guessed installation root for malformed/unsupported metadata.
    return Depot(tuple(sorted(bins, key=lambda b: b.path))) if bins else None
