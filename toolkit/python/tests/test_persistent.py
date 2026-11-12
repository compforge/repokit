"""Repository facts live in RepoKit home and preserve source/clone boundaries."""

import json
from unittest.mock import patch

import pytest
from test_operations import run

from repokit.git.state import checkout_info
from repokit.state import branch_creation, remote_branches
from repokit.state.identity import digest_path, name_part
from repokit.state.persistent import persistent_directory
from repokit.state.worktrees import WorktreePolicy, load_policy


def test_creation_provenance_and_policy_share_go_wire_shape(repo, tmp_path):
    common = checkout_info(repo).common_dir
    directory = persistent_directory(repo)
    path = (
        directory / "clones" / digest_path(common) / "branches" / (name_part("topic/a") + ".json")
    )
    path.parent.mkdir(parents=True)
    path.write_text(
        json.dumps(
            {
                "branch": "topic/a",
                "commonDir": common,
                "baseRef": "main",
                "baseCommit": "a" * 40,
                "remote": "origin",
                "createdAt": 12.5,
            }
        )
    )
    creation = branch_creation(repo, "topic/a")
    assert creation and creation.base_ref == "main" and creation.created_at == 12.5
    assert branch_creation(repo, "absent") is None
    path.write_text(path.read_text().replace(common, "/other/clone"))
    with pytest.raises(OSError):
        branch_creation(repo, "topic/a")
    (directory / "config.json").write_text('{"worktree":{"max_count":5,"max_prepared":2}}')
    assert load_policy(repo) == WorktreePolicy(5, 2)
    for document in ("null", '{"worktree":null}', '{"worktree":{"max_count":null}}'):
        (directory / "config.json").write_text(document)
        with pytest.raises(ValueError):
            load_policy(repo)


def test_remote_observation_scope_and_failure_are_distinct(repo):
    from repokit.state import remote as observation

    run(repo, "remote", "add", "origin", "https://github.com/example/one.git")
    with patch.object(
        observation.state, "ls_remote_tips", return_value={"main": "a" * 40}
    ) as query:
        assert remote_branches(repo, ("main",)).tips == (("main", "a" * 40),)
        remote_branches(repo, ("main",))
        assert query.call_count == 1
        remote_branches(repo, ("release",))
        assert query.call_count == 2
        run(repo, "remote", "set-url", "origin", "https://github.com/example/two.git")
        remote_branches(repo, ("main",))
        assert query.call_count == 3
    with patch.object(observation.state, "ls_remote_tips", side_effect=OSError("offline")):
        with pytest.raises(OSError, match="offline"):
            remote_branches(repo, ("main",), max_age=0)
    with patch.object(observation.state, "ls_remote_tips", return_value={}):
        assert remote_branches(repo, ("main",), max_age=0).tips == ()
