import subprocess

import pytest

from repokit import git, snapshot
from repokit import state as checkout_state


def run(root, *args):
    return subprocess.check_output(["git", "-C", str(root), *args], text=True).strip()


def test_snapshot_unicode_clean_commit_and_symlink(repo):
    first = snapshot(repo)
    assert first.complete
    (repo / "测试.py").write_text("print(2)\n")
    assert git.changed_paths(repo) == ["测试.py"]
    dirty = snapshot(repo)
    assert dirty.digest != first.digest
    assert git.stage(repo, ["测试.py"]).ok
    assert git.commit(repo, "second").ok
    assert snapshot(repo).digest == dirty.digest
    (repo / "alias").symlink_to("测试.py")
    assert snapshot(repo).complete
    (repo / "outside").symlink_to("/etc/hosts")
    assert not snapshot(repo).complete


def test_literal_stage_preserves_other_index_entries(repo):
    (repo / "[a].txt").write_text("literal")
    (repo / "a.txt").write_text("unrelated")
    assert git.stage(repo, ["[a].txt"]).ok
    assert run(repo, "diff", "--cached", "--name-only") == "[a].txt"


def test_worktree_dirty_removal_and_remote_lease(repo, tmp_path):
    path = repo.parent / (repo.name + "-checkout")
    assert checkout_state.add_worktree(repo, path, "HEAD", branch="feature").ok
    (path / "scratch").write_text("keep me")
    assert not checkout_state.remove_worktree(repo, path).ok
    assert (path / "scratch").read_text() == "keep me"
    (path / "scratch").unlink()
    assert checkout_state.remove_worktree(repo, path).ok
    remote = repo.parent / (repo.name + "-remote.git")
    subprocess.run(["git", "init", "--bare", "-q", str(remote)], check=True)
    run(repo, "remote", "add", "origin", str(remote))
    assert git.push(repo, "feature").ok
    (repo / "测试.py").write_text("next\n")
    git.stage(repo, ["测试.py"])
    assert git.commit(repo, "next").ok
    assert not git.push(repo, "feature", lease="0" * 40).ok


def test_rebase_keeps_conflicts(repo):
    base = run(repo, "rev-parse", "HEAD")
    run(repo, "checkout", "-qb", "target")
    (repo / "测试.py").write_text("target\n")
    git.stage(repo, ["测试.py"])
    assert git.commit(repo, "target").ok
    run(repo, "checkout", "-qb", "feature", base)
    (repo / "测试.py").write_text("feature\n")
    git.stage(repo, ["测试.py"])
    assert git.commit(repo, "feature").ok
    assert not git.rebase(repo, "target").ok
    assert "<<<<<<<" in (repo / "测试.py").read_text()


def test_snapshot_missing_repo_and_cancellation(tmp_path):
    from threading import Event

    with pytest.raises(subprocess.CalledProcessError):
        snapshot(tmp_path)
    cancel = Event()
    cancel.set()
    with pytest.raises(InterruptedError):
        snapshot(tmp_path, cancel=cancel)


def test_shared_go_content_identity(tmp_path):
    import json
    from pathlib import Path

    fixture = json.loads(
        (Path(__file__).parents[3] / "conformance/snapshot/working.json").read_text()
    )
    run(tmp_path, "init", "-q")
    for name, content in fixture["files"].items():
        (tmp_path / name).write_text(content)
    for name, target in fixture["symlinks"].items():
        (tmp_path / name).symlink_to(target)
    large = fixture["large"]
    (tmp_path / large["path"]).write_bytes(large["byte"].encode() * large["size"])
    observed = snapshot(tmp_path)
    assert observed.complete and observed.digest == fixture["digest"]
