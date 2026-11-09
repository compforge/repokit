"""Repository-local Git context is never inherited by explicitly targeted operations."""

import os
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
    return {
        key: value
        for key, value in source.items()
        if key not in _LOCAL and not key.startswith(("GIT_CONFIG_KEY_", "GIT_CONFIG_VALUE_"))
    }
