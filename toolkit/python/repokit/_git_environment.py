"""Repository-local Git context is never inherited by explicitly targeted operations."""

import os
import shlex
from collections.abc import Mapping

_LOCAL = frozenset(
    (
        "GIT_DIR",
        "GIT_WORK_TREE",
        "GIT_COMMON_DIR",
        "GIT_INDEX_FILE",
        "GIT_OBJECT_DIRECTORY",
        "GIT_ALTERNATE_OBJECT_DIRECTORIES",
        "GIT_IMPLICIT_WORK_TREE",
        "GIT_GRAFT_FILE",
        "GIT_SHALLOW_FILE",
        "GIT_PREFIX",
        "GIT_NAMESPACE",
        "GIT_NO_REPLACE_OBJECTS",
        "GIT_REPLACE_REF_BASE",
        "GIT_CONFIG",
        "GIT_CONFIG_PARAMETERS",
        "GIT_CONFIG_COUNT",
        "GIT_CEILING_DIRECTORIES",
        "GIT_DISCOVERY_ACROSS_FILESYSTEM",
        "GIT_SUPER_PREFIX",
    )
)


def checkout_environment(environment: Mapping[str, str] | None = None) -> dict[str, str]:
    """Build a child environment; apply explicit temporary-index overrides afterwards.

    @spec A checkout argument determines the target, independently of its caller's Git context.
    @link component://docs/git.md
    """
    source = os.environ if environment is None else environment
    result = {
        key: value
        for key, value in source.items()
        if key not in _LOCAL and not key.startswith(("GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"))
    }

    # Keep only transport/identity -c settings, reindexing the environment protocol.
    pairs = []
    try:
        count = int(source.get("GIT_CONFIG_COUNT", "0"))
    except ValueError:
        count = 0
    for index in range(max(0, min(count, len(source)))):
        key = source.get(f"GIT_CONFIG_KEY_{index}", "")
        value = source.get(f"GIT_CONFIG_VALUE_{index}")
        if value is not None and _portable_config(key):
            pairs.append((key, value))
    if pairs:
        result["GIT_CONFIG_COUNT"] = str(len(pairs))
        for index, (key, value) in enumerate(pairs):
            result[f"GIT_CONFIG_KEY_{index}"] = key
            result[f"GIT_CONFIG_VALUE_{index}"] = value
    try:
        parameters = shlex.split(source.get("GIT_CONFIG_PARAMETERS", ""))
    except ValueError:
        parameters = []
    retained = [value for value in parameters if _portable_config(value.partition("=")[0])]
    if retained:
        result["GIT_CONFIG_PARAMETERS"] = " ".join(
            "'" + value.replace("'", "'\\''") + "'" for value in retained
        )
    return result


def _portable_config(key: str) -> bool:
    key = key.lower()
    return key.startswith(
        ("http.", "https.", "credential.", "url.", "user.", "author.", "committer.")
    ) or key in {"core.sshcommand", "core.gitproxy", "core.askpass", "ssh.variant"}
