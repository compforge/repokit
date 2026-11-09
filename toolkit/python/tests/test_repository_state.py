"""Repository state, cache scope and checkout reclamation contracts."""

import json
from pathlib import Path

import pytest
from test_operations import run

from repokit import git
from repokit._git_environment import checkout_environment
from repokit._state_paths import digest_path, name_part, repository_key
from repokit.state import RepositoryState


def test_shared_identity_and_environment():
    base = Path(__file__).parents[3] / "conformance"
    cases = json.loads((base / "repository-state.json").read_text())
    from repokit._storage import digest
    from repokit.forge.connection import ForgeConnection

    for row in cases["connections"]:
        connection = ForgeConnection(
            row["host"], row["token"], provider=row["provider"], api_host=row["apiHost"]
        )
        assert connection.identity == row["identity"]
        assert digest([connection.name, connection.identity]) == row["connection"]
    for row in cases["repositories"]:
        assert repository_key(row["forge"], row["path"]) == row["key"]
    for row in cases["branches"]:
        assert name_part(row["name"]) == row["key"]
    env = json.loads((base / "git/environment.json").read_text())
    for row in env["configurations"]:
        assert checkout_environment(row["input"]) == row["output"]
    source = dict.fromkeys(env["removed"] + env["preserved"], "value")
    source.update(GIT_CONFIG_KEY_0="core.worktree", GIT_CONFIG_VALUE_0="wrong")
    assert checkout_environment(source) == dict.fromkeys(env["preserved"], "value")


def test_state_lifecycle_and_cache(repo, tmp_path):
    run(repo, "remote", "add", "origin", "https://github.com/owner/project.git")
    state = RepositoryState(tmp_path / "state")
    linked = tmp_path / "linked"
    assert git.add_worktree(repo, linked, "HEAD", branch="feature/a", home=state.store.root).ok
    directory = state.store.root / "repositories/github--owner--project"
    key = digest_path(str(linked.resolve()))
    assert (directory / "branches/feature%2Fa/worktrees" / key / "path").resolve() == linked
    cached = state.branch(repo, "feature/a", max_age=3600)
    assert cached.source == "cache" and cached.complete and len(cached.checkouts) == 1
    (directory / "checkouts" / key / "observation.json").unlink()
    repaired = state.branch(repo, "feature/a", max_age=3600)
    assert repaired.source == "git" and len(repaired.checkouts) == 1
    history = directory / "history/keep"
    history.parent.mkdir()
    history.write_text("keep")
    assert git.remove_worktree(repo, linked, home=state.store.root).ok
    assert not (directory / "checkouts" / key).exists()
    assert state.branch(repo, "feature/a").checkouts == ()
    assert history.read_text() == "keep"
    run(repo, "branch", "-D", "feature/a")
    state.observe(repo, reconcile=True)
    assert not (directory / "branches/feature%2Fa").exists()


def test_clone_heads_are_independent(repo, tmp_path, monkeypatch):
    run(repo, "remote", "add", "origin", "git@github.com:owner/project.git")
    run(repo, "branch", "topic")
    run(repo, "tag", "topic")
    other = tmp_path / "other"
    run(repo, "clone", str(repo), str(other))
    run(other, "remote", "set-url", "origin", "https://github.com/owner/project.git")
    run(other, "config", "user.name", "Fixture")
    run(other, "config", "user.email", "fixture@example.invalid")
    run(other, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "next")
    run(other, "branch", "topic")
    state = RepositoryState(tmp_path / "state")
    directory = state.observe(repo)
    state.observe(other)
    monkeypatch.setenv("GIT_DIR", str(repo / ".git"))
    monkeypatch.setenv("GIT_WORK_TREE", str(repo))
    monkeypatch.setenv("GIT_INDEX_FILE", str(repo / ".git/index"))
    first = state.branch(repo, "topic", max_age=3600)
    second = state.branch(other, "topic", max_age=3600)
    assert first.head != second.head
    assert state.branch(other, "topic").head == second.head
    record = directory / "branches/topic/observation.json"
    before = record.read_bytes()
    with pytest.raises(OSError):
        state.observe(tmp_path / "missing", reconcile=True)
    assert record.read_bytes() == before


def test_detached_and_unregistered_checkout_are_preserved(repo, tmp_path):
    state = RepositoryState(tmp_path / "state")
    linked = tmp_path / "detached"
    assert git.add_worktree(repo, linked, "HEAD", detach=True).ok
    directory = state.observe(repo)
    key = digest_path(str(linked.resolve()))
    assert (directory / "checkouts" / key).exists()
    assert not list((directory / "branches").glob("*/worktrees/" + key))
    # A stale registration is not authority to delete a surviving source directory.
    from repokit.git.state import checkout_info

    info = checkout_info(linked)
    import shutil

    shutil.rmtree(info.git_dir)
    state.observe(repo, reconcile=True)
    assert linked.exists() and (directory / "checkouts" / key).exists()


def test_python_typescript_state_and_lock_interoperate(repo, tmp_path):
    import subprocess
    import time

    module = Path(__file__).parents[2] / "typescript/dist/index.js"
    if not module.exists():
        pytest.skip("TypeScript build unavailable")
    run(repo, "remote", "add", "origin", "https://github.com/owner/project.git")
    run(repo, "branch", "topic")
    state = RepositoryState(tmp_path / "state")
    directory = state.observe(repo)
    program = f"""import {{RepositoryState}} from {json.dumps(module.as_uri())};
      const state=new RepositoryState({json.dumps(str(state.store.root))});
      const row=await state.branch({json.dumps(str(repo))},'topic',{{maxAgeMs:3600000}});
      if(row.source!=='cache')throw Error('cache not shared');
      await state.observe({json.dumps(str(repo))});
      console.log(row.head);"""
    with state.store.locked(directory / "topology"):
        child = subprocess.Popen(
            ["node", "--input-type=module", "-e", program],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        time.sleep(0.15)
        assert child.poll() is None
    out, err = child.communicate(timeout=10)
    assert child.returncode == 0, err
    assert state.branch(repo, "topic", max_age=3600).head == out.strip()


def test_unknown_primary_location_is_incomplete_and_not_reclaimed(repo, tmp_path):
    from repokit.git.state import get_current_branch

    primary_branch = get_current_branch(repo)
    run(repo, "init", "--separate-git-dir", str(tmp_path / "metadata"))
    linked = tmp_path / "linked"
    assert git.add_worktree(repo, linked, "HEAD", branch="topic").ok
    state = RepositoryState(tmp_path / "state")
    directory = state.observe(repo)
    primary_key = digest_path(str(repo.resolve()))
    state.observe(linked, reconcile=True)
    observed = state.branch(linked, primary_branch, max_age=3600)
    assert not observed.complete and not observed.checkouts
    assert (directory / "checkouts" / primary_key / "observation.json").exists()


def test_partial_publication_repairs_without_unrelated_branch_reads(repo, tmp_path, monkeypatch):
    from repokit.git.state import get_current_branch

    branch = get_current_branch(repo)
    run(repo, "branch", "other")
    state = RepositoryState(tmp_path / "state")
    directory = state.observe(repo)
    original = state.store.write

    def fail_branch(path, value, owned):
        if path == directory / "branches" / name_part(branch) / "observation.json":
            raise OSError("interrupted publication")
        return original(path, value, owned)

    monkeypatch.setattr(state.store, "write", fail_branch)
    with pytest.raises(OSError, match="interrupted publication"):
        state.observe(repo)
    monkeypatch.setattr(state.store, "write", original)
    unrelated = directory / "branches/other/observation.json"
    unrelated.write_text("{")
    for age in (3600, 0):
        observed = state.branch(repo, branch, max_age=age)
        assert observed.source == "git" and len(observed.checkouts) == 1
        assert unrelated.read_text() == "{"


def test_git_transport_configuration_survives_checkout_isolation(repo, monkeypatch):
    monkeypatch.setenv("GIT_CONFIG_COUNT", "2")
    monkeypatch.setenv("GIT_CONFIG_KEY_0", "core.worktree")
    monkeypatch.setenv("GIT_CONFIG_VALUE_0", "/wrong")
    monkeypatch.setenv("GIT_CONFIG_KEY_1", "http.extraHeader")
    monkeypatch.setenv("GIT_CONFIG_VALUE_1", "X-Test: a'b c")
    result = git.git(repo, "config", "--get", "http.extraHeader")
    assert result.ok and result.out.strip() == "X-Test: a'b c"
    monkeypatch.setenv(
        "GIT_CONFIG_PARAMETERS", "'core.worktree=/wrong' 'user.name=Portable identity'"
    )
    result = git.git(repo, "config", "--get", "user.name")
    assert result.ok and result.out.strip() == "Portable identity"
    assert git.git(repo, "rev-parse", "--show-toplevel").out.strip() == str(repo)


def test_worktree_creation_publishes_to_default_home(repo, tmp_path):
    from repokit import resolve_home

    linked = tmp_path / "default-linked"
    result = git.add_worktree(repo, linked, "HEAD", branch="default-home")
    assert result.ok, result.err
    state = RepositoryState()
    assert state.store.root == resolve_home()
    observed = state.branch(repo, "default-home", max_age=3600)
    assert observed.source == "cache" and len(observed.checkouts) == 1
