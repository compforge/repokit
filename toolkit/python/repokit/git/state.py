"""Git checkout, branch and remote observations."""

from __future__ import annotations

from dataclasses import dataclass
from functools import lru_cache
from pathlib import Path
from stat import S_ISDIR, S_ISREG
from time import monotonic
from typing import TypedDict

from . import operations as gitcmd


def git_path(repo_dir: str | Path, name: str) -> Path:
    """Resolve an absolute Git metadata path, even if it does not exist yet.

    Git decides whether metadata is checkout-local or shared. Failed reads raise;
    path whitespace is preserved, including a trailing newline in the name.
    """
    result = gitcmd.git(
        repo_dir, "rev-parse", "--path-format=absolute", "--git-path", name, raw=True
    )
    if not result.ok or not result.out:
        raise OSError(f"cannot resolve Git metadata path {name!r}: {result.err}")
    return Path(result.out.removesuffix("\n"))


def rebase_in_progress(repo_dir: str | Path) -> bool:
    """Observe either Git rebase backend in this checkout; failed reads raise."""
    # git am also owns rebase-apply; only its rebasing marker identifies a rebase.
    for name, matches in (("rebase-merge", S_ISDIR), ("rebase-apply/rebasing", S_ISREG)):
        path = git_path(repo_dir, name)
        try:
            if matches(path.stat().st_mode):
                return True
        except FileNotFoundError:
            pass
    return False


def get_current_branch(repo_dir: str | Path) -> str | None:
    """Current branch, including unborn branches; None means detached HEAD."""
    r = gitcmd.git(repo_dir, "branch", "--show-current")
    if not r.ok:
        raise OSError(f"cannot read current branch: {r.err}")
    return r.out or None


def _ahead_behind(repo_dir: str | Path, target: str) -> tuple[int, int] | None:
    head = rev_parse(repo_dir, "HEAD")
    other = rev_parse(repo_dir, target)
    if not head or not other:
        return None
    # Resolve both endpoints before counting; missing endpoints are distinct from failed counts.
    r = gitcmd.git(repo_dir, "rev-list", "--count", "--left-right", f"{head}...{other}")
    values = r.out.split()
    if not r.ok or len(values) != 2 or not all(v.isdecimal() for v in values):
        raise OSError(f"cannot count divergence from {target}: {r.err or r.out}")
    return int(values[0]), int(values[1])


def get_ahead_behind(repo_dir: str | Path, target: str = "main") -> tuple[int, int] | None:
    """(ahead, behind) vs origin/target; None for missing endpoints, failed reads raise."""
    return _ahead_behind(repo_dir, f"refs/remotes/origin/{target}")


class WorkspaceStatus(TypedDict):
    dirty: bool
    modified_count: int
    untracked_count: int
    complete: bool


def get_workspace_status(repo_dir: str | Path) -> WorkspaceStatus:
    """dict with dirty / modified_count / untracked_count."""
    r = gitcmd.git(repo_dir, "status", "--porcelain")
    if not r.ok:
        return {"dirty": False, "modified_count": 0, "untracked_count": 0, "complete": False}
    lines = [ln for ln in r.out.split("\n") if ln.strip()]
    modified = sum(1 for ln in lines if not ln.startswith("??"))
    untracked = sum(1 for ln in lines if ln.startswith("??"))
    return {
        "dirty": bool(lines),
        "modified_count": modified,
        "untracked_count": untracked,
        "complete": True,
    }


def target_exists(repo_dir: str | Path, target: str = "main") -> bool:
    return bool(rev_parse(repo_dir, f"refs/remotes/origin/{target}"))


def local_default_branch(repo_dir: str | Path, *, remote: str = "origin") -> str | None:
    """Read the locally cached remote HEAD branch without contacting the remote.

    None means the symbolic reference is absent; Git failures raise OSError.
    The cache may be stale. Refreshing it or choosing a fallback belongs to the caller.
    """
    prefix = f"refs/remotes/{remote}/"
    result = gitcmd.git(repo_dir, "symbolic-ref", "--quiet", f"{prefix}HEAD")
    if result.rc == 1:
        return None
    if not result.ok:
        raise OSError(f"cannot read default branch for remote {remote!r}: {result.err}")
    if not result.out.startswith(prefix) or not result.out.removeprefix(prefix):
        raise OSError(f"unexpected default branch reference for remote {remote!r}: {result.out!r}")
    return result.out.removeprefix(prefix)


def refresh_remote_head(repo_dir: str | Path, timeout: int = 5) -> bool:
    """Refresh local origin/HEAD from the remote default branch via one network round-trip
    (`git fetch` never touches origin/HEAD). Best-effort. Used as the no-token fallback when
    the forge can't supply the default branch (see context.repo._resolve_default_branch)."""
    return gitcmd.git(repo_dir, "remote", "set-head", "origin", "--auto", timeout=timeout).ok


def set_local_default_head(repo_dir: str | Path, branch: str) -> bool:
    """Point local `refs/remotes/origin/HEAD` at `branch` — purely local, no network.

    Keeps the git-side cache consistent with an authoritative value resolved elsewhere (the
    forge), so every `local_default_target` caller (gcampr, run_review, …) reads the same answer
    without each re-querying. Best-effort.
    """
    if not branch:
        return False
    return gitcmd.git(
        repo_dir, "symbolic-ref", "refs/remotes/origin/HEAD", f"refs/remotes/origin/{branch}"
    ).ok


def get_worktree_metadata(repo_dir: str | Path) -> tuple[bool, str, str | None]:
    """Checkout metadata derived from the same topology as checkout_info.

    Failed reads raise; an unknown primary location is retained by checkout_info.
    """
    info = checkout_info(repo_dir)
    if not info.linked:
        return False, "", None
    primary = next((entry for entry in list_checkouts(repo_dir) if entry.primary), None)
    return True, info.common_dir, primary.branch if primary else None


def is_linked_worktree(repo_dir: str | Path) -> bool:
    """True for a linked checkout; failed observations raise."""
    return checkout_info(repo_dir).linked


def get_head_sha(repo_dir: str | Path) -> str:
    """Current HEAD object; empty for unborn HEAD, failed reads raise."""
    return rev_parse(repo_dir, "HEAD")


def rev_parse(repo_dir: str | Path, ref: str) -> str:
    """Resolve a revision; empty means absent, not a failed Git observation."""
    r = gitcmd.git(repo_dir, "rev-parse", "--verify", "--quiet", "--end-of-options", ref)
    if r.ok and r.out:
        return r.out
    # Quiet verification uses 1 for absent revisions. Fatal repository errors,
    # timeouts and diagnostics must not be projected as absence.
    if r.rc == 1 and not r.err:
        return ""
    raise OSError(f"cannot resolve revision {ref!r}: {r.err or r.out}")


def is_ancestor(repo_dir: str | Path, ancestor: str | None, descendant: str | None) -> bool:
    """Whether ancestor is reachable; invalid objects and failed reads raise."""
    if not ancestor or not descendant:
        raise ValueError("ancestry requires two revisions")
    # Even identical inputs must be checked: textual equality proves no object exists.
    r = gitcmd.git(repo_dir, "merge-base", "--is-ancestor", "--", ancestor, descendant)
    if r.rc in (0, 1):
        return r.rc == 0
    raise OSError(f"cannot compare ancestry {ancestor!r} -> {descendant!r}: {r.err}")


def get_upstream_ahead_behind(repo_dir: str | Path) -> tuple[int, int] | None:
    """Divergence from the configured upstream; None when branch/upstream is absent."""
    branch = get_current_branch(repo_dir)
    if branch is None:
        return None
    # for-each-ref returns a successful empty value for no upstream or an unborn branch.
    ref = f"refs/heads/{branch}"
    r = gitcmd.git(repo_dir, "for-each-ref", "--format=%(refname)%00%(upstream)", ref)
    if not r.ok:
        raise OSError(f"cannot read upstream for {branch!r}: {r.err}")
    upstream = next(
        (line.split("\0", 1)[1] for line in r.out.splitlines() if line.startswith(ref + "\0")), ""
    )
    return _ahead_behind(repo_dir, upstream) if upstream else None


def ls_remote_tips(
    repo_dir: str | Path, *branches: str, timeout: int = 5, remote: str = "origin"
) -> dict[str, str]:
    """Read `{branch: sha}` via `git ls-remote` without fetching objects.

    `remote` is a remote name or URL, defaulting to origin. Empty means no matching
    remote refs; failed reads raise OSError.
    """
    if not branches:
        return {}
    r = gitcmd.git(repo_dir, "ls-remote", remote, *branches, timeout=timeout)
    if not r.ok:
        raise OSError(r.err or "cannot read remote branch tips")
    tips: dict[str, str] = {}
    for line in r.out.splitlines():
        if "\t" not in line:
            continue
        sha, ref = line.split("\t", 1)
        if ref.startswith("refs/heads/"):
            tips[ref[len("refs/heads/") :]] = sha
    return tips


def list_worktrees(repo_dir: str | Path) -> list[tuple[str, str, str | None]]:
    """Registered worktrees as (path, SHA, branch); paths are NUL delimited.

    The first entry may be a bare/separate Git directory, not a checkout. Use
    checkout_info for topology. Query failures raise rather than imply an empty repo.
    """
    r = gitcmd.git(repo_dir, "worktree", "list", "--porcelain", "-z")
    if not r.ok:
        raise OSError(r.err or "cannot list worktrees")
    out = []
    for block in r.out.split("\0\0"):
        fields = dict(line.split(" ", 1) for line in block.split("\0") if " " in line)
        if path := fields.get("worktree"):
            branch = fields.get("branch")
            out.append(
                (
                    path,
                    fields.get("HEAD", ""),
                    branch.removeprefix("refs/heads/") if branch else None,
                )
            )
    return out


@dataclass(frozen=True)
class CheckoutInfo:
    root: str
    git_dir: str
    common_dir: str
    main_root: str | None

    @property
    def linked(self) -> bool:
        return self.git_dir != self.common_dir


def _checkout_info(
    repo_dir: str | Path, entries: list[tuple[str, str, str | None]]
) -> CheckoutInfo:
    """Observe checkout topology, without choosing a caller's state directory.

    main_root is the checkout recoverable from shared Git metadata. It can be
    unknown for separate Git directories: their original checkout has no backlink.
    Non-checkouts and failed observations raise OSError.
    """

    def query(path: str | Path, flag: str) -> str:
        result = gitcmd.git(path, "rev-parse", "--path-format=absolute", flag, raw=True)
        if not result.ok or not result.out:
            raise OSError(result.err or f"cannot resolve {flag}")
        return str(Path(result.out.removesuffix("\n")).resolve())

    root = query(repo_dir, "--show-toplevel")
    if not (Path(root) / ".git").exists():
        raise OSError("Git metadata directory is not a checkout")
    gd = query(repo_dir, "--git-dir")
    cd = query(repo_dir, "--git-common-dir")
    main = None
    if entries:
        try:
            candidate = query(entries[0][0], "--show-toplevel")
            # Git can treat a separate metadata directory as the work tree. Its
            # lack of a .git entry distinguishes it from an actual checkout.
            if (Path(candidate) / ".git").exists() and query(candidate, "--git-common-dir") == cd:
                main = candidate
        except OSError:
            pass
    return CheckoutInfo(root, gd, cd, main)


def checkout_info(repo_dir: str | Path) -> CheckoutInfo:
    """Observe live checkout topology without persisting or reusing old inventory."""
    return _checkout_info(repo_dir, list_worktrees(repo_dir))


@dataclass(frozen=True)
class CheckoutEntry:
    """Registered checkout; path is unknown when only its metadata is locatable."""

    path: str | None
    head: str
    branch: str | None
    primary: bool


def list_checkouts(repo_dir: str | Path) -> list[CheckoutEntry]:
    """Project Git registrations into checkout locations, retaining unknown paths.

    A separate Git directory has no backlink to its primary checkout. When called
    from that primary checkout we can identify it directly; from a linked checkout
    its location stays unknown. Unknown must not be treated as an absent checkout.
    """
    entries = list_worktrees(repo_dir)
    info = _checkout_info(repo_dir, entries)
    main = info.root if not info.linked else info.main_root
    return [
        CheckoutEntry(main if index == 0 else str(Path(path).resolve()), head, branch, index == 0)
        for index, (path, head, branch) in enumerate(entries)
        if head  # Bare registration has no HEAD and is not a checkout.
    ]


def main_repo_root(repo_dir: str) -> str | None:
    """Verified main checkout, or None for an unknown location; failed reads raise."""
    return checkout_info(repo_dir).main_root


def list_local_branches(repo_dir: str | Path) -> list[tuple[str, str]]:
    """Every local branch as ``(name, head_sha)`` in stable name order.

    A PR/MR can outlive its checkout, so lifecycle inventory starts from local
    refs rather than from ``git worktree list``. The NUL separator avoids
    parsing human-facing branch output.
    """
    r = gitcmd.git(
        repo_dir,
        "for-each-ref",
        "--sort=refname",
        "--format=%(refname)%00%(objectname)",
        "refs/heads",
    )
    if not r.ok:
        raise OSError(r.err or "cannot list local branches")
    out: list[tuple[str, str]] = []
    for line in r.out.splitlines():
        if "\x00" not in line:
            raise OSError("invalid branch inventory")
        ref, sha = line.split("\x00", 1)
        if not ref.startswith("refs/heads/") or not sha:
            raise OSError("invalid branch inventory")
        out.append((ref.removeprefix("refs/heads/"), sha))
    return out


def fetch(repo_dir: str | Path, *refs: str, timeout: int = 8, remote: str = "origin") -> bool:
    """Run a bounded `git fetch <remote> [refs...]` at intentional refresh points.

    `remote` is a remote name or URL, defaulting to origin. Supplied refspecs and
    Git's remote configuration determine which local refs are updated.
    Best-effort: a failed fetch returns False.
    """
    return gitcmd.git(repo_dir, "fetch", remote, *refs, "--quiet", timeout=timeout).ok


def find_checkout(directory: str | Path) -> str | None:
    """Find a physical checkout by its .git entry. None means no ancestor checkout.

    Invalid paths, inaccessible/corrupt metadata and Git failures raise. Does not
    interpret a bare metadata directory as a checkout or choose environment overrides.
    """
    start = Path(directory).resolve(strict=True)
    if not start.is_dir():
        raise NotADirectoryError(str(start))
    for candidate in (start, *start.parents):
        # A metadata directory is not a source checkout, even inside one.
        if candidate.name == ".git":
            return None
        try:
            (candidate / ".git").lstat()
        except FileNotFoundError:
            continue
        result = gitcmd.git(candidate, "rev-parse", "--show-toplevel", raw=True)
        if not result.ok or not result.out:
            raise OSError(result.err or "cannot discover checkout")
        root = result.out.removesuffix("\n")
        if Path(root).resolve() != candidate:
            raise OSError("Git environment does not match discovered checkout")
        return root
    return None


def validate_branch_name(repo_dir: str | Path, name: str) -> None:
    """Require a literal branch name; shorthand such as @{-1} is not a name."""
    result = gitcmd.git(repo_dir, "check-ref-format", "--branch", name)
    if not result.ok or result.out != name:
        raise ValueError(f"invalid branch name {name!r}: {result.err or result.out}")


@dataclass(frozen=True)
class Commit:
    oid: str
    subject: str
    body: str


def commits(
    repo_dir: str | Path, base: str, head: str = "HEAD", *, include_merges: bool = True
) -> list[Commit]:
    """Commits reachable from head but not base; failed/missing endpoints raise."""
    resolved = [rev_parse(repo_dir, ref + "^{commit}") for ref in (base, head)]
    if not all(resolved):
        raise OSError(f"cannot read commits for {base!r}..{head!r}: missing endpoint")
    result = gitcmd.git(
        repo_dir,
        "log",
        "-z",
        "--format=%H%x00%s%x00%b",
        *([] if include_merges else ["--no-merges"]),
        f"{resolved[0]}..{resolved[1]}",
        "--",
        raw=True,
    )
    if not result.ok:
        raise OSError(f"cannot read commit history: {result.err}")
    fields = result.out.removesuffix("\0").split("\0") if result.out else []
    if len(fields) % 3:
        raise OSError("invalid Git commit history")
    return [Commit(*fields[i : i + 3]) for i in range(0, len(fields), 3)]


def merge_base(repo_dir: str | Path, left: str, right: str) -> str | None:
    """Common ancestor; None only for unrelated histories, errors propagate."""
    result = gitcmd.git(repo_dir, "merge-base", "--", left, right)
    if result.rc == 1 and not result.err:
        return None
    if not result.ok or not result.out:
        raise OSError(f"cannot read merge base of {left!r} and {right!r}: {result.err}")
    return result.out


def checkout_at(directory: str | Path) -> CheckoutInfo | None:
    """Exact checkout topology, or None for a directory without a .git entry.

    Failed metadata reads propagate. No ancestor search or application state policy.
    """
    root = Path(directory).resolve()
    try:
        marker = (root / ".git").stat()
    except FileNotFoundError:
        return None
    return _checkout_at(
        str(root), marker.st_dev, marker.st_ino, marker.st_mtime_ns, int(monotonic())
    )


@lru_cache(maxsize=64)
def _checkout_at(root: str, device: int, inode: int, modified: int, second: int) -> CheckoutInfo:
    return checkout_info(root)
