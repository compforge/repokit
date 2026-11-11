import subprocess

import pytest
from test_operations import run

from repokit import git
from repokit import state as checkout_state
from repokit.git.remote import parse_remote_url, remote
from repokit.git.state import checkout_info, list_checkouts, list_worktrees, main_repo_root


def test_history_paths_preserve_names_and_rename_sides(repo):
    assert git.committed_paths(repo) == ["测试.py"]
    root = run(repo, "rev-parse", "HEAD")
    name = " spaced\n中文.py "
    run(repo, "mv", "测试.py", name)
    assert git.commit(repo, "rename").ok
    assert set(git.committed_paths(repo)) == {"测试.py", name}
    assert set(git.range_paths(repo, root)) == {"测试.py", name}
    with pytest.raises(subprocess.CalledProcessError):
        git.range_paths(repo, "missing")


def test_range_uses_merge_base_and_merge_commit_first_parent(repo):
    base = run(repo, "rev-parse", "HEAD")
    run(repo, "checkout", "-qb", "target")
    (repo / "target").write_text("target")
    git.stage(repo, ["target"])
    git.commit(repo, "target")
    run(repo, "checkout", "-qb", "feature", base)
    (repo / "feature").write_text("feature")
    git.stage(repo, ["feature"])
    git.commit(repo, "feature")
    assert git.range_paths(repo, "target") == ["feature"]
    run(repo, "merge", "--no-ff", "-m", "merge", "target")
    assert git.committed_paths(repo) == ["target"]


@pytest.mark.parametrize(
    "url",
    [
        "git@github.com:org/sub/repo.git",
        "ssh://git@github.com/org/sub/repo.git",
        "git+ssh://git@github.com/org/sub/repo.git",
        "git+https://github.com/org/sub/repo.git",
        "ssh+git://git@github.com/org/sub/repo.git",
        "custom://github.com/org/sub/repo.git",
        "https://user:secret@github.com/org/sub/repo.git",
    ],
)
def test_remote_identity(repo, url):
    value = parse_remote_url(url)
    assert (value.host, value.path) == ("github.com", "org/sub/repo")
    run(repo, "remote", "add", "origin", url)
    assert remote(repo) == value


@pytest.mark.parametrize("url", ["file:///tmp/repo", "/tmp/a:b", "../repo", "https://"])
def test_local_paths_have_no_remote_identity(url):
    assert parse_remote_url(url) is None


def test_checkout_topology_preserves_worktree_names(repo):
    linked = repo.parent / (repo.name + " 中文\r\n ")
    assert checkout_state.add_worktree(repo, linked, "HEAD", branch="linked").ok
    info = checkout_info(linked)
    assert info.linked and info.root == str(linked.resolve())
    assert info.main_root == str(repo.resolve())
    assert list_worktrees(repo)[1][0] == str(linked.resolve())
    assert checkout_state.remove_worktree(repo, linked).ok


def test_separate_git_directory_is_not_a_checkout(repo):
    metadata = repo.parent / (repo.name + "-metadata")
    run(repo, "init", "--separate-git-dir", str(metadata))
    linked = repo.parent / (repo.name + "-linked")
    assert checkout_state.add_worktree(repo, linked, "HEAD", branch="linked").ok
    for path in (repo, linked):
        info = checkout_info(path)
        assert info.common_dir == str(metadata.resolve())
        assert info.main_root is None
        assert main_repo_root(str(path)) is None
    assert checkout_state.remove_worktree(repo, linked).ok


def test_query_failure_is_not_an_empty_inventory(tmp_path):
    with pytest.raises(OSError):
        list_worktrees(tmp_path)
    with pytest.raises(OSError):
        checkout_info(tmp_path)


def test_checkout_inventory_distinguishes_metadata_from_primary(repo):
    primary_branch = run(repo, "branch", "--show-current")
    linked = repo.parent / (repo.name + "-inventory-linked")
    assert checkout_state.add_worktree(repo, linked, "HEAD", branch="inventory-linked").ok
    entries = list_checkouts(linked)
    assert [(e.path, e.branch, e.primary) for e in entries] == [
        (str(repo.resolve()), primary_branch, True),
        (str(linked.resolve()), "inventory-linked", False),
    ]
    assert checkout_state.remove_worktree(repo, linked).ok
    metadata = repo.parent / (repo.name + "-inventory-metadata")
    run(repo, "init", "--separate-git-dir", str(metadata))
    assert checkout_state.add_worktree(repo, linked, "HEAD", branch="inventory-other").ok
    with pytest.raises(OSError):
        list_checkouts(metadata)
    assert list_checkouts(repo)[0].path == str(repo.resolve())
    assert list_checkouts(linked)[0].path is None
    assert list_checkouts(linked)[0].branch == primary_branch
    assert checkout_state.remove_worktree(repo, linked).ok


def test_failed_inventory_queries_are_not_empty_collections(tmp_path):
    from repokit.git.state import list_local_branches, ls_remote_tips

    with pytest.raises(OSError):
        list_local_branches(tmp_path)
    with pytest.raises(OSError):
        ls_remote_tips(tmp_path, "main")
    run(tmp_path, "init", "-q")
    assert list_local_branches(tmp_path) == []
