from unittest.mock import patch

import pytest
from test_operations import run

from repokit import git
from repokit import state as checkout_state
from repokit.git.index import staged_changes, status_entries
from repokit.git.state import git_path


def partial(repo):
    (repo / "测试.py").write_text("staged\n")
    assert git.stage(repo, ["测试.py"]).ok
    (repo / "测试.py").write_text("working\n")
    return git_path(repo, "index").read_bytes()


def test_rejection_preserves_partial_index_and_worktree(repo):
    before = partial(repo)
    (repo / "extra").write_text("extra")

    def reject(index):
        assert {c.path for c in index.changes} == {"测试.py", "extra"}
        assert git_path(repo, "index").read_bytes() == before
        # Concurrent ordinary Git writers cannot race the validated replacement.
        assert not git.stage(repo, ["extra"]).ok
        raise ValueError("policy rejected")

    with pytest.raises(ValueError, match="policy rejected"):
        git.stage(repo, ["extra"], validate=reject)
    assert git_path(repo, "index").read_bytes() == before
    assert (repo / "测试.py").read_text() == "working\n"
    assert not git_path(repo, "index.lock").exists()


def test_status_query_does_not_refresh_index(repo):
    before = partial(repo)
    status_entries(repo)
    assert git_path(repo, "index").read_bytes() == before


def test_validated_stage_installs_literal_paths_and_keeps_partial_staging(repo):
    partial(repo)
    names = [" [a]\n中文 ", "a", "link"]
    for name in names[:2]:
        (repo / name).write_text(name)
    (repo / "link").symlink_to("a")
    seen = []
    assert git.stage(repo, [names[0], "link"], validate=lambda index: seen.extend(index.changes)).ok
    assert {c.path for c in seen} == {"测试.py", names[0], "link"}
    assert run(repo, "show", ":测试.py") == "staged"
    assert {c.path for c in staged_changes(repo)} == {"测试.py", names[0], "link"}


def test_failed_add_keeps_original_index(repo):
    before = partial(repo)
    called = []
    result = git.stage(repo, ["missing"], validate=lambda index: called.append(index))
    assert not result.ok and not called
    assert git_path(repo, "index").read_bytes() == before
    assert not git_path(repo, "index.lock").exists()


def test_existing_lock_is_preserved(repo):
    before = partial(repo)
    lock = git_path(repo, "index.lock")
    lock.write_bytes(b"another writer")
    assert not git.stage(repo, ["测试.py"], validate=lambda _: None).ok
    assert lock.read_bytes() == b"another writer"
    assert git_path(repo, "index").read_bytes() == before


def test_unborn_and_empty_validation(tmp_path):
    run(tmp_path, "init", "-q")
    assert git.stage(tmp_path, [], validate=lambda index: index.changes == ()).ok
    assert not git_path(tmp_path, "index").exists()
    (tmp_path / "first").write_text("first")
    assert git.stage(tmp_path, ["first"], validate=lambda _: None).ok
    assert staged_changes(tmp_path)[0].path == "first"


@pytest.mark.parametrize("layout", ["linked", "separate", "split"])
def test_validated_stage_uses_git_index_location(repo, tmp_path, layout):
    root = repo
    if layout == "linked":
        root = tmp_path / "linked"
        assert checkout_state.add_worktree(repo, root, "HEAD", detach=True).ok
    elif layout == "separate":
        root = tmp_path / "separate"
        root.mkdir()
        run(root, "init", "-q", "--separate-git-dir", str(tmp_path / "metadata"))
    else:
        run(repo, "update-index", "--split-index")
    (root / "new").write_text("new")
    assert git.stage(root, ["new"], validate=lambda _: None).ok
    assert "new" in {c.path for c in staged_changes(root)}
    if root != repo:
        assert not staged_changes(repo)


def test_timeout_does_not_install_prepared_index(repo):
    before = partial(repo)
    with patch("repokit.git._staging.git", return_value=git.GitResult(-1, "", "timeout", True)):
        result = git.stage(repo, ["测试.py"], validate=lambda _: None)
    assert not result.ok and result.uncertain
    assert git_path(repo, "index").read_bytes() == before


def test_success_does_not_unlink_next_writers_lock(repo):
    import os

    replace = os.replace
    lock = git_path(repo, "index.lock")

    def install(source, target):
        replace(source, target)
        lock.write_bytes(b"next writer")

    with patch("repokit.git._staging.os.replace", side_effect=install):
        assert git.stage(repo, ["测试.py"], validate=lambda _: None).ok
    assert lock.read_bytes() == b"next writer"


def test_candidate_queries_bind_to_index_blobs_not_worktree(repo):
    name = " settings\n.cfg "
    (repo / name).write_text('[item "one"]\n path = candidate\n')
    assert git.stage(repo, [name]).ok
    (repo / name).write_text('[item "one"]\n path = working\n')
    head = run(repo, "rev-parse", "HEAD")
    run(repo, "update-index", "--add", "--cacheinfo", f"160000,{head},nested")
    seen = []

    def inspect(index):
        assert index.config_values(name, r"^item\..*\.path$") == ["candidate"]
        assert index.config_values("missing", ".*") == []
        assert index.config_values(name, "^absent") == []
        entry = next(e for e in index.entries if e.path == "nested")
        assert (entry.mode, entry.oid, entry.stage) == ("160000", head, 0)
        # No .gitmodules exists. The toolkit does not enforce registration policy.
        seen.append(index)

    assert git.stage(repo, [], validate=inspect).ok
    (repo / name).unlink()
    assert seen[0].config_values(name, r"^item\..*\.path$") == ["candidate"]


def test_candidate_config_disables_external_includes(repo, tmp_path):
    external = tmp_path / "external.cfg"
    external.write_text('[item "external"]\n path = outside\n')
    (repo / "config").write_text(f'[include]\n path = {external}\n[item "local"]\n path = inside\n')

    def inspect(index):
        assert index.config_values("config", r"^item\..*\.path$") == ["inside"]

    assert git.stage(repo, ["config"], validate=inspect).ok


@pytest.mark.parametrize("kind", ["malformed", "symlink", "gitlink"])
def test_candidate_config_errors_preserve_index(repo, kind):
    if kind == "gitlink":
        head = run(repo, "rev-parse", "HEAD")
        run(repo, "update-index", "--add", "--cacheinfo", f"160000,{head},config")
        paths = []
    else:
        if kind == "symlink":
            (repo / "config").symlink_to("测试.py")
        else:
            (repo / "config").write_text("[invalid")
        paths = ["config"]
    before = git_path(repo, "index").read_bytes()
    with pytest.raises(OSError):
        git.stage(repo, paths, validate=lambda index: index.config_values("config", ".*"))
    assert git_path(repo, "index").read_bytes() == before
    assert not git_path(repo, "index.lock").exists()
