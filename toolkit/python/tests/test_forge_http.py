import io
import json
import os
import subprocess
import sys
import time
import urllib.error
from email.message import Message
from unittest.mock import patch

import pytest

from repokit.forge import GitHubForge, GitLabForge
from repokit.forge.model import ForgeAuthError, ForgeError, ForgeOutcomeUnknown, ForgeRateLimited


def headers(**values):
    result = Message()
    for name, value in values.items():
        result[name.replace("_", "-")] = str(value)
    return result


class Response(io.BytesIO):
    def __init__(self, body, **values):
        super().__init__(json.dumps(body).encode())
        self.headers = headers(**values)
        self.status = 200


def error(status, body=None, **values):
    return urllib.error.HTTPError(
        "https://api.github.com",
        status,
        "test",
        headers(**values),
        io.BytesIO(json.dumps(body or {}).encode()),
    )


def test_cooldown_survives_another_process_and_is_credential_scoped(tmp_path):
    retry_at = int(time.time()) + 600
    client = GitHubForge("github.com", "o", "one", "secret", home=tmp_path)
    with patch(
        "urllib.request.urlopen",
        side_effect=error(
            403, x_ratelimit_remaining=0, x_ratelimit_reset=retry_at, x_ratelimit_resource="core"
        ),
    ):
        with pytest.raises(ForgeRateLimited) as caught:
            client.get(1)
    assert caught.value.retry_at == retry_at
    script = """
import sys
from unittest.mock import patch
from repokit.forge import GitHubForge
from repokit.forge.model import ForgeRateLimited
with patch('urllib.request.urlopen', side_effect=AssertionError('network during cooldown')):
    try:
        GitHubForge('github.com', 'o', 'two', 'secret', home=sys.argv[1]).get(2)
    except ForgeRateLimited:
        pass
    else:
        raise AssertionError('cooldown not shared')
"""
    subprocess.run([sys.executable, "-c", script, str(tmp_path)], check=True, env=os.environ)
    other = GitHubForge("github.com", "o", "one", "different", home=tmp_path)
    with patch("urllib.request.urlopen", return_value=Response({"number": 1, "state": "open"})):
        assert other.get(1).is_open


def test_permission_403_is_not_a_rate_limit(tmp_path):
    client = GitHubForge("github.com", "o", "r", "secret", home=tmp_path)
    with patch("urllib.request.urlopen", side_effect=error(403, {"message": "forbidden"})):
        with pytest.raises(ForgeAuthError):
            client.get(1)


def test_dispatch_lock_deadline_precedes_any_mutation(tmp_path):
    import fcntl

    client = GitHubForge("github.com", "o", "r", "secret", home=tmp_path, timeout=0.1)
    state = client.c.state
    state.home.mkdir(parents=True)
    with (state.home / "request.lock").open("w") as held:
        fcntl.flock(held, fcntl.LOCK_EX | fcntl.LOCK_NB)
        with patch("urllib.request.urlopen", side_effect=AssertionError("unexpected dispatch")):
            with pytest.raises(ForgeError) as caught:
                client.c.post("issues/1/comments", {"body": "comment"})
    assert not isinstance(caught.value, ForgeOutcomeUnknown)
    assert client.request_counts()["requests"] == 0


def test_last_success_remains_usable_and_cools_only_its_primary_bucket(tmp_path):
    client = GitHubForge("github.com", "o", "r", "secret", home=tmp_path)
    with patch(
        "urllib.request.urlopen",
        return_value=Response(
            {"number": 1, "state": "open"},
            x_ratelimit_remaining=0,
            x_ratelimit_reset=int(time.time()) + 600,
            x_ratelimit_resource="core",
        ),
    ):
        assert client.get(1).is_open
    with patch("urllib.request.urlopen", return_value=Response({"data": {}})) as request:
        with pytest.raises(ForgeRateLimited):
            client.get(2)
        assert client.g.post("", {"query": "query { viewer { login } }"}) == {"data": {}}
        request.assert_called_once()


def test_expired_cooldown_allows_a_new_attempt(tmp_path):
    client = GitHubForge("github.com", "o", "r", "secret", home=tmp_path)
    with patch("repokit.forge._rest.time.time", return_value=1000):
        with patch("urllib.request.urlopen", side_effect=error(429)):
            with pytest.raises(ForgeRateLimited):
                client.get(1)
    with patch("repokit.forge._rest.time.time", return_value=1061):
        with patch("urllib.request.urlopen", return_value=Response({"number": 1, "state": "open"})):
            assert client.get(1).is_open


def test_secondary_limit_blocks_rest_and_graphql(tmp_path):
    client = GitHubForge("github.com", "o", "r", "secret", home=tmp_path)
    with patch("urllib.request.urlopen", side_effect=error(429, retry_after=120)) as request:
        with pytest.raises(ForgeRateLimited) as caught:
            client.get(1)
        assert caught.value.secondary
        assert caught.value.retry_at > time.time() + 115
        with pytest.raises(ForgeRateLimited):
            client.g.post("", {"query": "query { viewer { login } }"})
        assert request.call_count == 1


def test_graphql_200_rate_error_is_structured(tmp_path):
    client = GitHubForge("github.com", "o", "r", "secret", home=tmp_path)
    with patch(
        "urllib.request.urlopen",
        return_value=Response(
            {"errors": [{"type": "RATE_LIMITED", "message": "API rate limit exceeded"}]}
        ),
    ):
        with pytest.raises(ForgeRateLimited) as caught:
            client.g.post("", {"query": "query { viewer { login } }"})
    assert caught.value.resource == "graphql"


def test_conditional_pages_preserve_bodies_across_clients(tmp_path):
    first = GitHubForge("github.com", "o", "r", "secret", home=tmp_path)
    with patch(
        "urllib.request.urlopen",
        side_effect=[Response([1, 2], ETag='"p1"'), Response([3], ETag='"p2"')],
    ):
        assert first.c.get_all("comments", per_page=2) == [1, 2, 3]
    second = GitHubForge("github.com", "o", "r", "secret", home=tmp_path)
    requests = []

    def unchanged(req, **kwargs):
        requests.append(req)
        raise error(304)

    with patch("urllib.request.urlopen", side_effect=unchanged):
        assert second.c.get_all("comments", per_page=2) == [1, 2, 3]
    assert [r.get_header("If-none-match") for r in requests] == ['"p1"', '"p2"']
    assert second.request_counts() == {"requests": 2, "not_modified": 2}
    # An earlier-page edit must be observed; conditional responses do not skip pages.
    with patch("urllib.request.urlopen", side_effect=[Response([9, 2], ETag='"new"'), error(304)]):
        assert second.c.get_all("comments", per_page=2) == [9, 2, 3]


def test_cached_first_page_does_not_hide_later_page_failure(tmp_path):
    client = GitHubForge("github.com", "o", "r", "secret", home=tmp_path)
    with patch("urllib.request.urlopen", side_effect=[Response([1, 2], ETag='"p1"'), error(500)]):
        with pytest.raises(ForgeError):
            client.c.get_all("comments", per_page=2)


def test_open_inventory_excludes_foreign_source_and_fetches_all_pages():
    gh = GitHubForge("github.com", "o", "r", "secret")
    rows = [
        {"number": i, "state": "open", "head": {"repo": {"full_name": "o/r"}}} for i in range(101)
    ]
    rows.append({"number": 999, "head": {"repo": {"full_name": "other/r"}}})
    with patch.object(gh.c, "get", side_effect=[rows[:100], rows[100:]]) as read:
        assert len(gh.open_pull_requests()) == 101
        assert read.call_count == 2
    gl = GitLabForge("gitlab.com", "o/r", "secret")
    with (
        patch.object(gl.c, "get", return_value={"id": 7, "path_with_namespace": "o/r"}),
        patch.object(
            gl.c,
            "get_all",
            return_value=[
                {"iid": 1, "state": "opened", "source_project_id": 7},
                {"iid": 2, "state": "opened", "source_project_id": 8},
            ],
        ),
    ):
        assert [pr.number for pr in gl.open_pull_requests()] == [1]
