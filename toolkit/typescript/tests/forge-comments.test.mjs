import assert from "node:assert/strict";
import test from "node:test";
import { readFileSync } from "node:fs";
import { mkdtemp, readdir, readFile, rm, writeFile } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { GitHubForgeReader, GitLabForgeReader, ForgeNotFound } from "../dist/index.js";
const fixtures = JSON.parse(readFileSync(new URL("../../../conformance/forge-comments.json", import.meta.url), "utf8"));
const json = value => new Response(JSON.stringify(value));
for (const provider of ["github", "gitlab"]) {
  test(`${provider} flat Comment contract preserves every message and platform facts`, async t => {
    const home = await mkdtemp(join(tmpdir(), "repokit-comments-"));
    t.after(() => rm(home, { recursive: true, force: true }));
    const data = structuredClone(fixtures[provider]);
    const key = { repository: { forge: { name: provider }, path: "acme/repo" }, number: 7 };
    const fetch = async (input) => {
      const url = String(input);
      if (url.endsWith("/graphql"))
        return json({ data: { repository: { pullRequest: { reviewThreads: { nodes: data.threads, pageInfo: { hasNextPage: false, endCursor: null } } } } } });
      if (provider === "gitlab")
        return json(data.discussions);
      return json(url.includes("/issues/") ? data.conversation : url.includes("/reviews") ? data.summaries : data.review);
    };
    const Client = provider === "github" ? GitHubForgeReader : GitLabForgeReader;
    const reader = new Client({
      forge: provider, provider, host: `${provider}.com`, token: "fixture"
    }, { fetch, home });
    const comments = await reader.listComments(key);
    assert.deepEqual(comments.map(c => ({
      id: c.id, reply_to_id: c.replyToId ?? null, resolution: c.resolution
    })), data.expected);
    assert.equal(comments.length, 4);
    const root = join(home, "repositories", `${provider}--acme--repo`, "prs", "7", "comments");
    const directories = (await readdir(root,{withFileTypes:true})).filter(entry=>entry.isDirectory()).map(entry=>entry.name);
    assert.equal(directories.length, 4);
    for (const directory of directories) {
      const path = join(root, directory, (await readdir(join(root, directory)))[0]);
      const record = JSON.parse(await readFile(path, "utf8"));
      assert.equal(record.version, 1);
      assert.deepEqual(record.comment.pr, key);
      assert(record.observedAt);
      await writeFile(path, "corrupt");
    }
    assert.deepEqual(await reader.listComments(key), comments);
    const other = new Client({ forge: provider, provider, host: `${provider}.com`, token: "another" }, { fetch, home });
    await other.listComments(key);
    assert.equal((await readdir(join(root, directories[0]))).length, 2);
    await rm(root, { recursive: true });
    assert.deepEqual(await reader.listComments(key), comments);
    assert.equal(comments[2].anchor.side, undefined);
    const anchored = comments.find(c => c.anchor);
    assert.equal(anchored.anchor.side, "LEFT");
    if (provider === "github") {
      assert.equal(anchored.anchor.line, undefined);
      assert.equal(anchored.anchor.originalLine, 12);
      assert.equal(anchored.anchor.outdated, true);
      data.threads.push(data.threads[0]);
      await assert.rejects(reader.listComments(key), /Duplicate/);
      data.threads.pop();
      data.review[1].in_reply_to_id = 999;
      await assert.rejects(reader.listComments(key), /parent|mismatch/);
    }
    else {
      assert.equal(anchored.anchor.path, "old.go");
      assert.equal(anchored.anchor.line, 12);
      assert.equal(comments[1].replyToId, comments[0].id);
      await assert.rejects(reader.getComment(key, "foreign"), ForgeNotFound);
    }
  });
}
test("GitHub comment pagination cannot turn a later-page failure into success", async t => {
  const home = await mkdtemp(join(tmpdir(), "repokit-comments-"));
  t.after(() => rm(home, { recursive: true, force: true }));
  const reader = new GitHubForgeReader({
    forge: "github", provider: "github", host: "github.com", token: "fixture"
  }, { home, fetch: async (input) => {
      const url = String(input);
      if (url.endsWith("/graphql"))
        return json({ data: { repository: { pullRequest: { reviewThreads: { nodes: [], pageInfo: { hasNextPage: false } } } } } });
      if (url.includes("page=2"))
        return new Response("failed", { status: 503 });
      return json(Array.from({ length: 100 }, (_, i) => ({ id: i + 1, body: "comment" })));
    } });
  await assert.rejects(reader.listComments({ repository: { forge: { name: "github" }, path: "acme/repo" }, number: 7 }), /503/);
});
