import json
from pathlib import Path

import pytest

from repokit.forge import ForgeConnection, detect_provider
from repokit.forge.model import ForgeAuthError


def test_connection_identity_and_context_are_shared_with_typescript(repo, tmp_path):
    """@case Credentials, provider and endpoint isolate caches; targets stay explicit."""
    fixture = Path(__file__).resolve().parents[3] / "conformance/forge-connection.json"
    for row in json.loads(fixture.read_text()):
        connection = ForgeConnection(
            host=row["host"],
            provider=row["provider"],
            api_host=row["apiHost"],
            token=row["token"],
            home=tmp_path,
        )
        assert connection.name == row["name"]
        assert connection.identity == row["identity"]
        assert row["token"] not in repr(connection)
        context = connection.pull_request_context(repo, source="owner/repo")
        assert context.source == connection.repository("owner/repo")
        assert context.targets == (context.source,)
        assert context.observer.store.root == tmp_path
        assert context.observer.reader.home == tmp_path
        assert context.observer.reader.c.state.home.parent == tmp_path / "forge-http"
        explicit = connection.pull_request_context(
            repo, source="owner/repo", targets=("upstream/repo",)
        )
        assert explicit.targets == (connection.repository("upstream/repo"),)
        with pytest.raises(ValueError):
            connection.pull_request_context(repo, source="owner/repo", targets=())


def test_access_configuration_requires_credentials_and_valid_timeout():
    assert detect_provider("github.example") == "github"
    assert detect_provider("forge.example", "github") == "github"
    assert detect_provider("forge.example") == "gitlab"
    with pytest.raises(ForgeAuthError):
        ForgeConnection("github.com", " ")
    with pytest.raises(ValueError):
        ForgeConnection("github.com", "fixture", timeout=0)


@pytest.mark.parametrize("provider", ["github", "gitlab"])
def test_endpoint_override_preserves_identity_across_repository_queries(
    tmp_path, monkeypatch, provider
):
    """@case Logical Forge identity survives endpoint overrides and nested repository clients."""
    from repokit.forge._rest import RestClient

    connection = ForgeConnection(
        "Forge.Example", "fixture", provider=provider, api_host="API.Example", home=tmp_path
    )
    calls = []

    def get(client, path, **params):
        calls.append((client.base_url, path))
        row = {"id": 17, "full_name": "owner/repo", "path_with_namespace": "owner/repo"}
        return [row] if path in {"user/repos", "projects"} else row

    def get_all(client, path, **params):
        calls.append((client.base_url, path))
        return []

    monkeypatch.setattr(RestClient, "get", get)
    monkeypatch.setattr(RestClient, "get_all", get_all)
    client = (
        connection.client("owner/repo").for_repository("upstream/repo").for_repository("owner/repo")
    )
    assert client.repository() == connection.repository("owner/repo")
    assert client.list_repositories().items == (connection.repository("owner/repo"),)
    assert (
        client.prs_for_branch("feature", source_repository=connection.repository("owner/repo"))
        == []
    )
    assert calls and all(url.startswith("https://api.example/api/") for url, _ in calls)
    foreign = ForgeConnection("other.example", "fixture", provider=provider)
    with pytest.raises(ValueError, match="does not belong"):
        client.prs_for_branch("feature", source_repository=foreign.repository("owner/repo"))


def test_mixed_case_public_github_routes_to_public_api(tmp_path, monkeypatch):
    """@case Host case does not select the Enterprise endpoint."""
    from repokit.forge._rest import RestClient

    calls = []

    def get(client, path, **params):
        calls.append(client.base_url)
        return []

    monkeypatch.setattr(RestClient, "get", get)
    connection = ForgeConnection("GitHub.com", "fixture", home=tmp_path)
    connection.client("owner/repo").list_repositories()
    assert calls == ["https://api.github.com"]
    assert connection.identity == ForgeConnection("github.com", "fixture").identity
