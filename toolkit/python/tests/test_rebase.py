import subprocess

import pytest
from test_operations import run

from repokit import git
from repokit import state as checkout_state
from repokit.git.state import get_head_sha, git_path, is_ancestor, rebase_in_progress


@pytest.mark.parametrize("mode", ["ordinary", "linked", "separate", "separate-linked"])
@pytest.mark.parametrize("backend", ["merge", "apply"])
@pytest.mark.parametrize("action", ["continue", "abort"])
def test_rebase_lifecycle(repo, mode, backend, action, monkeypatch):
    monkeypatch.delenv("GIT_EDITOR", raising=False)
    base = get_head_sha(repo)
    if mode.startswith("separate"):
        run(repo, "init", "--separate-git-dir", str(repo / "metadata"))
    run(repo, "config", "rebase.backend", backend)
    run(repo, "checkout", "-qb", "target")
    (repo / "测试.py").write_text("target\n")
    assert git.stage(repo, ["测试.py"]).ok
    assert git.commit(repo, "target").ok
    run(repo, "checkout", "-qb", "feature", base)
    (repo / "测试.py").write_text("feature\n")
    assert git.stage(repo, ["测试.py"]).ok
    assert git.commit(repo, "feature").ok
    before = get_head_sha(repo)
    view = repo
    if mode.endswith("linked"):
        run(repo, "checkout", "--detach", base)
        view = repo / "linked"
        assert checkout_state.add_worktree(repo, view, "feature").ok
    assert not rebase_in_progress(view)
    assert not git.rebase(view, "target").ok
    assert rebase_in_progress(view)
    assert git_path(view, "rebase-merge" if backend == "merge" else "rebase-apply").is_dir()
    if view != repo:
        assert not rebase_in_progress(repo)
    if action == "abort":
        assert git.abort_rebase(view).ok
        assert get_head_sha(view) == before
        assert (view / "测试.py").read_text() == "feature\n"
    else:
        # Unresolved conflicts are a known failure, not a successful continuation.
        assert not git.continue_rebase(view, editor="true").ok
        assert rebase_in_progress(view)
        (view / "测试.py").write_text("target + feature\n")
        assert git.stage(view, ["测试.py"]).ok
        assert git.continue_rebase(view, editor="true").ok
        assert is_ancestor(view, "target", "HEAD")
        assert run(view, "log", "-1", "--format=%s") == "feature"
    assert not rebase_in_progress(view)
    assert run(view, "branch", "--show-current") == "feature"


@pytest.mark.parametrize("operation", [git.continue_rebase, git.abort_rebase])
def test_no_rebase_is_a_known_failure(repo, operation):
    result = operation(repo)
    assert not result.ok and not result.uncertain
    assert not rebase_in_progress(repo)


@pytest.mark.parametrize("operation", [git.continue_rebase, git.abort_rebase])
def test_rebase_timeout_is_uncertain(repo, monkeypatch, operation):
    def timeout(argv, budget, **kwargs):
        raise TimeoutError("operation timed out")

    monkeypatch.setattr("repokit._process.run_command", timeout)
    result = operation(repo)
    assert not result.ok and result.uncertain


def test_git_am_conflict_is_not_a_rebase(repo):
    base = get_head_sha(repo)
    (repo / "测试.py").write_text("feature\n")
    assert git.stage(repo, ["测试.py"]).ok
    assert git.commit(repo, "feature").ok
    patch = subprocess.check_output(["git", "-C", str(repo), "format-patch", "-1", "--stdout"])
    run(repo, "checkout", "--detach", base)
    (repo / "测试.py").write_text("target\n")
    assert git.stage(repo, ["测试.py"]).ok
    assert git.commit(repo, "target").ok
    result = subprocess.run(["git", "-C", str(repo), "am"], input=patch, capture_output=True)
    assert result.returncode != 0
    assert git_path(repo, "rebase-apply").is_dir()
    assert not rebase_in_progress(repo)
    run(repo, "am", "--abort")
