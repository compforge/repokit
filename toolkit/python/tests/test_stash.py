from unittest.mock import patch

from test_operations import run

from repokit import git
from repokit import state as checkout_state
from repokit.git import stash as stash


def test_restore_keeps_partial_staging_and_untracked_content(repo):
    (repo / "测试.py").write_text("staged\n")
    assert git.stage(repo, ["测试.py"]).ok
    (repo / "测试.py").write_text("working\n")
    (repo / "new file").write_text("untracked")
    before_index = run(repo, "write-tree")
    saved = stash.save(repo, include_untracked=True, message="用户\nmessage")
    assert saved.result.ok and saved.oid
    assert not (repo / "new file").exists()
    assert git.create_branch(repo, "next", "HEAD").ok
    assert stash.restore(repo, saved.oid).ok
    assert run(repo, "write-tree") == before_index
    assert (repo / "测试.py").read_text() == "working\n"
    assert (repo / "new file").read_text() == "untracked"
    assert saved.oid in run(repo, "stash", "list", "--format=%H")


def test_shared_stash_tip_cannot_change_saved_identity(repo, tmp_path):
    other = tmp_path / "other"
    assert checkout_state.add_worktree(repo, other, "HEAD", detach=True).ok
    (repo / "测试.py").write_text("ours")
    (other / "测试.py").write_text("theirs")
    original = git.git
    own_oid = ""

    def interleave(root, *args, **kwargs):
        nonlocal own_oid
        result = original(root, *args, **kwargs)
        if args[:2] == ("stash", "push"):
            own_oid = run(repo, "rev-parse", "refs/stash")
            run(other, "stash", "push", "-m", "other checkout")
        return result

    with patch("repokit.git.operations.git", side_effect=interleave):
        saved = stash.save(repo)
    assert saved.result.ok and saved.oid == own_oid
    other_oid = run(repo, "rev-parse", "refs/stash")
    assert other_oid != saved.oid
    assert stash.restore(repo, saved.oid).ok
    assert (repo / "测试.py").read_text() == "ours"
    assert run(repo, "rev-parse", "refs/stash") == other_oid


def test_noop_does_not_reuse_an_older_stash(repo):
    (repo / "测试.py").write_text("saved")
    first = stash.save(repo)
    second = stash.save(repo)
    assert first.result.ok and first.oid
    assert second.result.ok and second.oid is None


def test_failed_or_uncertain_save_keeps_failure_and_recovery_identity(repo):
    (repo / "测试.py").write_text("saved")
    original = git.git

    def timeout_after_save(root, *args, **kwargs):
        result = original(root, *args, **kwargs)
        return git.GitResult(-1, "", "timeout", True) if args[:2] == ("stash", "push") else result

    with patch("repokit.git.operations.git", side_effect=timeout_after_save):
        saved = stash.save(repo)
    assert not saved.result.ok and saved.result.uncertain and saved.oid
    assert stash.restore(repo, saved.oid).ok


def test_conflict_keeps_saved_object(repo):
    base = run(repo, "rev-parse", "HEAD")
    (repo / "测试.py").write_text("target")
    assert git.stage(repo, ["测试.py"]).ok
    assert git.commit(repo, "target").ok
    target = run(repo, "rev-parse", "HEAD")
    assert git.create_branch(repo, "old", base).ok
    (repo / "测试.py").write_text("carried")
    saved = stash.save(repo)
    assert saved.oid
    assert git.create_branch(repo, "next", target).ok
    assert not stash.restore(repo, saved.oid).ok
    assert saved.oid in run(repo, "stash", "list", "--format=%H")
