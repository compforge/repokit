import json
from pathlib import Path

from repokit import resolve_home
from repokit.forge import ForgeConnection
from repokit.state import RepositoryState


def test_shared_home_precedence(tmp_path, monkeypatch):
    user = tmp_path / "user"
    monkeypatch.setenv("HOME", str(user))
    monkeypatch.chdir(tmp_path)
    cases = json.loads((Path(__file__).parents[3] / "conformance/home.json").read_text())

    def expand(value):
        return (
            value.replace("USER", str(user)).replace("CWD", str(tmp_path))
            if value is not None
            else None
        )

    for case in cases:
        if case["environment"] is None:
            monkeypatch.delenv("REPOKIT_HOME", raising=False)
        else:
            monkeypatch.setenv("REPOKIT_HOME", expand(case["environment"]))
        expected = Path(expand(case["expected"]))
        assert resolve_home(expand(case["explicit"])) == expected
        assert not expected.exists()
    monkeypatch.setenv("REPOKIT_HOME", str(tmp_path / "shared"))
    assert RepositoryState().store.root == ForgeConnection("github.com", "fixture").home
