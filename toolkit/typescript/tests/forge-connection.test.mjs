import test from "node:test";
import assert from "node:assert/strict";
import { readFileSync, mkdtempSync, rmSync } from "node:fs";
import { execFileSync } from "node:child_process";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { ForgeConnection, detectProvider } from "../dist/index.js";

const rows = JSON.parse(readFileSync(new URL("../../../conformance/forge-connection.json", import.meta.url), "utf8"));
test("connection identity matches Python and keeps target selection explicit", () => {
  const checkout = mkdtempSync(join(tmpdir(), "repokit-connection-"));
  execFileSync("git", ["init", "-q", checkout]);
  try {
    for (const row of rows) {
      const connection = new ForgeConnection({ ...row, home: checkout });
      assert.equal(connection.name, row.name);
      assert.equal(connection.identity, row.identity);
      assert.ok(!JSON.stringify(connection).includes(row.token));
      const context = connection.pullRequestContext(checkout, { source: "owner/repo" });
      try {
        assert.deepEqual(context.options.targets, [connection.repository("owner/repo")]);
        assert.equal(context.options.home, checkout);
        assert.equal(context.options.connection, row.identity);
      } finally { context.close(); }
      const explicit = connection.pullRequestContext(checkout, { source: "owner/repo", targets: ["upstream/repo"] });
      try { assert.deepEqual(explicit.options.targets, [connection.repository("upstream/repo")]); }
      finally { explicit.close(); }
      assert.throws(() => connection.pullRequestContext(checkout, { source: "owner/repo", targets: [] }));
    }
  } finally { rmSync(checkout, { recursive: true, force: true }); }
});
test("access requires credentials and a bounded timeout", () => {
  assert.equal(detectProvider("github.example"), "github");
  assert.equal(detectProvider("forge.example", "github"), "github");
  assert.throws(() => new ForgeConnection({ host: "github.com", token: " " }), /no credential/);
  assert.throws(() => new ForgeConnection({ host: "github.com", token: "fixture", timeoutMs: 0 }));
});


test("mixed-case hosts preserve public routing and logical identity with API overrides", async () => {
  for (const [host, provider, apiHost, endpoint] of [
    ["GitHub.com", "github", undefined, "https://api.github.com/user/repos"],
    ["Forge.Example", "github", "API.Example", "https://api.example/api/v3/user/repos"],
    ["Forge.Example", "gitlab", "API.Example", "https://api.example/api/v4/projects"],
  ]) {
    const connection = new ForgeConnection({ host, provider, apiHost, token: "fixture" });
    const urls = [];
    const reader = connection.reader({ fetch: async input => {
      urls.push(String(input).split("?")[0]);
      return new Response(JSON.stringify([{
        id: 17, full_name: "owner/repo", path_with_namespace: "owner/repo",
      }]), { status: 200, headers: { "Content-Type": "application/json" } });
    } });
    const page = await reader.listRepositories();
    assert.deepEqual(urls, [endpoint]);
    assert.deepEqual(page.items[0], connection.repository("owner/repo"));
  }
});
