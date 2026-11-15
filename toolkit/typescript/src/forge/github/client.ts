import type { Comment } from "../comment.js";
import { readComments } from "./comments.js";
import { ForgeNotFound } from "../model.js";
import type { PullRequestSnapshot, PullRequestConflictState, PullRequestMergeReadiness, PullRequestState } from "@compforge/harness-common";
import { assertPullRequestKey, pullRequestKey } from "../key.js";
import { authorMatches, connectionKey, queryKey, nextPage } from "../query.js";
import type { ForgeReader, Page, PageQuery, PullRequestKey, PullRequestQuery,
  PullRequestScanQuery, PullRequestReviewObservation, ForgeReaderOptions, PullRequestReviewThreadState, ForgeReaderConfig, ForgeReadIssue } from "../model.js";
import { scanPullRequests } from "../scan.js";
import { pageContinuation, pagePosition } from "../page.js";
import { ForgeCoverageError } from "../model.js";
import { readIssue } from "../issues.js";
import { ForgeHttpClient, nonEmptyString, record, records, reviewActivityFingerprint } from "../http.js";

const MAX_REVIEW_THREAD_PAGES = 20;
const MAX_COMMENT_PAGES = 100;


function repositoryPath(repository: string): string {
  const parts = repository.split("/");
  if (parts.length !== 2 || parts.some((part) => !/^[a-zA-Z0-9_.-]+$/.test(part) || part === "." || part === "..")) {
    throw new Error(
      `GitHub repository must be owner/repo, got ${repository}`,
    );
  }
  return parts.map(encodeURIComponent).join("/");
}

function lifecycle(pullRequest: Record<string, unknown>): PullRequestState {
  if (pullRequest.merged === true || pullRequest.merged_at) return "merged";
  if (pullRequest.state === "open") return "open";
  if (pullRequest.state === "closed") return "closed";
  throw new Error(`unknown GitHub PullRequest state: ${pullRequest.state}`);
}

function conflictState(pr: Record<string, unknown>): PullRequestConflictState {
  return pr.mergeable === true ? "none" : pr.mergeable === false ? "conflicted" : "unknown";
}
function mergeReadiness(pr: Record<string, unknown>): PullRequestMergeReadiness {
  if (pr.isDraft === true) return "draft";
  switch (pr.mergeStateStatus) {
    case "CLEAN": return "ready";
    case "DIRTY": return "conflict";
    case "BLOCKED": return "blocked";
    case "BEHIND": return "behind";
    case "UNSTABLE": return "ci_blocked";
    case "DRAFT": return "draft";
    default: return "unknown";
  }
}

export class GitHubForgeReader implements ForgeReader {
  readonly forge: string;
  readonly provider = "github" as const;
  readonly #token: string;
  readonly #restBase: string;
  readonly #graphqlUrl: string;
  readonly #http: ForgeHttpClient;
  readonly #now: () => Date;
  readonly #connection: string;

  constructor(
    config: ForgeReaderConfig,
    options: ForgeReaderOptions = {},
  ) {
    if (config.provider !== "github") {
      throw new Error("GitHubForgeReader requires a github config");
    }
    if (!config.token) throw new Error(`GitHub token missing for ${config.host}`);
    this.forge = config.forge;
    this.#token = config.token;

    const restBase = config.host === "github.com"
      ? "https://api.github.com"
      : `https://${config.host}/api/v3`;
    this.#restBase = restBase;
    this.#graphqlUrl = restBase.endsWith("/api/v3")
      ? `${restBase.slice(0, -"/api/v3".length)}/api/graphql`
      : `${restBase}/graphql`;
    this.#connection = connectionKey("github", this.forge, this.#restBase, config.token);
    this.#http = new ForgeHttpClient(options);
    this.#now = options.now ?? (() => new Date());
  }

  withSignal<T>(signal: AbortSignal, operation: () => Promise<T>): Promise<T> { return this.#http.withSignal(signal, operation); }
  close(): void { this.#http.close(); }

  async listRepositories(query: PageQuery = {}) {
    const position = pagePosition(`${this.#connection}:repositories`, query);
    const parameters = new URLSearchParams({ sort: "full_name", direction: "asc",
      per_page: String(position.pageSize), page: String(position.page) });
    const { data, headers } = await this.#http.request("GET", `${this.#restBase}/user/repos?${parameters}`, { headers: this.#headers() });
    const items = records("GitHub repositories", data).map(row => {
      const path = nonEmptyString("GitHub repository full_name", row.full_name);
      repositoryPath(path);
      return { forge: { name: this.forge }, path };
    });
    if (items.length > position.pageSize) throw new Error("Repository response exceeds requested page size");
    const nextCursor = pageContinuation(position, nextPage(headers, position.page, "github") ?? (items.length >= position.pageSize ? position.page + 1 : undefined));
    return { items, ...(nextCursor ? { nextCursor } : {}) };
  }

  scanPullRequests(query: PullRequestScanQuery) {
    return scanPullRequests(this, query, this.#now(), operation => this.#http.withinDeadline(20_000, operation), this.#connection);
  }

  async listPullRequests(query: PullRequestQuery): Promise<Page<PullRequestKey>> {
    const position = pagePosition(`${this.#connection}:pullRequests:${queryKey(query, this.forge)}`, query);
    if ((position.page - 1) * position.pageSize >= 1000) throw new ForgeCoverageError("GitHub search exceeds the 1000-result window");
    const terms = ["is:pr"];
    if (query.sourceBranch) terms.push(`head:${JSON.stringify(query.sourceBranch)}`);
    if (query.scope.kind === "repository") {
      repositoryPath(query.scope.repository.path);
      terms.push(`repo:${query.scope.repository.path}`);
    }
    if (query.scope.author) terms.push(`author:${query.scope.author}`);
    if (query.updatedAfter || query.updatedBefore) {
      const from = query.updatedAfter ? new Date(query.updatedAfter).toISOString() : undefined;
      const to = query.updatedBefore ? new Date(query.updatedBefore).toISOString() : undefined;
      terms.push(`updated:${from && to ? `${from}..${to}` : from ? `>=${from}` : `<=${to}`}`);
    }
    if (query.state === "open") terms.push("is:open");
    if (query.state === "merged") terms.push("is:merged");
    if (query.state === "closed") terms.push("is:closed", "is:unmerged");
    const { data, headers } = await this.#http.request("GET", `${this.#restBase}/search/issues?${new URLSearchParams({
      q: terms.join(" "), sort: "updated", order: "asc", per_page: String(position.pageSize), page: String(position.page) })}`, { headers: this.#headers() });
    const result = record("GitHub PullRequest search", data);
    if (result.incomplete_results !== false || !Number.isSafeInteger(result.total_count) || Number(result.total_count) < 0 || Number(result.total_count) > 1000) {
      throw new ForgeCoverageError("GitHub PullRequest search incomplete or exceeds 1000 results");
    }
    if (!Array.isArray(result.items) || result.items.length > position.pageSize) throw new Error("Invalid GitHub PullRequest page");
    const items: PullRequestKey[] = [], issues: ForgeReadIssue[] = [];
    for (const [index, value] of result.items.entries()) try {
      const row = record(`GitHub PullRequest row ${index}`, value);
      const repository = nonEmptyString("GitHub repository_url", row.repository_url);
      const prefix = `${this.#restBase}/repos/`;
      if (!repository.startsWith(prefix) || !row.pull_request) throw new Error("Invalid GitHub PullRequest repository");
      const path = repository.slice(prefix.length);
      repositoryPath(path);
      if (query.scope.kind === "repository" && path.toLowerCase() !== query.scope.repository.path.toLowerCase()) throw new Error("GitHub returned another repository");
      if (!authorMatches(row.user, "login", query.scope.author)) throw new Error("GitHub returned another author");
      const key = pullRequestKey(this.forge, query.scope.kind === "repository" ? query.scope.repository.path : path, row.number as number);
      assertPullRequestKey(key, this.forge);
      items.push(key);
    } catch (error) { issues.push(readIssue(error, "list", { scope: query.scope })); }
    const next = nextPage(headers, position.page, "github") ??
      (position.page * position.pageSize < Number(result.total_count) ? position.page + 1 : undefined);
    const nextCursor = pageContinuation(position, next);
    return { items, ...(nextCursor ? { nextCursor } : {}), ...(issues.length ? { issues } : {}) };
  }

  async getPullRequest(key: PullRequestKey): Promise<PullRequestSnapshot> {
    this.#assertPullRequestKey(key);
    const repository = pullRequestKey(this.forge, key.repository.path, key.number).repository;
    const { data } = await this.#http.request(
      "GET",
      `${this.#restBase}/repos/${repositoryPath(key.repository.path)}` +
        `/pulls/${key.number}`,
      { headers: this.#headers() },
    );
    const pullRequest = record("GitHub PullRequest", data);
    const head = pullRequest.head && typeof pullRequest.head === "object" ? pullRequest.head as Record<string, unknown> : {};
    const base = pullRequest.base && typeof pullRequest.base === "object" ? pullRequest.base as Record<string, unknown> : {};
    const source = head.repo && typeof head.repo === "object" ? head.repo as Record<string, unknown> : {};
    return {
      ...(typeof source.full_name === "string" ? { sourceRepository: pullRequestKey(this.forge, source.full_name, key.number).repository } : {}),
      ...(typeof head.ref === "string" ? { sourceBranch: head.ref } : {}),
      ...(typeof base.ref === "string" ? { targetBranch: base.ref } : {}),
      repository,
      number: key.number,
      title: nonEmptyString("GitHub PullRequest.title", pullRequest.title),
      url: nonEmptyString("GitHub PullRequest.html_url", pullRequest.html_url),
      state: lifecycle(pullRequest),
      conflictState: conflictState(pullRequest),
      ...(pullRequest.draft === true ? { mergeReadiness: "draft" as const } : {}),
      ...(typeof (pullRequest.head as Record<string, unknown> | undefined)?.sha === "string" ? { headSha: (pullRequest.head as Record<string, string>).sha } : {}),
      ...(typeof (pullRequest.base as Record<string, unknown> | undefined)?.sha === "string" ? { baseSha: (pullRequest.base as Record<string, string>).sha } : {}),
      ...(typeof pullRequest.updated_at === "string" ? { updatedAt: pullRequest.updated_at } : {}),
      ...(typeof pullRequest.merged_at === "string" ? { mergedAt: pullRequest.merged_at } : {}),
    };
  }

  async getPullRequestReview(key: PullRequestKey): Promise<PullRequestReviewObservation> {
    this.#assertPullRequestKey(key);
    const review = await this.#reviewThreads(key);
    return { key: pullRequestKey(this.forge, key.repository.path, key.number), reviewThreadState: review.state,
      ...(review.activityFingerprint ? { reviewActivityFingerprint: review.activityFingerprint } : {}),
      mergeReadiness: review.readiness,
      observedAt: this.#now().toISOString() };
  }

  async listComments(key: PullRequestKey): Promise<readonly Comment[]> {
    this.#assertPullRequestKey(key);
    const base = `${this.#restBase}/repos/${repositoryPath(key.repository.path)}`;
    return readComments(key, path => this.#commentRows(`${base}/${path}`), async (query, variables) => {
      const { data } = await this.#http.request("POST", this.#graphqlUrl, {
        headers: this.#headers(), body: JSON.stringify({ query, variables }),
      });
      return data;
    });
  }
  async getComment(key: PullRequestKey, id: string): Promise<Comment> {
    const found = (await this.listComments(key)).find(c => c.id === id);
    if (!found) throw new ForgeNotFound("comment not found in the specified PR");
    return found;
  }

  #headers(): HeadersInit {
    return {
      Accept: "application/vnd.github+json",
      Authorization: `Bearer ${this.#token}`,
      "Content-Type": "application/json",
      "X-GitHub-Api-Version": "2022-11-28",
    };
  }

  #assertPullRequestKey(key: PullRequestKey): void {
    assertPullRequestKey(key, this.forge);
    repositoryPath(key.repository.path);
  }

  async #commentRows(
    endpoint: string,
  ): Promise<readonly Record<string, unknown>[]> {
    const result: Record<string, unknown>[] = [];
    for (let page = 1; page <= MAX_COMMENT_PAGES; page += 1) {
      const separator = endpoint.includes("?") ? "&" : "?";
      const { data, headers } = await this.#http.request(
        "GET",
        `${endpoint}${separator}per_page=100&page=${page}`,
        { headers: this.#headers() },
      );
      const batch = records("GitHub comments", data);
      result.push(...batch);
      const hasNextPage = headers.get("link")
        ?.split(",")
        .some((link) => /;\s*rel="next"/.test(link)) ?? false;
      if (!hasNextPage && batch.length < 100) return result;
    }
    throw new Error("GitHub comment pagination limit exceeded");
  }

  /**
   * @spec Review activity includes thread resolution and every comment's id and edit time.
   * @why The newest reply does not reflect edits to earlier comments; REST pagination covers all threads.
   * @case id=review_comment_history,desc=`Edit an older comment on another page`,expect=`Activity fingerprint changes`
   * @see {@link ../../tests/forge.test.mjs}
   */
  async #reviewThreads(
    key: PullRequestKey,
  ): Promise<{
    readonly readiness: PullRequestMergeReadiness;
    readonly state: PullRequestReviewThreadState;
    readonly activityFingerprint?: string;
  }> {
    const [owner, name] = key.repository.path.split("/");
    let cursor: string | undefined;
    const activityTokens: string[] = [];
    let unresolved = false;
    for (let page = 0; page < MAX_REVIEW_THREAD_PAGES; page += 1) {
      const { data } = await this.#http.request(
        "POST",
        this.#graphqlUrl,
        {
          headers: this.#headers(),
          body: JSON.stringify({
            query: `query($owner:String!,$name:String!,$number:Int!,$after:String){
              repository(owner:$owner,name:$name){
                pullRequest(number:$number){
                  isDraft
                  mergeStateStatus
                  reviewThreads(first:100,after:$after){
                    nodes{
                      id
                      isResolved
                    }
                    pageInfo{hasNextPage endCursor}
                  }
                }
              }
            }`,
            variables: {
              owner,
              name,
              number: key.number,
              after: cursor ?? null,
            },
          }),
        },
      );
      const root = record("GitHub GraphQL response", data);
      if (Array.isArray(root.errors) && root.errors.length > 0) {
        const detail = JSON.stringify(root.errors);
        if (/rate.?limit/i.test(detail)) {
          throw this.#http.rateLimit(
            "GitHub GraphQL rate limit exceeded",
          );
        }
        throw new Error("GitHub GraphQL returned errors");
      }
      const graph = record("GitHub GraphQL data", root.data);
      const repository = record("GitHub GraphQL repository", graph.repository);
      const pullRequest = record(
        "GitHub GraphQL PullRequest",
        repository.pullRequest,
      );
      const reviewThreads = record(
        "GitHub GraphQL reviewThreads",
        pullRequest.reviewThreads,
      );
      const nodes = records(
        "GitHub GraphQL reviewThreads.nodes",
        reviewThreads.nodes,
      );
      for (const thread of nodes) {
        if (typeof thread.id !== "string" || !thread.id) {
          throw new Error("GitHub review thread has no id");
        }
        if (typeof thread.isResolved !== "boolean") {
          throw new Error("GitHub review thread has no resolution state");
        }
        unresolved ||= thread.isResolved === false;
        activityTokens.push(JSON.stringify([
          thread.id,
          thread.isResolved,
        ]));
      }
      const pageInfo = record(
        "GitHub GraphQL reviewThreads.pageInfo",
        reviewThreads.pageInfo,
      );
      if (pageInfo.hasNextPage !== true) {
        if (activityTokens.length === 0) return { state: "none", readiness: mergeReadiness(pullRequest) };
        const comments = await this.#commentRows(
          `${this.#restBase}/repos/${repositoryPath(key.repository.path)}/pulls/${key.number}/comments`,
        );
        for (const comment of comments) {
          if (
            (typeof comment.id !== "string" && typeof comment.id !== "number") ||
            !String(comment.id)
          ) {
            throw new Error("GitHub review comment has no id");
          }
          if (
            comment.updated_at !== undefined &&
            typeof comment.updated_at !== "string"
          ) {
            throw new Error("GitHub review comment updated_at is invalid");
          }
          activityTokens.push(JSON.stringify([
            String(comment.id),
            comment.updated_at ?? null,
          ]));
        }
        return {
          readiness: mergeReadiness(pullRequest),
          state: unresolved ? "unresolved" : "resolved",
          activityFingerprint: reviewActivityFingerprint("github", activityTokens),
        };
      }
      if (typeof pageInfo.endCursor !== "string" || !pageInfo.endCursor) {
        throw new Error("GitHub review thread cursor missing");
      }
      cursor = pageInfo.endCursor;
    }
    throw new Error("GitHub review thread pagination limit exceeded");
  }
}
