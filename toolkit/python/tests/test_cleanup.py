"""Shared retention contracts and native cleanup behavior."""

import json
from dataclasses import asdict
from pathlib import Path

import pytest

from repokit.state.cleanup import CacheCleanOptions, clean_comments, clean_pull_requests
from repokit.state.policy import CleanupPolicy, load_cleanup_policy


def put(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text(json.dumps(value))


@pytest.mark.parametrize(
    "row",
    json.loads(
        (Path(__file__).resolve().parents[3] / "conformance/cleanup-policy.json").read_text()
    )["cases"],
    ids=lambda row: row["name"],
)
def test_policy_contract(tmp_path, row):
    put(tmp_path / "config.json", row["home"])
    put(tmp_path / "repositories/fixture/config.json", row["repository"])
    if row.get("invalid"):
        with pytest.raises((OSError, ValueError)):
            load_cleanup_policy(tmp_path, "fixture")
    else:
        assert asdict(load_cleanup_policy(tmp_path, "fixture")) == row["expected"]


def test_comment_collections_and_protected_pr(tmp_path):
    root = tmp_path / "repositories/fixture/prs"
    for n, size in ((1, 3), (2, 2)):
        put(root / str(n) / "access.json", dict(version=1, lastAccessAt=n))
        put(root / str(n) / "a.json", dict(version=1, scopeIds=["watch"] if n == 1 else []))
        put(root / str(n) / "comments/access.json", dict(version=1, lastAccessAt=n))
        for i in range(size):
            for connection in ("a", "b"):
                put(root / str(n) / "comments" / str(i) / (connection + ".json"), dict(version=1))
    (root / "1/comments/empty").mkdir()
    policy = CleanupPolicy(pr=1, comment=3)
    args = dict(home=tmp_path, repository_key="fixture", policy=policy)
    protected = clean_pull_requests(CacheCleanOptions(**args))
    assert protected.counts[0]["after"] == 2
    assert protected.counts[0]["over_budget"] == 1
    preview = clean_comments(CacheCleanOptions(**args, dry_run=True))
    actual = clean_comments(CacheCleanOptions(**args))
    assert preview.counts == actual.counts
    assert actual.counts[0]["before"] == 5 and actual.counts[0]["after"] == 2
    assert preview.actions[0]["estimatedBytes"] == actual.actions[0]["estimatedBytes"]
    assert not (root / "1/comments").exists()
    assert (root / "2/comments").is_dir()
    assert clean_comments(CacheCleanOptions(**args)).actions == []
    assert not list((tmp_path / "locks").glob("*/*.lock"))
