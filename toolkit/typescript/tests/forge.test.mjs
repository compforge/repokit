import assert from "node:assert/strict";
import { test, describe } from "node:test";
import { GitHubForgeReader, GitLabForgeReader, ForgeRateLimitError } from "../dist/index.js";

const wire = value => JSON.parse(JSON.stringify(value));
function assertFields(actual, expected) {
  assert.deepEqual(Object.fromEntries(Object.keys(expected).map(key => [key, actual[key]])), expected);
}

function json(data, headers) {
    return new Response(JSON.stringify(data), {
        status: 200,
        headers: {
            "content-type": "application/json",
            ...headers,
        },
    });
}
describe("GitHubForgeReader", () => {
    test("backs off after a rate-limit response without issuing more requests", async () => {
        let calls = 0;
        const fetch = async () => {
            calls += 1;
            return new Response(JSON.stringify({ message: "API rate limit exceeded" }), {
                status: 403,
                headers: {
                    "content-type": "application/json",
                    "retry-after": "120",
                    "x-ratelimit-remaining": "0",
                },
            });
        };
        const connector = new GitHubForgeReader({
            forge: "github.com",
            provider: "github",
            host: "github.com",
            token: "secret",
        }, { fetch });
        const query = () => connector.listPullRequests({ scope: {kind:"repository",repository:{forge:{name:"github.com"},path:"owner/repo"}},
            state: "open",
            pageSize: 1,
        });
        await assert.rejects(query(), ForgeRateLimitError);
        await assert.rejects(query(), error => {
            assert.ok(error.retryAfterMs > 0);
            return true;
        });
        assert.equal(calls, 1);
    });
    test("propagates GraphQL rate-limit errors instead of hiding them", async () => {
        const fetch = async (input) => {
            if (String(input).endsWith("/pulls/3")) {
                return json({ number: 3, state: "open", mergeable: true });
            }
            return json({ errors: [{ message: "API rate limit exceeded" }] });
        };
        const connector = new GitHubForgeReader({
            forge: "github.com",
            provider: "github",
            host: "github.com",
            token: "secret",
        }, { fetch });
        await assert.rejects(connector.getPullRequestReview({ repository: { forge: { name: "github.com" }, path: "owner/repo" }, number: 3 }), ForgeRateLimitError);
    });

    test("keeps the observation when review thread API is unavailable", async () => {
        const fetch = async (input) => {
            if (String(input).endsWith("/pulls/3")) {
                return json({
                    number: 3,
                    title: "Already merged",
                    html_url: "https://github.com/owner/repo/pull/3",
                    state: "closed",
                    merged: true,
                    mergeable: null,
                });
            }
            return new Response("forbidden", { status: 403 });
        };
        const connector = new GitHubForgeReader({
            forge: "github.com",
            provider: "github",
            host: "github.com",
            token: "secret",
        }, { fetch });
        assertFields(await connector.getPullRequest({ repository: { forge: { name: "github.com" }, path: "owner/repo" }, number: 3 }), {
            state: "merged",
            conflictState: "unknown",
        });
    });


    test("tracks earlier comment edits across pages and rejects incomplete snapshots", async () => {
        let updatedAt = "2026-07-26T07:00:00Z";
        let resolved = false;
        let deleted = false;
        let failPage = false;
        const pages = [];
        const fetch = async (input) => {
            const url = String(input);
            if (url.endsWith("/pulls/5")) {
                return json({
                    number: 5,
                    title: "Ready to merge",
                    html_url: "https://github.com/owner/repo/pull/5",
                    state: "open",
                    mergeable: true,
                });
            }
            if (url.endsWith("/graphql")) {
                return json({
                    data: {
                        repository: {
                            pullRequest: {
                                reviewThreads: {
                                    nodes: [{
                                            id: "PRRT_1",
                                            isResolved: resolved,
                                            comments: {
                                                nodes: [{
                                                        id: "PRRC_2",
                                                        updatedAt: "2026-07-26T08:00:00Z",
                                                    }],
                                            },
                                        }],
                                    pageInfo: { hasNextPage: false, endCursor: null },
                                },
                            },
                        },
                    },
                });
            }
            if (url.includes("/pulls/5/comments?")) {
                const page = new URL(url).searchParams.get("page");
                pages.push(page);
                if (page === "1") {
                    return json([{ id: 2, updated_at: "2026-07-26T08:00:00Z" }], {
                        link: '<https://api.github.com/repos/owner/repo/pulls/5/comments?per_page=100&page=2>; rel="next"',
                    });
                }
                if (failPage)
                    return new Response("unavailable", { status: 503 });
                return json(deleted ? [] : [{ id: 1, updated_at: updatedAt }]);
            }
            return new Response("not found", { status: 404 });
        };
        const connector = new GitHubForgeReader({
            forge: "github.com",
            provider: "github",
            host: "github.com",
            token: "secret",
        }, { fetch });
        const identity = { repository: { forge: { name: "github.com" }, path: "owner/repo" }, number: 5 };
        const first = await connector.getPullRequestReview(identity);
        updatedAt = "2026-07-26T09:00:00Z";
        const second = await connector.getPullRequestReview(identity);
        assert.equal(typeof first.reviewActivityFingerprint, "string");
        assert.equal(typeof second.reviewActivityFingerprint, "string");
        assert.notEqual(second.reviewActivityFingerprint, first.reviewActivityFingerprint);
        assert.deepEqual(wire(pages), wire(["1", "2", "1", "2"]));
        assert.equal((await connector.getPullRequestReview(identity)).reviewActivityFingerprint, second.reviewActivityFingerprint);
        resolved = true;
        const third = await connector.getPullRequestReview(identity);
        assert.equal(third.reviewThreadState, "resolved");
        assert.notEqual(third.reviewActivityFingerprint, second.reviewActivityFingerprint);
        deleted = true;
        const fourth = await connector.getPullRequestReview(identity);
        assert.equal(typeof fourth.reviewActivityFingerprint, "string");
        assert.notEqual(fourth.reviewActivityFingerprint, third.reviewActivityFingerprint);
        failPage = true;
        await assert.rejects(connector.getPullRequestReview(identity), /503/);
    });

});
describe("GitLabForgeReader", () => {

    test("does not treat ordinary conversation notes as review threads", async () => {
        const fetch = async (input) => {
            const url = String(input);
            if (url.endsWith("/merge_requests/4")) {
                return json({
                    iid: 4,
                    title: "Ordinary conversation",
                    web_url: "https://gitlab.example.com/group/repo/-/merge_requests/4",
                    state: "closed",
                    detailed_merge_status: "unchecked",
                    has_conflicts: false,
                });
            }
            if (url.includes("/discussions?")) {
                return json([{
                        id: "conversation",
                        notes: [{
                                id: 1,
                                resolvable: false,
                                resolved: false,
                            }],
                    }]);
            }
            return new Response("not found", { status: 404 });
        };
        const connector = new GitLabForgeReader({
            forge: "gitlab.example.com",
            provider: "gitlab",
            host: "gitlab.example.com",
            token: "secret",
        }, { fetch });
        assertFields(await connector.getPullRequestReview({ repository: { forge: { name: "gitlab.example.com" }, path: "group/repo" }, number: 4 }), {
            reviewThreadState: "none",
        });
    });



});
export {};
