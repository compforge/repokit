import json
import subprocess

import pytest

from repokit import forge_client, pull_request_context, pull_request_identity
from repokit.forge.model import ForgeAuthError, ForgeError
from repokit.git import GitResult


def checkout(root, origin=None):
    subprocess.run(["git", "init", "-q", str(root)], check=True)
    if origin:
        subprocess.run(["git", "-C", str(root), "remote", "add", "origin", origin], check=True)
    return root


def test_absent_origin_and_unavailable_access_are_distinct(tmp_path):
    repo = checkout(tmp_path / "repo")
    home = tmp_path / "unused"
    assert forge_client(repo, home=home) is None
    assert pull_request_context(repo, home=home) is None
    assert not home.exists()
    subprocess.run(
        ["git", "-C", str(repo), "remote", "add", "origin", "https://github.com/owner/repo"],
        check=True,
    )
    with pytest.raises(ForgeAuthError):
        forge_client(repo, home=home)
    with pytest.raises(ForgeAuthError):
        pull_request_context(repo, home=home)
    assert pull_request_identity(repo, 9, target_repository="upstream/repo").to_dict() == {
        "source": "github.com",
        "repository": "upstream/repo",
        "number": 9,
    }


def test_native_access_uses_private_host_home_timeout_and_explicit_targets(tmp_path):
    repo = checkout(tmp_path / "repo", "https://git.example.com/fork/repo.git")
    path = tmp_path / "config.json"
    path.write_text(
        json.dumps(
            {
                "forges": {
                    "git.example.com": {
                        "provider": "gitlab",
                        "apiHost": "api.example.com",
                        "token": "fixture",
                    }
                }
            }
        )
    )
    path.chmod(0o600)
    client = forge_client(repo, home=tmp_path, timeout=3, target_repository="upstream/repo")
    assert (client.host, client.path, client.timeout, client.home) == (
        "api.example.com",
        "upstream/repo",
        3,
        tmp_path,
    )
    context = pull_request_context(repo, home=tmp_path, timeout=3, targets=("upstream/repo",))
    try:
        assert context.source.path == "fork/repo"
        assert [target.path for target in context.targets] == ["upstream/repo"]
        assert context.observer.reader.timeout == 3
    finally:
        context.observer.close()
    path.write_text("invalid")
    with pytest.raises(ForgeError, match="Invalid"):
        forge_client(repo, home=tmp_path)


def test_git_result_detail_preserves_uncertain_effect():
    assert GitResult(1, "out", "err").detail == "err"
    assert GitResult(1, "out", "").detail == "out"
    assert "outcome unknown" in GitResult(-1, "", "timeout", uncertain=True).detail
