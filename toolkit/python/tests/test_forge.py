from repokit.forge import GitHubForge, GitLabForge
from repokit.forge._rest import RestClient
from repokit.forge.model import parse_pr_number


def test_rest_get_all_pages_until_short_batch():
    """List endpoints must not silently truncate at the first page: review→label uses the
    forge as its durable source of truth, and two review rounds can already exceed 50 comments."""
    c = RestClient("https://example.invalid", {})
    calls = []

    def get(path, **params):
        calls.append((path, params))
        return list(range(100)) if params["page"] == 1 else [100]

    c.get = get
    assert len(c.get_all("comments")) == 101
    assert calls == [
        ("comments", {"page": 1, "per_page": 100}),
        ("comments", {"page": 2, "per_page": 100}),
    ]


def test_parse_pr_number():
    assert parse_pr_number("https://github.com/o/r/pull/12") == 12
    assert parse_pr_number("https://gitlab.com/g/p/-/merge_requests/7") == 7
    assert parse_pr_number("#5") == 5 and parse_pr_number("!9") == 9 and parse_pr_number("42") == 42
    assert parse_pr_number("nope") is None


def test_forge_pr_mapping():
    """Each adapter normalizes its native JSON → the neutral PullRequest: iid/number,
    GitHub's open/closed + merged flag → open|merged|closed, head/base → source/target.
    No provider on the PR — that's repo-level."""
    gl = GitLabForge.__new__(GitLabForge)  # bypass __init__ (no HTTP needed for mapping)
    pr = gl._to_pr(
        {
            "iid": 7,
            "state": "opened",
            "source_branch": "f",
            "target_branch": "m",
            "web_url": "u",
            "sha": "abc",
        }
    )
    assert (pr.number, pr.state, pr.source_branch, pr.sha) == (7, "open", "f", "abc")
    assert not hasattr(pr, "provider")
    assert gl._to_pr({"iid": 8, "state": "merged"}).state == "merged"
    assert gl._to_pr({"iid": 9, "state": "locked"}).state == "closed"

    gh = GitHubForge.__new__(GitHubForge)
    gh.owner, gh.name = "o", "r"
    pr = gh._to_pr(
        {
            "number": 12,
            "state": "open",
            "head": {"ref": "f", "sha": "abc"},
            "base": {"ref": "main"},
            "html_url": "u",
        }
    )
    assert (pr.number, pr.state, pr.source_branch, pr.target_branch) == (12, "open", "f", "main")
    # closed + merged_at → merged; closed without merge → closed
    assert gh._to_pr({"number": 13, "state": "closed", "merged_at": "2026-01-01"}).state == "merged"
    assert gh._to_pr({"number": 14, "state": "closed"}).state == "closed"
    assert gh._to_pr({"number": 15, "state": "closed", "merged": True}).state == "merged"


def test_forge_merge_readiness_mapping():
    """GitLab detailed_merge_status → neutral MergeReadiness, with a has_conflicts fallback and
    UNKNOWN for the async 'checking' window (UNKNOWN must never collapse to READY/CONFLICT).
    GitHub inherits the safe UNKNOWN default since it's not implemented yet."""
    from repokit.forge.model import MergeReadiness

    r = GitLabForge._readiness
    assert r({"detailed_merge_status": "mergeable"}) is MergeReadiness.READY
    assert r({"detailed_merge_status": "conflict"}) is MergeReadiness.CONFLICT
    assert (
        r({"detailed_merge_status": "discussions_not_resolved"})
        is MergeReadiness.DISCUSSIONS_UNRESOLVED
    )
    assert r({"detailed_merge_status": "ci_still_running"}) is MergeReadiness.CI_BLOCKED
    assert (
        r({"detailed_merge_status": "checking"}) is MergeReadiness.UNKNOWN
    )  # async window, not a verdict
    assert r({}) is MergeReadiness.UNKNOWN
    assert r({"has_conflicts": True}) is MergeReadiness.CONFLICT  # fallback when no detailed status
    # GitHub adapter hasn't implemented it → inherits the safe UNKNOWN default (no HTTP)
    assert GitHubForge.__new__(GitHubForge).merge_readiness(0) is MergeReadiness.UNKNOWN


def test_forge_release_endpoints():
    """create_release / latest_release hit the right endpoint with the provider's field names
    and map back to the neutral Release: github POST/GET releases (`target_commitish`/`body`,
    `/releases/latest` with 404→None), gitlab POST/GET releases (`ref`/`description`, list→first).
    """
    from repokit.forge.github import GitHubForge
    from repokit.forge.gitlab import GitLabForge
    from repokit.forge.model import ForgeNotFound

    class _Cap:
        def __init__(self, get_resp=None, get_exc=None):
            self.calls = []
            self._get, self._get_exc = get_resp, get_exc
            self.gets = []

        def get(self, path, **kw):
            self.gets.append((path, kw))
            if self._get_exc:
                raise self._get_exc
            return self._get

        def post(self, path, body):
            self.calls.append((path, body))
            return {
                "tag_name": body.get("tag_name"),
                "name": body.get("name"),
                "html_url": f"gh/{body.get('tag_name')}",
                "target_commitish": body.get("target_commitish"),
                "_links": {"self": f"gl/{body.get('tag_name')}"},
                "commit": {"id": "deadbeef"},
                "published_at": "2026-07-05",
            }

    gh = GitHubForge("api.github.com", "o", "r", "t")
    gh.c = _Cap()
    rel = gh.create_release(tag="v1.8.0", target="main", notes="hi")
    assert gh.c.calls[0] == (
        "releases",
        {"tag_name": "v1.8.0", "target_commitish": "main", "name": "v1.8.0", "body": "hi"},
    )
    assert (rel.tag, rel.target, rel.web_url) == ("v1.8.0", "main", "gh/v1.8.0")

    gl = GitLabForge("h", "o/r", "t")
    gl.c = _Cap()
    rel = gl.create_release(tag="v1.8.0", target="release", name="R", notes="hi")
    assert gl.c.calls[0] == (
        "releases",
        {"tag_name": "v1.8.0", "ref": "release", "name": "R", "description": "hi"},
    )
    assert (rel.tag, rel.name, rel.target, rel.web_url) == ("v1.8.0", "R", "deadbeef", "gl/v1.8.0")

    # latest: github 404 → None (first release); a hit → mapped
    gh2 = GitHubForge("api.github.com", "o", "r", "t")
    gh2.c = _Cap(get_exc=ForgeNotFound("404"))
    assert gh2.latest_release() is None and gh2.c.gets[0][0] == "releases/latest"
    gh3 = GitHubForge("api.github.com", "o", "r", "t")
    gh3.c = _Cap(get_resp={"tag_name": "v1.7.2", "html_url": "u"})
    assert gh3.latest_release().tag == "v1.7.2"
    # gitlab: list newest-first → first; empty list → None
    gl2 = GitLabForge("h", "o/r", "t")
    gl2.c = _Cap(get_resp=[{"tag_name": "v1.7.2"}])
    assert gl2.latest_release().tag == "v1.7.2" and gl2.c.gets[0] == ("releases", {"per_page": 1})
    gl3 = GitLabForge("h", "o/r", "t")
    gl3.c = _Cap(get_resp=[])
    assert gl3.latest_release() is None


def test_forge_default_branch():
    """default_branch() 读 repo 根对象的 default_branch（gitlab GET /projects/{id}、
    github GET /repos/{o}/{n}，路径为 ""）；缺字段 → ""。"""
    from repokit.forge.github import GitHubForge
    from repokit.forge.gitlab import GitLabForge

    class _C:
        def __init__(self, d):
            self.d, self.paths = d, []

        def get(self, path):
            self.paths.append(path)
            return self.d

    gl = GitLabForge("h", "o/r", "t")
    gl.c = _C({"default_branch": "release"})
    assert gl.default_branch() == "release" and gl.c.paths == [""]  # repo root
    gh = GitHubForge("api.github.com", "o", "r", "t")
    gh.c = _C({"default_branch": "main"})
    assert gh.default_branch() == "main"
    gl2 = GitLabForge("h", "o/r", "t")
    gl2.c = _C({})
    assert gl2.default_branch() == ""  # missing field → empty


def test_merge_requires_reviewed_tip_and_maps_endpoints():
    from unittest.mock import Mock

    import pytest

    for cls, number_field, endpoint in (
        (GitHubForge, "number", "pulls"),
        (GitLabForge, "iid", "merge_requests"),
    ):
        client = cls.__new__(cls)
        client.c = Mock()
        client.c.put.return_value = {number_field: 7, "merged": True, "state": "merged"}
        client.c.get.return_value = {number_field: 7, "merged": True, "state": "closed"}
        with pytest.raises(ValueError):
            client.merge(7, expected_sha="")
        assert client.merge(7, expected_sha="reviewed").state == "merged"
        client.c.put.assert_called_once_with(f"{endpoint}/7/merge", {"sha": "reviewed"})


def test_invalid_later_page_does_not_return_partial_inventory():
    from unittest.mock import patch

    import pytest

    from repokit.forge.model import ForgeError

    client = RestClient("https://example.invalid", {})
    with patch.object(client, "get", side_effect=[list(range(100)), {"unexpected": "object"}]):
        with pytest.raises(ForgeError, match="page 2"):
            client.get_all("pulls")
