"""Checkout lifecycle: Git mutations, creation records and state publication."""

from pathlib import Path

from ..git.operations import GitResult, git


def add_worktree(
    repo: str | Path,
    path: str | Path,
    ref: str,
    *,
    branch: str | None = None,
    detach: bool = False,
    home: str | Path | None = None,
) -> GitResult:
    """Create a checkout and publish its observed state after Git succeeds.

    @spec State owns the Git mutation and publication sequence. Publication failure
    retains the successful Git outcome and never invites replay of the mutation.
    @link component://docs/state.md
    @case tests/test_repository_state.py
    """
    if branch is not None and detach:
        raise ValueError("branch and detach are mutually exclusive")
    args = ["worktree", "add"]
    if detach:
        args.append("--detach")
    if branch is not None:
        args += ["-b", branch]
    from ..budget import operation
    from .worktrees import record_creation, worktree_lease

    try:
        with operation(timeout=30), worktree_lease(repo, timeout=30):
            result = git(repo, *args, "--", str(path), ref, timeout=30)
            if result.ok:
                created = Path(path) if Path(path).is_absolute() else Path(repo) / path
                try:
                    record_creation(created)
                except (OSError, ValueError) as exc:
                    # The checkout exists; never report the creation as safely retryable.
                    return GitResult(
                        result.rc, result.out, f"checkout created; metadata unavailable: {exc}"
                    )
        if result.ok:
            from .repository import RepositoryState

            try:
                RepositoryState(home).observe(created)
            except (OSError, ValueError) as exc:
                return GitResult(
                    result.rc, result.out, f"checkout created; metadata unavailable: {exc}"
                )
        return result
    except OSError as exc:
        return GitResult(-1, "", str(exc))


def remove_worktree(
    repo: str | Path, path: str | Path, *, force: bool = False, home: str | Path | None = None
) -> GitResult:
    """Default removal preserves dirty and locked worktrees."""
    result = git(
        repo,
        "worktree",
        "remove",
        *(["--force", "--force"] if force else []),
        "--",
        str(path),
        timeout=30,
    )

    if result.ok:
        from .repository import RepositoryState

        try:
            RepositoryState(home).observe(repo, reconcile=True)
        except (OSError, ValueError) as exc:
            return GitResult(
                result.rc, result.out, f"checkout removed; metadata unavailable: {exc}"
            )
    return result
