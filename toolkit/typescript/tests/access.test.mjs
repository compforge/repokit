import assert from "node:assert/strict";
import { test } from "node:test";
import { execFileSync } from "node:child_process";
import { mkdtemp, writeFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { pullRequestContext } from "../dist/index.js";

test("checkout access distinguishes absence, unavailable access and explicit target", async t => {
  const root = await mkdtemp(join(tmpdir(), "repokit-access-"));
  t.after(() => rm(root, { recursive: true, force: true }));
  const git = (...args) => execFileSync("git", ["-C", root, ...args]);
  git("init", "-q");
  assert.equal(await pullRequestContext(root, { home: root }), undefined);
  git("remote", "add", "origin", "https://git.example.com/fork/repo.git");
  await assert.rejects(pullRequestContext(root, { home: root }), /Configure a provider/);
  await writeFile(join(root, "config.json"), JSON.stringify({ forges: {
    "git.example.com": { provider: "gitlab", apiHost: "api.example.com", token: "fixture" }
  } }), { mode: 0o600 });
  const context = await pullRequestContext(root, { home: root, timeoutMs: 2500, targets: ["upstream/repo"] });
  try {
    assert.equal(context.options.source.path, "fork/repo");
    assert.equal(context.options.source.forge.name, "git.example.com");
    assert.deepEqual(context.options.targets.map(target => target.path), ["upstream/repo"]);
    assert.equal(context.options.requestTimeoutMs, 2500);
    assert.equal(context.options.home, root);
  } finally { context.close(); }
  await writeFile(join(root, "config.json"), JSON.stringify({forges:{"git.example.com":{provider:"gitlab"}}}));
  await assert.rejects(pullRequestContext(root, { home: root }), /no gitlab credential/);
});
