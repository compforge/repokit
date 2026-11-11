import json
from pathlib import Path

import pytest

from repokit.config import ConfigError, load_config
from repokit.forge import ForgeConnection
from repokit.forge.model import ForgeAuthError

CASES = json.loads((Path(__file__).parents[3] / "conformance/config/cases.json").read_text())


@pytest.mark.parametrize("case", CASES, ids=lambda case: case["name"])
def test_shared_forge_config_contract(case, tmp_path, monkeypatch):
    for name in ("GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "FIXTURE_FORGE_TOKEN"):
        monkeypatch.delenv(name, raising=False)
    for name, value in case.get("env", {}).items():
        monkeypatch.setenv(name, value)
    if "config" in case:
        path = tmp_path / "config.json"
        path.write_text(json.dumps(case["config"]))
        path.chmod(0o600)
    try:
        config = load_config(tmp_path)
        assert "secret-canary" not in repr(config)
        connection = config.forge(case["host"])
    except (ConfigError, ForgeAuthError) as error:
        assert case.get("error") in str(error).lower()
        assert "secret-canary" not in str(error)
    else:
        assert "error" not in case
        expected = case["expected"]
        assert connection == ForgeConnection(
            host=expected["host"],
            provider=expected["provider"],
            token=expected["token"],
            api_host=expected["apiHost"] or None,
            home=tmp_path,
        )


def test_config_home_permissions_and_snapshot(tmp_path, monkeypatch):
    home = tmp_path / "not-created"
    monkeypatch.setenv("REPOKIT_HOME", str(home))
    assert load_config().home == home
    assert not home.exists()
    path = tmp_path / "config.json"
    path.write_text('{"forges":{"github.com":{"token":"secret-canary"}}}')
    path.chmod(0o600)
    config = load_config(tmp_path)
    assert "secret-canary" not in repr(config)
    path.write_text('{"token":"secret-canary",')
    monkeypatch.delenv("GH_TOKEN", raising=False)
    monkeypatch.delenv("GITHUB_TOKEN", raising=False)
    assert config.forge("github").token == "secret-canary"
    with pytest.raises(ConfigError) as captured:
        load_config(tmp_path)
    assert "secret-canary" not in str(captured.value)
    path.chmod(0o644)
    with pytest.raises(ConfigError, match="chmod 600"):
        load_config(tmp_path)
