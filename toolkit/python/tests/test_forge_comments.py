"""Comment contract exercises use the same provider facts as Go/TypeScript."""

import copy
import json
import shutil
from pathlib import Path

import pytest

from repokit.forge import CommentAnchor, CommentForge, GitHubForge, GitLabForge, PullRequestForge
from repokit.forge.comment import CommentResolution
from repokit.forge.model import ForgeError, ForgeNotFound, ForgeOutcomeUnknown

FIXTURE = json.loads((Path(__file__).parents[3] / "conformance/forge-comments.json").read_text())


@pytest.fixture(autouse=True)
def isolated_home(tmp_path, monkeypatch):
    monkeypatch.setenv("REPOKIT_HOME", str(tmp_path))


class FakeTransport:
    def __init__(self, provider):
        self.provider = provider
        self.data = copy.deepcopy(FIXTURE[provider])
        self.calls = []
        self.ack = None
        self.fail = False

    def get_all(self, path):
        if self.fail:
            raise ForgeError("later page failed")
        if path.endswith("/versions"):
            return [
                {"head_commit_sha": "head", "base_commit_sha": "base", "start_commit_sha": "start"}
            ]
        if self.provider == "gitlab":
            return self.data["discussions"]
        return self.data[
            "summaries"
            if path.endswith("/reviews")
            else "conversation"
            if path.startswith("issues/")
            else "review"
        ]

    def get(self, path):
        return {"number": 7}

    def post(self, path, body):
        return self.request("POST", path, body=body)

    def put(self, path, body):
        return self.request("PUT", path, body=body)

    def request(self, method, path, *, body=None):
        self.calls.append((method, path, body))
        if path == "":
            if body["query"].lstrip().startswith("query"):
                return {
                    "data": {
                        "repository": {
                            "pullRequest": {
                                "reviewThreads": {
                                    "nodes": self.data["threads"],
                                    "pageInfo": {"hasNextPage": False, "endCursor": None},
                                }
                            }
                        }
                    }
                }
            if self.ack is not None:
                return self.ack
            resolved = "unresolveReviewThread" not in body["query"]
            action = "resolveReviewThread" if resolved else "unresolveReviewThread"
            return {"data": {action: {"thread": {"id": "T1", "isResolved": resolved}}}}
        if self.ack is not None:
            return self.ack
        if method == "DELETE":
            return None
        value = {"id": 8, "body": body["body"], "created_at": "2026-01-02T00:00:00Z"}
        if path.endswith("/replies"):
            value["in_reply_to_id"] = 1
        if path.endswith("/comments/1"):
            value["id"] = 1
        if path.endswith("/notes/2"):
            value["id"] = 2
        if self.provider == "gitlab" and path.endswith("/discussions"):
            return {"id": "new", "individual_note": False, "notes": [value]}
        return value


def client(provider):
    f = (
        GitHubForge("github.com", "acme", "repo", "fixture")
        if provider == "github"
        else GitLabForge("gitlab.com", "acme/repo", "fixture")
    )
    transport = FakeTransport(provider)
    f.c = transport
    if provider == "github":
        f.g = transport
    return f, transport


@pytest.mark.parametrize("provider", ["github", "gitlab"])
def test_flat_inventory_counts_every_message_and_preserves_facts(provider):
    f, _ = client(provider)
    comments = f.list_comments(7)
    assert [
        {"id": c.id, "reply_to_id": c.reply_to_id, "resolution": c.resolution.value}
        for c in comments
    ] == FIXTURE[provider]["expected"]
    assert len(comments) == 4
    assert comments[2].anchor is not None and comments[2].anchor.side is None
    anchored = next(c for c in comments if c.anchor)
    assert anchored.anchor.side == "LEFT"
    if provider == "github":
        assert anchored.anchor.line is None and anchored.anchor.original_line == 12
        assert anchored.anchor.original_revision == "base" and anchored.anchor.outdated
    else:
        assert anchored.anchor.path == "old.go" and anchored.anchor.line == 12
        assert comments[1].reply_to_id == comments[0].id


@pytest.mark.parametrize("provider", ["github", "gitlab"])
def test_crud_returns_confirmed_comments_and_rejects_foreign_ids(provider):
    f, t = client(provider)
    created = f.create_comment(7, "new")
    assert created.body == "new" and created.id.endswith(":8")
    anchor = CommentAnchor("new.go", "head", "LEFT", 12, old_path="old.go")
    f.create_comment(7, "positioned", anchor=anchor)
    payload = t.calls[-1][2]
    assert (payload.get("side") or payload["position"].get("old_line")) in ("LEFT", 12)
    root = "review:1" if provider == "github" else "note:1"
    reply = f.reply_comment(7, root, "answer")
    assert reply.body == "answer"
    assert reply.reply_to_id == root
    target = "conversation:1" if provider == "github" else "note:2"
    assert f.update_comment(7, target, "edited").body == "edited"
    assert f.delete_comment(7, target).deleted
    before = len(t.calls)
    with pytest.raises(ForgeNotFound):
        f.delete_comment(7, "foreign:900")
    assert all(call[0] == "POST" and call[1] == "" for call in t.calls[before:])
    nested = f.reply_comment(7, "review:2" if provider == "github" else "note:2", "nested")
    assert nested.reply_to_id == root
    with pytest.raises(ForgeError):
        f.delete_comment(7, root)


@pytest.mark.parametrize("provider", ["github", "gitlab"])
def test_write_acknowledgement_and_inventory_failure_are_not_success(provider):
    f, t = client(provider)
    t.ack = {}
    with pytest.raises(ForgeOutcomeUnknown):
        f.create_comment(7, "body")
    t.fail = True
    with pytest.raises(ForgeError):
        f.list_comments(7)


def test_resolution_requires_confirmation_and_supports_reopen():
    f, t = client("github")
    assert f.resolve_comment(7, "review:2").resolution == CommentResolution.RESOLVED
    t.data["threads"][0]["isResolved"] = True
    assert f.resolve_comment(7, "review:1", False).resolution == CommentResolution.UNRESOLVED
    t.ack = {"errors": [{"message": "failed"}]}
    with pytest.raises(ForgeOutcomeUnknown):
        f.resolve_comment(7, "review:1", False)
    f, t = client("gitlab")
    t.ack = t.data["discussions"][0]
    with pytest.raises(ForgeOutcomeUnknown):
        f.resolve_comment(7, "note:1")
    t.ack = copy.deepcopy(t.ack)
    for n in t.ack["notes"]:
        n["resolved"] = True
    assert f.resolve_comment(7, "note:1").resolution == CommentResolution.RESOLVED
    with pytest.raises(ForgeError):
        f.resolve_comment(7, "note:5")


def test_github_inconsistent_observations_fail_instead_of_dropping_replies():
    f, t = client("github")
    t.data["threads"].append(t.data["threads"][0])
    with pytest.raises(ForgeError, match="duplicate"):
        f.list_comments(7)
    t.data["threads"].pop()
    t.data["review"][1]["in_reply_to_id"] = 999
    with pytest.raises(ForgeError):
        f.list_comments(7)


# Type check the implementations against independent domain interfaces.
def domain_contracts(github: GitHubForge, gitlab: GitLabForge) -> None:
    comments: list[CommentForge] = [github, gitlab]
    prs: list[PullRequestForge] = [github, gitlab]
    assert comments and prs


@pytest.mark.parametrize("provider", ["github", "gitlab"])
def test_comment_home_rebuild_and_connection_isolation(provider, tmp_path):
    f, transport = client(provider)
    before = f.list_comments(number=7)
    root = tmp_path / "repositories" / f"{provider}--acme--repo" / "prs" / "7" / "comments"
    records = list(root.glob("*/*.json"))
    assert len(records) == 4
    for path in records:
        value = json.loads(path.read_text())
        assert value["version"] == 1 and value["observedAt"]
        assert value["connection"] == path.stem
        assert value["comment"]["pr"] == {
            "repository": {"forge": {"name": provider}, "path": "acme/repo"},
            "number": 7,
        }
        path.write_text("corrupt")
    assert f.list_comments(7) == before
    other, _ = client(provider)
    other._token = "another-connection"
    other.list_comments(7)
    assert len(list(root.glob("*/*.json"))) == 8
    shutil.rmtree(root)
    assert f.list_comments(7) == before
    shutil.rmtree(root)
    assert f.get_comment(number=7, comment_id=before[0].id) == before[0]
    assert len(list(root.glob("*/*.json"))) == 1
    transport.fail = True
    with pytest.raises(ForgeError):
        f.get_comment(7, before[0].id)


def test_comment_write_survives_home_failure(tmp_path, caplog):
    f, transport = client("gitlab")
    (tmp_path / "repositories").write_text("not a directory")
    assert f.create_comment(7, "new").body == "new"
    assert len(transport.calls) == 1
    assert "comment observation publication failed" in caplog.text
