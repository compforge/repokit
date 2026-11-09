from unittest.mock import Mock, patch

import pytest
from test_operations import run

from repokit.forge.model import Forge, ForgeError
from repokit.repository import default_branch


def test_default_branch_cache_owns_ttl_and_invalidates_on_origin_change(repo, tmp_path):
    """@case A fresh observation avoids Forge requests; origin changes cannot reuse it."""
    state = tmp_path / "state"
    forge = Mock(spec=Forge)
    forge.default_branch.return_value = "release/stable"
    first = default_branch(repo, forge=forge, home=state)
    assert first.branch == "release/stable" and first.fresh
    assert default_branch(repo, forge=forge, home=state) == first
    forge.default_branch.assert_called_once()
    run(repo, "remote", "add", "origin", "https://example.invalid/other/repo")
    forge.default_branch.return_value = "trunk"
    assert default_branch(repo, forge=forge, home=state).branch == "trunk"
    assert forge.default_branch.call_count == 2


def test_failed_refresh_retains_stale_fact_without_guessing_or_advancing_timestamp(repo, tmp_path):
    state = tmp_path / "state"
    forge = Mock(spec=Forge)
    forge.default_branch.return_value = "release"
    first = default_branch(repo, forge=forge, home=state)
    forge.default_branch.side_effect = ForgeError("offline")
    stale = default_branch(repo, forge=forge, home=state, max_age=0)
    assert stale.branch == "release" and not stale.fresh
    assert stale.observed_at == first.observed_at and stale.diagnostic == "offline"
    run(repo, "symbolic-ref", "--delete", "refs/remotes/origin/HEAD")
    absent = default_branch(repo, home=tmp_path / "empty")
    assert absent.branch is None and not absent.fresh


def test_git_remote_fallback_is_cached_but_query_failures_are_not_absence(repo, tmp_path):
    run(repo, "remote", "add", "origin", "https://example.invalid/repo")
    run(repo, "symbolic-ref", "refs/remotes/origin/HEAD", "refs/remotes/origin/main")
    with patch("repokit.git.state.refresh_remote_head", return_value=True) as refresh:
        result = default_branch(repo, home=tmp_path / "state")
        assert result.fresh and result.branch == "main"
        default_branch(repo, home=tmp_path / "state")
        refresh.assert_called_once()
    with pytest.raises(OSError):
        default_branch(tmp_path / "missing", home=tmp_path / "state")
