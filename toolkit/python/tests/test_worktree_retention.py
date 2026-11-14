"""Destructive retention contracts run exclusively in temporary repositories."""

import json
from dataclasses import replace
from pathlib import Path
from unittest.mock import patch

import pytest
from test_pr_observation import REPO, Reader, checkout, git, observer

from repokit import state as checkout_state
from repokit.dependencies import inspect_dependencies
from repokit.forge import PullRequestContext
from repokit.forge.model import ForgeError
from repokit.git import state
from repokit.state.worktrees import (
    CREATED,
    WorktreePolicy,
    clean_worktrees,
    load_policy,
    worktree_lease,
)


@pytest.fixture
def repo(tmp_path):
    root = checkout(tmp_path)
    (root / ".gitignore").write_text("node_modules/\ndist/\n.private/\n")
    (root / "package.json").write_text('{"name":"fixture","version":"1.0.0"}')
    (root / "package-lock.json").write_text('{"lockfileVersion":3}')
    git(root, "add", ".")
    git(root, "commit", "-m", "fixture")
    return root


def linked(repo, name, *, legacy=False, prepared=False):
    path = repo.parent / name
    result = checkout_state.add_worktree(repo, path, "HEAD", branch=name)
    assert result.ok, result.err
    if legacy:
        (Path(state.checkout_info(path).git_dir) / CREATED).unlink()
    if prepared:
        env = inspect_dependencies(path)[0]
        env.directory.mkdir()
        (env.directory / ".repokit-dependencies.json").write_text(
            json.dumps({"manager": env.manager, "fingerprint": env.fingerprint})
        )
        (env.directory / "data").write_text("rebuildable")
    return path


def test_unknown_dates_are_oldest_and_dry_run_predicts_survivors(repo):
    # +case=`Legacy timestamps sort oldest; dry run applies both limits to projected survivors.`
    newest = linked(repo, "new", prepared=True)
    old = linked(repo, "a-legacy", legacy=True, prepared=True)
    middle = linked(repo, "b-legacy", legacy=True, prepared=True)
    policy = WorktreePolicy(2, 1)
    preview = clean_worktrees(repo, policy=policy, dry_run=True)
    assert not preview.errors
    assert (preview.remaining_count, preview.remaining_prepared) == (2, 1)
    assert [(a.path, a.action) for a in preview.actions] == [
        (str(old), "remove_worktree"),
        (str(middle), "release_environment"),
    ]
    assert all(path.exists() and (path / "node_modules").exists() for path in (old, middle, newest))
    report = clean_worktrees(repo, policy=policy)
    assert not report.errors
    assert not old.exists()
    assert middle.exists() and not (middle / "node_modules").exists()
    assert (newest / "node_modules").exists()
    assert state.rev_parse(repo, "a-legacy")
    assert not clean_worktrees(repo, policy=policy).actions


@pytest.mark.parametrize(
    "content", ["dirty", "staged", "untracked", "ignored", "invalid_environment"]
)
@pytest.mark.parametrize("merged", [False, True])
def test_eligible_checkout_discards_all_contents(repo, tmp_path, content, merged):
    # +case=`Merged or excess worktrees are deleted regardless of contents or environment validity.`
    path = linked(repo, "discard", prepared=True)
    policy = WorktreePolicy(10 if merged else 0, 0)
    context = context_for(repo, tmp_path, "discard") if merged else None
    if content in ("dirty", "staged"):
        (path / "package.json").write_text("{}")
        if content == "staged":
            git(path, "add", "package.json")
    elif content == "untracked":
        (path / "notes").write_text("discard")
    elif content == "ignored":
        (path / ".private").mkdir()
        (path / ".private" / "data").write_text("discard")
    else:
        (path / "not-ignored").mkdir()
        policy = replace(policy, outputs=("not-ignored",))
    for dry_run in (True, False):
        report = clean_worktrees(repo, context=context, policy=policy, dry_run=dry_run)
        assert not report.errors
        assert report.remaining_count == 0 and report.remaining_prepared == 0
        assert len(report.actions) == 1 and report.actions[0].action == "remove_worktree"
        assert path.exists() == dry_run
    assert state.rev_parse(repo, "discard")


def test_current_locked_detached_and_primary_are_protected(repo):
    current = linked(repo, "current", prepared=True)
    locked = linked(repo, "locked", prepared=True)
    git(repo, "worktree", "lock", str(locked))
    detached = repo.parent / "detached"
    assert checkout_state.add_worktree(repo, detached, "HEAD", detach=True).ok
    report = clean_worktrees(current, policy=WorktreePolicy(0, 0))
    assert all(path.exists() for path in (repo, current, locked, detached))
    assert report.remaining_count == 3
    assert {a.reason for a in report.actions} == {
        "primary_or_current_checkout",
        "locked",
        "detached_checkout",
    }


def context_for(repo, tmp_path, branch, pr_state="merged", sha=None):
    reader = Reader()
    reader.rows[1] = replace(
        reader.rows[1], source_branch=branch, state=pr_state, sha=sha or state.get_head_sha(repo)
    )
    return PullRequestContext(
        observer(tmp_path / "observations", reader), repo, source=REPO, targets=(REPO,)
    )


@pytest.mark.parametrize("pr_state", ["closed", "open", "merged"])
def test_only_merged_is_terminal(repo, tmp_path, pr_state):
    path = linked(repo, "proposal")
    context = context_for(repo, tmp_path, "proposal", pr_state)
    report = clean_worktrees(repo, context=context)
    assert not report.errors
    assert path.exists() == (pr_state != "merged")
    assert state.rev_parse(repo, "proposal")


def test_merged_checkout_removed_with_new_commits_preserved(repo, tmp_path):
    path = linked(repo, "proposal")
    context = context_for(repo, tmp_path, "proposal")
    git(path, "commit", "--allow-empty", "-m", "new work")
    head = state.get_head_sha(path)
    report = clean_worktrees(repo, context=context)
    assert not report.errors and report.actions[0].reason == "merged"
    assert not path.exists() and state.rev_parse(repo, "proposal") == head


def test_failed_verification_does_not_fall_back_to_age_removal(repo, tmp_path):
    path = linked(repo, "proposal")
    context = context_for(repo, tmp_path, "proposal")
    with patch.object(context, "verify", side_effect=ForgeError("offline")):
        report = clean_worktrees(repo, context=context, policy=WorktreePolicy(0, 0))
    assert path.exists() and report.errors


def test_symlink_environment_never_erases_destination(repo, tmp_path):
    path = linked(repo, "linked-env")
    outside = tmp_path / "shared"
    outside.mkdir()
    (outside / ".repokit-dependencies.json").write_text('{"manager":"npm","fingerprint":"old"}')
    (path / "node_modules").symlink_to(outside, target_is_directory=True)
    report = clean_worktrees(repo, policy=WorktreePolicy(0, 0))
    assert not path.exists() and outside.exists() and not report.errors


def test_declared_outputs_only_and_tracked_files_are_protected(repo):
    path = linked(repo, "outputs")
    (path / "dist").mkdir()
    (path / "dist" / "data").write_text("build output")
    clean_worktrees(repo, policy=WorktreePolicy(1, 0, ("dist",)))
    assert path.exists() and not (path / "dist").exists()
    (path / "dist").mkdir()
    (path / "dist" / "source").write_text("tracked")
    git(path, "add", "-f", "dist/source")
    report = clean_worktrees(repo, policy=WorktreePolicy(1, 0, ("dist",)))
    assert (path / "dist" / "source").exists() and report.errors


def test_invalid_policy_and_shared_repo_config(repo):
    for values in ((1, 2), (-1, 0), (0, -1)):
        with pytest.raises(ValueError):
            WorktreePolicy(*values)
    with pytest.raises(ValueError):
        WorktreePolicy(outputs=("../outside",))
    path = linked(repo, "config")
    from repokit.state.persistent import persistent_directory

    config = persistent_directory(repo) / "config.json"
    config.parent.mkdir(parents=True, exist_ok=True)
    config.write_text('{"worktree":{"max_count":2,"max_prepared":1}}')
    assert load_policy(path) == WorktreePolicy(2, 1)


def test_busy_lease_defers_maintenance(repo):
    path = linked(repo, "busy")
    with worktree_lease(repo), pytest.raises(BlockingIOError):
        clean_worktrees(repo, policy=WorktreePolicy(0, 0))
    assert path.exists()


def test_protected_old_checkout_does_not_evict_newest_retained_set(repo):
    old = linked(repo, "old", prepared=True)
    new = linked(repo, "new", prepared=True)
    git(repo, "worktree", "lock", str(old))
    report = clean_worktrees(repo, policy=WorktreePolicy(1, 1))
    assert old.exists() and new.exists()
    assert (new / "node_modules").exists()
    assert (report.remaining_count, report.remaining_prepared) == (2, 2)


def test_unmanaged_dependencies_count_toward_prepared_limit(repo):
    old = linked(repo, "unmanaged", prepared=True)
    newest = linked(repo, "managed", prepared=True)
    (old / "node_modules" / ".repokit-dependencies.json").unlink()
    report = clean_worktrees(repo, policy=WorktreePolicy(2, 1))
    assert report.remaining_prepared == 2
    assert (newest / "node_modules").exists()
    assert any(a.reason == "unmanaged_environment" for a in report.actions)


@pytest.mark.parametrize("change", ["branch", "head", "detached"])
def test_checkout_identity_rechecked_after_pr_verification(repo, tmp_path, change):
    path = linked(repo, "proposal")
    context = context_for(repo, tmp_path, "proposal")
    verify = context.verify

    def changed(row):
        result = verify(row)
        if change == "branch":
            git(path, "switch", "-c", "new-work")
        elif change == "head":
            git(path, "commit", "--allow-empty", "-m", "new work")
        else:
            git(path, "switch", "--detach")
        return result

    with patch.object(context, "verify", side_effect=changed):
        report = clean_worktrees(repo, context=context)
    assert path.exists()
    assert report.actions[0].reason == "checkout_identity_changed"


def test_successful_pr_write_does_not_refresh_the_repository(repo, tmp_path):
    context = context_for(repo, tmp_path, "topic", "open")
    from repokit.forge import PullRequestKey

    key = PullRequestKey(REPO, 1)
    reader = context.observer.reader
    pr = reader.get(1)
    with (
        patch.object(reader, "update", return_value=pr, create=True),
        patch.object(context, "refresh", side_effect=AssertionError("full refresh")) as refresh,
    ):
        assert context.update(key, title="updated") is pr
    refresh.assert_not_called()
    assert context.last_lifecycle is None


def test_failed_pr_write_does_not_run_maintenance(repo, tmp_path):
    context = context_for(repo, tmp_path, "topic", "open")
    from repokit.forge import PullRequestKey

    with (
        patch.object(
            context.observer.reader, "close", side_effect=ForgeError("rejected"), create=True
        ),
        patch.object(context, "refresh") as refresh,
    ):
        with pytest.raises(ForgeError):
            context.close(PullRequestKey(REPO, 1))
        refresh.assert_not_called()


def test_lease_wait_resolves_topology_once_and_returns_git_result(repo):
    import threading
    import time

    from repokit import operation

    path = repo.parent / "waited"
    with worktree_lease(repo):
        with patch.object(state, "checkout_info", wraps=state.checkout_info) as observe:
            with pytest.raises(TimeoutError):
                with worktree_lease(repo, timeout=0.15):
                    pytest.fail("busy lease acquired")
            assert observe.call_count == 1
        with operation(timeout=0.15):
            result = checkout_state.add_worktree(repo, path, "HEAD", branch="waited")
        assert not result.ok and not result.uncertain and not path.exists()
        result_holder = []
        thread = threading.Thread(
            target=lambda: result_holder.append(
                checkout_state.add_worktree(repo, path, "HEAD", branch="waited")
            )
        )
        thread.start()
        time.sleep(0.1)
        assert thread.is_alive()
    thread.join(timeout=5)
    assert result_holder[0].ok and path.exists()


def test_partial_environment_release_keeps_ownership_for_retry(repo):
    from repokit.state import worktrees

    path = linked(repo, "partial", prepared=True)
    directory = path / "node_modules"

    def interrupted(directory):
        (directory / ".repokit-dependencies.json").unlink()
        raise OSError("interrupted deletion")

    with patch.object(worktrees.shutil, "rmtree", side_effect=interrupted):
        first = clean_worktrees(repo, policy=WorktreePolicy(1, 0))
    assert first.errors and directory.exists()
    second = clean_worktrees(repo, policy=WorktreePolicy(1, 0))
    assert not second.errors and not directory.exists()
    assert second.remaining_prepared == 0


def test_replaced_environment_is_not_deleted_by_release_journal(repo):
    from repokit.state import worktrees

    path = linked(repo, "replacement", prepared=True)
    directory = path / "node_modules"
    with patch.object(worktrees.shutil, "rmtree", side_effect=OSError("interrupted")):
        clean_worktrees(repo, policy=WorktreePolicy(1, 0))
    directory.rename(path / ".private")
    directory.mkdir()
    (directory / "user-data").write_text("preserve")
    report = clean_worktrees(repo, policy=WorktreePolicy(1, 0))
    assert report.errors and (directory / "user-data").exists()


def test_completed_release_journal_recovers_without_touching_replacement(repo):
    import shutil

    from repokit.state import worktrees

    path = linked(repo, "completed", prepared=True)
    directory = path / "node_modules"
    identity = directory.stat()
    journal = Path(state.checkout_info(path).git_dir) / "repokit-releases.json"
    journal.write_text(json.dumps({"node_modules": [identity.st_dev, identity.st_ino]}))
    shutil.rmtree(directory)
    original = journal.read_text()
    assert not clean_worktrees(repo, policy=WorktreePolicy(1, 1), dry_run=True).errors
    assert journal.read_text() == original
    assert not clean_worktrees(repo, policy=WorktreePolicy(1, 1)).errors
    assert json.loads(journal.read_text()) == {}
    directory.mkdir()
    (directory / "user-data").write_text("preserve")
    assert worktrees._pending_releases(path, recover=True) == {}
    assert (directory / "user-data").exists()
