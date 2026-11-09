"""Native observations, shared state and branch lifecycle contracts."""

import json
import os
import subprocess
import threading
import time
from dataclasses import replace
from pathlib import Path
from typing import cast

import pytest
from harness_common import Forge as ForgeIdentity
from harness_common import Repository

from repokit.forge import PullRequestContext, PullRequestKey, PullRequestObserver
from repokit.forge.model import Forge, ForgeError, PullRequest
from repokit.forge.observation.store import ObservationStore

REPO = Repository(ForgeIdentity("github"), "owner/project")
KEY = PullRequestKey(REPO, 1)


class Reader:
    provider = "github"

    def __init__(self):
        self.rows = {
            1: PullRequest(
                1,
                state="open",
                source_branch="topic",
                source_repository=REPO.path,
                target_repository=REPO.path,
            )
        }
        self.calls = []
        self.fail = set()

    def for_repository(self, path):
        return self

    def get(self, number):
        self.calls.append(number)
        if number in self.fail:
            raise ForgeError("offline")
        return self.rows[number]

    def prs_for_branch(self, branch, *, source_repository):
        return [
            pr
            for pr in self.rows.values()
            if pr.source_branch == branch and pr.source_repository == source_repository.path
        ]


def observer(tmp_path, reader, **options):
    return PullRequestObserver(
        cast(Forge, reader), forge="github", state_dir=tmp_path, connection="account", **options
    )


def git(root, *args):
    return subprocess.check_output(
        ["git", "-c", "core.hooksPath=/dev/null", "-C", str(root), *args], text=True
    ).strip()


def checkout(tmp_path):
    root = tmp_path / "repo"
    root.mkdir()
    git(root, "init", "-b", "topic")
    git(root, "config", "user.email", "test@example.com")
    git(root, "config", "user.name", "Test")
    git(root, "commit", "--allow-empty", "-m", "initial")
    return root


def test_terminal_state_and_closed_reopening(tmp_path):
    reader = Reader()
    clock = [1000.0]
    service = observer(tmp_path, reader, now=lambda: clock[0])
    assert service.get_pull_request(KEY).state == "open"
    reader.rows[1] = replace(reader.rows[1], state="closed")
    clock[0] += 61
    assert service.get_pull_request(KEY).state == "closed"
    reader.rows[1] = replace(reader.rows[1], state="open")
    clock[0] += 601
    assert service.get_pull_request(KEY).state == "open"
    reader.rows[1] = replace(reader.rows[1], state="merged")
    clock[0] += 61
    assert service.get_pull_request(KEY).state == "merged"
    reader.fail.add(1)
    clock[0] += 10000
    assert service.maintain() == ()
    assert service.get_pull_request(KEY).state == "merged"
    assert len(reader.calls) == 4


def test_failed_pr_does_not_block_healthy_refresh(tmp_path):
    reader = Reader()
    reader.rows[2] = replace(reader.rows[1], number=2)
    clock = [1000.0]
    service = observer(tmp_path, reader, now=lambda: clock[0])
    service.get_pull_request(KEY)
    service.get_pull_request(PullRequestKey(REPO, 2))
    clock[0] += 61
    reader.fail.add(1)
    reader.rows[2] = replace(reader.rows[2], state="merged")
    assert len(service.maintain()) == 1
    assert service.get_pull_request(PullRequestKey(REPO, 2)).state == "merged"
    record = json.loads(service.path(KEY).read_text())
    assert record["observation"]["snapshot"]["state"] == "open"
    before = len(reader.calls)
    service.maintain()
    assert len(reader.calls) == before


def test_branch_reuse_ambiguity_and_live_checkout_topology(tmp_path):
    root = checkout(tmp_path)
    reader = Reader()
    reader.rows[1] = replace(reader.rows[1], state="merged", sha=git(root, "rev-parse", "HEAD"))
    context = PullRequestContext(
        observer(tmp_path / "state", reader), root, source=REPO, targets=(REPO,)
    )
    initial = context.get_checkout()
    assert initial.selected.number == 1 and initial.checkouts[0].primary
    reader.rows[2] = replace(reader.rows[1], number=2, state="open")
    assert context.verify(initial).selected.number == 2
    reader.rows[3] = replace(reader.rows[2], number=3)
    ambiguous = context.verify(initial)
    assert ambiguous.ambiguous and ambiguous.selected is None
    git(root, "switch", "--detach")
    assert context.get_checkout() is None
    assert not context.get_branch("topic").checkouts
    assert context.for_pull_request(KEY)[0].branch == "topic"
    with pytest.raises(ForgeError, match="changed"):
        context.verify(initial)


def test_foreign_fork_same_branch_is_not_related(tmp_path):
    root = checkout(tmp_path)
    reader = Reader()
    reader.rows[1] = replace(reader.rows[1], source_repository="other/project")
    context = PullRequestContext(
        observer(tmp_path / "state", reader), root, source=REPO, targets=(REPO,)
    )
    assert context.get_checkout().selected is None


def test_cancel_while_waiting_for_lock(tmp_path):
    service = observer(tmp_path, Reader())
    errors = []
    with service.store.locked(service.path(KEY)):

        def read():
            try:
                service.get_pull_request(KEY)
            except InterruptedError:
                errors.append("cancelled")

        thread = threading.Thread(target=read)
        thread.start()
        time.sleep(0.05)
        service.close()
        thread.join(timeout=1)
        assert errors == ["cancelled"]


def test_stale_lease_and_displaced_owner(tmp_path):
    store = ObservationStore(tmp_path)
    path = tmp_path / "record.json"
    lock = Path(f"{path}.lock")
    lock.mkdir()
    os.utime(lock, (time.time() - 31, time.time() - 31))
    with store.locked(path) as owned:
        store.write(path, {"version": 1}, owned)
        lock.rename(tmp_path / "old.lock")
        lock.mkdir()
        with pytest.raises(OSError):
            store.write(path, {"version": 2}, owned)
    assert lock.exists() and json.loads(path.read_text())["version"] == 1


def test_typescript_and_python_share_lock_and_terminal_fact(tmp_path):
    # @case Mixed processes serialize publication and share the same merged fact.
    module = Path(__file__).parents[2] / "typescript" / "dist" / "index.js"
    if not module.exists():
        pytest.skip("TypeScript build unavailable; run toolkit/typescript make build for interop")
    reader = Reader()
    service = observer(tmp_path, reader)
    path = service.path(KEY)
    program = """import {PullRequestObserver} from MODULE;
      const reader = {forge:'github', getPullRequest:async()=>{throw Error('duplicate read')}};
      const observer = new PullRequestObserver(reader,
        {stateDir:DIR,connection:'account',background:false});
      const repository = {forge:{name:'github'},path:'owner/project'};
      const result=await observer.getPullRequest({repository,number:1});
      console.log(result.state); await observer.close();""".replace(
        "MODULE", json.dumps(module.as_uri())
    ).replace("DIR", json.dumps(str(tmp_path)))
    with service.store.locked(path) as owned:
        process = subprocess.Popen(
            ["node", "--input-type=module", "-e", program],
            stdout=subprocess.PIPE,
            stderr=subprocess.PIPE,
            text=True,
        )
        time.sleep(0.15)
        assert process.poll() is None
        service.store.write(
            path,
            {
                "version": 1,
                "connection": service.connection,
                "scopeIds": [],
                "failures": 0,
                "observation": {
                    "key": {
                        "repository": {"forge": {"name": "github"}, "path": REPO.path},
                        "number": 1,
                    },
                    "snapshot": {"state": "merged"},
                    "observedAt": "2026-11-02T00:00:00.000Z",
                    "attemptedAt": "2026-11-02T00:00:00.000Z",
                },
            },
            owned,
        )
    output, error = process.communicate(timeout=10)
    assert process.returncode == 0, error
    assert output.strip() == "merged"
    assert service.get_pull_request(KEY).state == "merged" and reader.calls == []


def test_cold_discovery_is_bounded_and_eventually_covers_all_branches(tmp_path):
    root = checkout(tmp_path)
    for index in range(24):
        git(root, "branch", f"old-{index:02}")
    context = PullRequestContext(
        observer(tmp_path / "state", Reader()), root, source=REPO, targets=(REPO,)
    )
    first = context.local_branches(limit=10)
    assert sum(row.error == "association discovery pending" for row in first) == 15
    second = context.local_branches(limit=10)
    assert sum(row.error == "association discovery pending" for row in second) == 5
    assert all(row.error is None for row in context.local_branches(limit=10))


def test_changed_remote_cannot_authorize_reclamation(tmp_path):
    root = checkout(tmp_path)
    reader = Reader()
    context = PullRequestContext(
        observer(tmp_path / "state", reader), root, source=REPO, targets=(REPO,)
    )
    observed = context.get_checkout()
    git(root, "remote", "add", "origin", "https://github.com/other/project.git")
    with pytest.raises(ForgeError, match="remote changed"):
        context.verify(observed)


def test_bad_association_does_not_consume_healthy_discovery_slots(tmp_path):
    root = checkout(tmp_path)
    git(root, "branch", "broken")
    context = PullRequestContext(
        observer(tmp_path / "state", Reader()), root, source=REPO, targets=(REPO,)
    )
    path = context._association_path("broken")
    path.parent.mkdir(parents=True)
    path.write_text("{")
    rows = {row.branch: row for row in context.local_branches(limit=1)}
    assert rows["broken"].error
    assert rows["topic"].selected.number == 1


@pytest.mark.parametrize(
    "record",
    [
        {"version": 1, "keys": [], "nextRefreshAt": "invalid"},
        {"version": 1, "keys": [None], "nextRefreshAt": "2099-01-01T00:00:00Z"},
        {"version": 1, "keys": [{}], "nextRefreshAt": "2099-01-01T00:00:00Z"},
    ],
)
def test_malformed_association_is_reported_without_aborting_scan(tmp_path, record):
    root = checkout(tmp_path)
    git(root, "branch", "broken")
    context = PullRequestContext(
        observer(tmp_path / "state", Reader()), root, source=REPO, targets=(REPO,)
    )
    path = context._association_path("broken")
    path.parent.mkdir(parents=True)
    path.write_text(json.dumps(record))
    assert context.get_branch("broken").error
    rows = {row.branch: row for row in context.local_branches()}
    assert rows["broken"].error
    assert rows["topic"].selected.number == 1


def test_missing_pr_head_preserves_candidates_and_healthy_branches(tmp_path):
    root = checkout(tmp_path)
    git(root, "branch", "broken")
    reader = Reader()
    reader.rows[2] = replace(
        reader.rows[1], number=2, source_branch="broken", state="merged", sha="f" * 40
    )
    context = PullRequestContext(
        observer(tmp_path / "state", reader), root, source=REPO, targets=(REPO,)
    )
    rows = {row.branch: row for row in context.local_branches()}
    assert "ancestry unavailable" in rows["broken"].error
    assert rows["broken"].selected is None
    assert rows["broken"].candidates[0].number == 2
    assert rows["topic"].selected.number == 1
    assert context.verify(rows["broken"]).error


def test_disappearing_branch_does_not_authorize_mutation(tmp_path, monkeypatch):
    root = checkout(tmp_path)
    git(root, "branch", "broken")
    context = PullRequestContext(
        observer(tmp_path / "state", Reader()), root, source=REPO, targets=(REPO,)
    )
    original = context._branch
    removed = False

    def read(branch, **kwargs):
        nonlocal removed
        if branch == "broken" and not removed:
            git(root, "branch", "-D", branch)
            removed = True
        return original(branch, **kwargs)

    monkeypatch.setattr(context, "_branch", read)
    rows = {row.branch: row for row in context.local_branches()}
    assert rows["broken"].head  # One planning inventory survives the concurrent change.
    assert "topic" in rows
    with pytest.raises(ValueError, match="does not exist"):
        context.verify(rows["broken"])


def test_legacy_pr_record_migrates_and_merged_metadata_refreshes(tmp_path):
    from repokit.forge.observation.store import digest

    reader = Reader()
    service = observer(tmp_path, reader)
    reader.rows[1] = replace(reader.rows[1], state="merged", title="original")
    service.get_pull_request(KEY)
    canonical = service.path(KEY)
    old = tmp_path / "repositories" / digest(["github", REPO.path]) / "prs/1/observation.json"
    old.parent.mkdir(parents=True)
    canonical.rename(old)
    service.get_pull_request(KEY)
    assert reader.calls == [1] and canonical.exists()
    reader.rows[1] = replace(reader.rows[1], title="edited after merge")
    service.get_pull_request(KEY, refresh=True)
    assert service._snapshot(KEY).title == "edited after merge"
    reader.rows[1] = replace(reader.rows[1], state="open")
    with pytest.raises(ForgeError):
        service.get_pull_request(KEY, refresh=True)
    assert service.get_pull_request(KEY).state == "merged"
