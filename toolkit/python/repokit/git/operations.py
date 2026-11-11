"""Git operations with explicit arguments, bounded execution and observable failures."""

from __future__ import annotations

import os
import subprocess
from collections.abc import Callable
from dataclasses import dataclass
from pathlib import Path
from typing import TYPE_CHECKING

from .._git_environment import checkout_environment

if TYPE_CHECKING:
    from .index import IndexView


@dataclass(frozen=True)
class GitResult:
    rc: int
    out: str
    err: str
    uncertain: bool = False

    @property
    def ok(self) -> bool:
        return self.rc == 0

    @property
    def detail(self) -> str:
        """Human-readable failure context, preserving uncertain operation outcomes."""
        detail = self.err or self.out
        if self.uncertain:
            return f"outcome unknown; inspect Git state before retrying: {detail}"
        return detail


def git(
    repo_dir: str | Path,
    *args: str,
    timeout: float = 5,
    raw: bool = False,
    index_file: Path | None = None,
) -> GitResult:
    env = checkout_environment()
    if index_file is not None:
        env["GIT_INDEX_FILE"] = str(index_file.absolute())
    return _run(["git", "-C", str(repo_dir), *args], timeout, raw=raw, env=env)


def git_global(*args: str, timeout: float = 5) -> GitResult:
    return _run(["git", *args], timeout)


def _run(
    argv: list[str], timeout: float, *, raw: bool = False, env: dict[str, str] | None = None
) -> GitResult:
    from .._process import run_command
    from ..budget import current_budget

    budget = current_budget(timeout)
    # Expiry before launch is a known non-action; transport interruption after
    # launch cannot prove that a write was rolled back.
    try:
        budget.remaining()
    except (TimeoutError, InterruptedError) as exc:
        return GitResult(-1, "", str(exc))
    try:
        result = run_command(argv, budget, env=env)
        out = os.fsdecode(result.stdout)
        return GitResult(
            0, out if raw or "\0" in out else out.strip(), os.fsdecode(result.stderr).strip()
        )
    except subprocess.CalledProcessError as exc:
        out = os.fsdecode(exc.output or b"")
        return GitResult(
            exc.returncode,
            out if raw or "\0" in out else out.strip(),
            os.fsdecode(exc.stderr or b"").strip(),
        )
    except (TimeoutError, InterruptedError, ValueError) as exc:
        return GitResult(-1, "", str(exc), uncertain=True)
    except OSError as exc:
        return GitResult(-1, "", str(exc))


def stage(
    repo: str | Path,
    paths: list[str],
    *,
    validate: Callable[[IndexView], None] | None = None,
) -> GitResult:
    """Stage literal paths, retaining unrelated entries.

    With validate, prepare an isolated index and pass its IndexView (entries and
    changes against HEAD) to the callback. A raised exception propagates and leaves
    the original index untouched, as does a failed add. Empty paths validate the
    current index.
    The callback must only inspect facts: the real index is locked until installation.
    Git objects/clean-filter side effects are not rolled back.
    """
    if validate is not None:
        from ._staging import stage_validated

        return stage_validated(repo, paths, validate)
    if not paths:
        raise ValueError("stage requires explicit paths")
    return git(repo, "--literal-pathspecs", "add", "--", *paths, timeout=30)


def create_branch(repo: str | Path, name: str, base: str) -> GitResult:
    """Create and check out a branch at a resolved commit; never stash implicitly."""
    resolved = git(repo, "rev-parse", "--verify", "--end-of-options", f"{base}^{{commit}}")
    return git(repo, "checkout", "-b", name, resolved.out, timeout=30) if resolved.ok else resolved


def commit(repo: str | Path, message: str) -> GitResult:
    """Commit the current index. Selection and validation belong to the caller."""
    return git(repo, "commit", "-m", message, timeout=120)


def push(
    repo: str | Path,
    branch: str,
    *,
    remote: str = "origin",
    upstream: bool = False,
    lease: str | None = None,
) -> GitResult:
    args = ["push"]
    if upstream:
        args.append("--set-upstream")
    if lease is not None:
        args.append(f"--force-with-lease=refs/heads/{branch}:{lease}")
    return git(repo, *args, "--", remote, f"HEAD:refs/heads/{branch}", timeout=120)


def rebase(repo: str | Path, base: str) -> GitResult:
    """Leave conflicts in place for the caller; never auto-abort or discard work."""
    resolved = git(repo, "rev-parse", "--verify", "--end-of-options", f"{base}^{{commit}}")
    return git(repo, "rebase", resolved.out, timeout=120) if resolved.ok else resolved


def continue_rebase(repo: str | Path, *, editor: str | None = None) -> GitResult:
    """Continue staged conflict resolutions; optionally set core.editor for this call.

    Git's editor environment variables still take precedence over core.editor.
    Failure leaves the operation in place for the caller to inspect.
    """
    config = ["-c", f"core.editor={editor}"] if editor is not None else []
    return git(repo, *config, "rebase", "--continue", timeout=120)


def abort_rebase(repo: str | Path) -> GitResult:
    """Ask Git to abort the current rebase and restore its original checkout state."""
    return git(repo, "rebase", "--abort", timeout=30)


def prune_worktrees(repo: str | Path) -> GitResult:
    """Prune stale worktree registrations using Git's default expiration policy.

    This removes metadata, not checkout directories or branches. Git preserves locked
    worktree registrations. The caller decides when cleanup is appropriate.
    """
    return git(repo, "worktree", "prune", timeout=15)


def add_exclude(repo: str | Path, pattern: str) -> bool:
    """Append one exact pattern to info/exclude; return whether a line was added.

    Resolve metadata through Git so linked worktrees share the correct exclude file.
    Preserve existing bytes and pattern whitespace. Git/filesystem failures propagate;
    choosing patterns and accepting a best-effort failure are caller policies.
    """
    from .state import git_path

    if not pattern or any(char in pattern for char in "\r\n\0"):
        raise ValueError("exclude pattern must be one nonempty line without NUL")
    path = git_path(repo, "info/exclude")
    try:
        existing = path.read_bytes()
    except FileNotFoundError:
        existing = b""
    encoded = pattern.encode("utf-8")
    if any(line.removesuffix(b"\r") == encoded for line in existing.split(b"\n")):
        return False
    path.parent.mkdir(parents=True, exist_ok=True)
    separator = b"\n" if existing and not existing.endswith(b"\n") else b""
    with path.open("ab") as output:
        output.write(separator + encoded + b"\n")
    return True


def changed_paths(repo: str | Path, *, base: str = "HEAD", head: str | None = None) -> list[str]:
    """Changed paths, including both rename sides; Git failure remains an error."""
    from .._process import Budget, run

    budget = Budget(10, None)
    root = Path(repo)
    before = (
        run(root, ["rev-parse", "--verify", "--end-of-options", f"{base}^{{commit}}"], budget)
        .decode()
        .strip()
    )
    args = ["diff", "--no-renames", "--name-only", "-z", before]
    if head is not None:
        after = (
            run(root, ["rev-parse", "--verify", "--end-of-options", f"{head}^{{commit}}"], budget)
            .decode()
            .strip()
        )
        args.append(after)
    output = run(root, [*args, "--"], budget)
    if head is None:
        output += run(root, ["ls-files", "--others", "--exclude-standard", "-z"], budget)
    return list(
        dict.fromkeys(os.fsdecode(p) for p in output.split(b"\0") if p and not p.endswith(b"/"))
    )


def committed_paths(repo: str | Path, rev: str = "HEAD") -> list[str]:
    """Paths changed by one commit, against its first parent (empty tree for roots)."""
    from .._process import Budget, run

    budget = Budget(10, None)
    root = Path(repo)
    sha = (
        run(root, ["rev-parse", "--verify", "--end-of-options", f"{rev}^{{commit}}"], budget)
        .decode()
        .strip()
    )
    output = run(
        root,
        [
            "diff-tree",
            "--root",
            "--diff-merges=first-parent",
            "--no-commit-id",
            "-r",
            "--no-renames",
            "--name-only",
            "-z",
            sha,
            "--",
        ],
        budget,
    )
    return list(dict.fromkeys(os.fsdecode(p) for p in output.split(b"\0") if p))


def range_paths(repo: str | Path, base: str, head: str = "HEAD") -> list[str]:
    """Branch changes from the merge base to head, preserving both rename sides."""
    from .._process import Budget, run

    budget = Budget(10, None)
    root = Path(repo)
    refs = [
        run(root, ["rev-parse", "--verify", "--end-of-options", f"{ref}^{{commit}}"], budget)
        .decode()
        .strip()
        for ref in (base, head)
    ]
    output = run(
        root, ["diff", "--no-renames", "--name-only", "-z", f"{refs[0]}...{refs[1]}", "--"], budget
    )
    return list(dict.fromkeys(os.fsdecode(p) for p in output.split(b"\0") if p))


def fetch_branches(
    repo: str | Path, branches: list[str], *, remote: str = "origin", timeout: float = 60
) -> GitResult:
    """Refresh explicit tracking refs, including remote rewrites and narrow fetch configs."""
    from .state import validate_branch_name

    if not branches:
        raise ValueError("fetch requires explicit branches")
    for branch in branches:
        validate_branch_name(repo, branch)
    # Validate the destination namespace before constructing refspecs.
    validate_branch_name(repo, remote)
    refs = [f"+refs/heads/{b}:refs/remotes/{remote}/{b}" for b in dict.fromkeys(branches)]
    return git(repo, "fetch", remote, *refs, timeout=timeout)
