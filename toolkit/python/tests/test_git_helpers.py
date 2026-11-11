import shutil
from pathlib import Path

import pytest
from test_operations import run

from repokit import git
from repokit import state as checkout_state
from repokit.git.state import git_path, list_checkouts, local_default_branch


def test_local_default_branch_reads_only_the_selected_remote(repo):
    assert local_default_branch(repo) is None
    run(repo, "symbolic-ref", "refs/remotes/upstream/HEAD", "refs/remotes/upstream/release/stable")
    assert local_default_branch(repo, remote="upstream") == "release/stable"
    assert local_default_branch(repo) is None
    # A cached symbolic target need not have been fetched yet.
    run(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
    assert local_default_branch(repo) == "main"


def test_local_default_branch_does_not_hide_failed_or_invalid_reads(repo, tmp_path):
    with pytest.raises(OSError, match="cannot read default branch"):
        local_default_branch(tmp_path / "missing")
    run(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/heads/main")
    with pytest.raises(OSError, match="unexpected default branch reference"):
        local_default_branch(repo)


def test_add_exclude_preserves_bytes_and_exact_patterns(repo):
    path = git_path(repo, "info/exclude")
    original = b"# existing comment\r\n/old/\r\n/space "
    path.write_bytes(original)
    assert not git.add_exclude(repo, "/old/")
    assert path.read_bytes() == original
    assert git.add_exclude(repo, "/space")
    assert path.read_bytes() == original + b"\n/space\n"
    assert not git.add_exclude(repo, "/space")
    assert git.add_exclude(repo, "/中文/")
    assert path.read_bytes().endswith("/中文/\n".encode())
    assert git.git(repo, "check-ignore", "space", "中文/file").ok


@pytest.mark.parametrize("separate", [False, True])
def test_add_exclude_uses_shared_metadata_from_linked_checkout(repo, separate):
    if separate:
        run(repo, "init", "--separate-git-dir", str(repo.parent / (repo.name + "-metadata")))
    linked = repo.parent / (repo.name + "-linked")
    assert checkout_state.add_worktree(repo, linked, "HEAD", branch="linked").ok
    path = git_path(repo, "info/exclude")
    path.unlink()
    assert git.add_exclude(linked, "/scratch/")
    assert path.read_bytes() == b"/scratch/\n"
    assert not git.add_exclude(repo, "/scratch/")
    assert git.git(linked, "check-ignore", "scratch/file").ok
    assert git.git(repo, "check-ignore", "scratch/file").ok


@pytest.mark.parametrize("pattern", ["", "one\ntwo", "one\rtwo", "one\0two"])
def test_add_exclude_rejects_multiple_lines(repo, pattern):
    before = git_path(repo, "info/exclude").read_bytes()
    with pytest.raises(ValueError):
        git.add_exclude(repo, pattern)
    assert git_path(repo, "info/exclude").read_bytes() == before


def test_add_exclude_propagates_read_errors_without_overwriting(repo, monkeypatch):
    path = git_path(repo, "info/exclude")
    original = path.read_bytes()
    read_bytes = Path.read_bytes

    def deny(target):
        if target == path:
            raise PermissionError("denied")
        return read_bytes(target)

    monkeypatch.setattr(Path, "read_bytes", deny)
    with pytest.raises(PermissionError, match="denied"):
        git.add_exclude(repo, "/scratch/")
    assert read_bytes(path) == original


def test_add_exclude_propagates_git_and_write_errors(repo, tmp_path):
    with pytest.raises(OSError):
        git.add_exclude(tmp_path / "missing", "/scratch/")
    path = git_path(repo, "info/exclude")
    path.unlink()
    path.mkdir()
    with pytest.raises(IsADirectoryError):
        git.add_exclude(repo, "/scratch/")


def test_prune_worktrees_removes_stale_metadata_but_preserves_live_locked_and_branches(repo):
    paths = {name: repo.parent / (repo.name + "-" + name) for name in ("live", "stale", "locked")}
    for name, path in paths.items():
        assert checkout_state.add_worktree(repo, path, "HEAD", branch=name).ok
    run(repo, "worktree", "lock", str(paths["locked"]))
    for name in ("stale", "locked"):
        shutil.rmtree(paths[name])
    result = git.prune_worktrees(repo)
    assert result.ok
    registered = {entry.path for entry in list_checkouts(repo)}
    assert str(paths["stale"]) not in registered
    assert str(paths["locked"]) in registered
    assert str(paths["live"]) in registered
    assert paths["live"].is_dir()
    assert run(repo, "rev-parse", "refs/heads/stale") == run(repo, "rev-parse", "HEAD")


def test_prune_worktrees_returns_failed_git_result(tmp_path):
    result = git.prune_worktrees(tmp_path)
    assert not result.ok and result.err and not result.uncertain
