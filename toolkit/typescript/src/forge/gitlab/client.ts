import { CommentState } from "../comment-state.js";
import type { Comment } from "../comment.js";
import { readComments } from "./comments.js";
import type { PullRequestSnapshot, PullRequestConflictState, PullRequestMergeReadiness, PullRequestState } from "@compforge/harness-common";
import { assertPullRequestKey, pullRequestKey } from "../key.js";
import { authorMatches, connectionKey, queryKey, nextPage } from "../query.js";
import type { ForgeReader, Page, PageQuery, PullRequestKey, PullRequestQuery,
  PullRequestScanQuery, PullRequestReviewObservation, ForgeReaderOptions, PullRequestReviewThreadState, ForgeReaderConfig, ForgeReadIssue } from "../model.js";
import { scanPullRequests } from "../scan.js";
import { pageContinuation, pagePosition } from "../page.js";
import { ForgeCoverageError, ForgeNotFound } from "../model.js";
import { readIssue } from "../issues.js";
import { ForgeHttpClient, nonEmptyString, record, records, reviewActivityFingerprint } from "../http.js";

const MAX_DISCUSSION_PAGES = 20;


function lifecycle(mergeRequest: Record<string, unknown>): PullRequestState {
  switch (mergeRequest.state) {
    case "opened":
    case "open":
      return "open";
    case "merged":
      return "merged";
    case "closed":
    case "locked":
      return "closed";
    default:
      throw new Error(
        `unknown GitLab MergeRequest state: ${mergeRequest.state}`,
      );
  }
}

function conflictState(pr: Record<string, unknown>): PullRequestConflictState {
  if (pr.has_conflicts === true || pr.detailed_merge_status === "conflict") return "conflicted";
  if (pr.merge_status === "can_be_merged" || pr.detailed_merge_status === "mergeable") return "none";
  return "unknown";
}
function mergeReadiness(pr: Record<string, unknown>): PullRequestMergeReadiness {
  if (pr.draft === true || pr.work_in_progress === true) return "draft";
  switch (pr.detailed_merge_status) {
    case "mergeable": return "ready";
    case "conflict": return "conflict";
    case "discussions_not_resolved": return "discussions_unresolved";
    case "ci_must_pass": case "ci_still_running": return "ci_blocked";
    case "not_approved": return "needs_approval";
    case "draft_status": return "draft";
    case "need_rebase": return "behind";
    case "blocked_status": case "requested_changes": case "external_status_checks":
    case "locked_paths": case "locked_lfs_files": case "security_policy_pipeline_check":
    case "security_policies_violations": case "jira_association": case "title_not_matching":
    case "merge_time": case "not_open": return "blocked";
    default: return pr.has_conflicts === true ? "conflict" : "unknown";
  }
}

function encodedProject(repository: string): string {
  if (!repository.trim()) {
    throw new Error("GitLab repository must not be empty");
  }
  return encodeURIComponent(repository);
}

export class GitLabForgeReader implements ForgeReader {
  readonly forge: string;
  readonly provider = "gitlab" as const;
  readonly #token: string;
  readonly #apiBase: string;
  readonly #http: ForgeHttpClient;
  readonly #now: () => Date;
  readonly #connection: string;
  readonly #comments: CommentState;

  constructor(
    config: ForgeReaderConfig,
    options: ForgeReaderOptions = {},
  ) {
    if (config.provider !== "gitlab") {
      throw new Error("GitLabForgeReader requires a gitlab config");
    }
    if (!config.token) throw new Error(`GitLab token missing for ${config.host}`);
    this.forge = config.forge;
    this.#comments = new CommentState(config, options.home);
    this.#token = config.token;

    this.#apiBase = `https://${config.host}/api/v4`;
    this.#connection = connectionKey("gitlab", this.forge, this.#apiBase, config.token);
    this.#http = new ForgeHttpClient(options);
    this.#now = options.now ?? (() => new Date());
  }

  withSignal<T>(signal: AbortSignal, operation: () => Promise<T>): Promise<T> { return this.#http.withSignal(signal, operation); }
  close(): void { this.#http.close(); }

  async listRepositories(query: PageQuery = {}) {
    const position = pagePosition(`${this.#connection}:repositories`, query);
    const parameters = new URLSearchParams({ membership: "true", simple: "true", order_by: "id", sort: "asc",
      per_page: String(position.pageSize), page: String(position.page) });
    const { data, headers } = await this.#http.request("GET", `${this.#apiBase}/projects?${parameters}`, { headers: this.#headers() });
    const items = records("GitLab repositories", data).map(row => {
      const path = nonEmptyString("GitLab repository path_with_namespace", row.path_with_namespace);
      if (path.split("/").some(part => !part || part === "." || part === "..")) throw new Error("Invalid GitLab repository path");
      return { forge: { name: this.forge }, path };
    });
    if (items.length > position.pageSize) throw new Error("Repository response exceeds requested page size");
    const nextCursor = pageContinuation(position, nextPage(headers, position.page, "gitlab") ?? (items.length >= position.pageSize ? position.page + 1 : undefined));
    return { items, ...(nextCursor ? { nextCursor } : {}) };
  }

  scanPullRequests(query: PullRequestScanQuery) {
    return scanPullRequests(this, query, this.#now(), operation => this.#http.withinDeadline(20_000, operation), this.#connection);
  }

  async listPullRequests(query: PullRequestQuery): Promise<Page<PullRequestKey>> {
    const position = pagePosition(`${this.#connection}:pullRequests:${queryKey(query, this.forge)}`, query);
    const parameters = new URLSearchParams({ scope: "all", state: query.state === "open" ? "opened" : query.state ?? "all",
      order_by: "updated_at", sort: "asc", per_page: String(position.pageSize), page: String(position.page) });
    if (query.sourceBranch) parameters.set("source_branch", query.sourceBranch);
    if (query.scope.author) parameters.set("author_username", query.scope.author);
    if (query.updatedAfter) parameters.set("updated_after", query.updatedAfter);
    if (query.updatedBefore) parameters.set("updated_before", query.updatedBefore);
    const project = query.scope.kind === "repository" ? query.scope.repository.path : undefined;
    const endpoint = project ? this.#projectBase(project) : this.#apiBase;
    const { data, headers } = await this.#http.request("GET", `${endpoint}/merge_requests?${parameters}`, { headers: this.#headers() });
    if (!Array.isArray(data) || data.length > position.pageSize) throw new Error("Invalid GitLab PullRequest page");
    const items: PullRequestKey[] = [], issues: ForgeReadIssue[] = [];
    for (const [index, value] of data.entries()) try {
      const row = record(`GitLab PullRequest row ${index}`, value);
      if (!authorMatches(row.author, "username", query.scope.author)) throw new Error("GitLab returned another author");
      // Repository queries already supply the canonical target identity. Global results need a path.
      let path = project;
      if (!path) {
        const url = new URL(nonEmptyString("GitLab web_url", row.web_url));
        const match = url.pathname.match(/^\/(.+?)\/(?:-\/)?merge_requests\/([1-9]\d*)\/?$/);
        if (url.origin !== new URL(this.#apiBase).origin || !match || Number(match[2]) !== row.iid) throw new Error("Invalid GitLab PullRequest identity URL");
        path = decodeURIComponent(match[1]!);
        if (path.split("/").length < 2) throw new Error("GitLab repository path requires a namespace");
      }
      const key = pullRequestKey(this.forge, path, row.iid as number);
      assertPullRequestKey(key, this.forge);
      items.push(key);
    } catch (error) { issues.push(readIssue(error, "list", { scope: query.scope })); }
    const next = nextPage(headers, position.page, "gitlab") ?? (data.length >= position.pageSize ? position.page + 1 : undefined);
    const nextCursor = pageContinuation(position, next);
    return { items, ...(nextCursor ? { nextCursor } : {}), ...(issues.length ? { issues } : {}) };
  }

  async getPullRequest(key: PullRequestKey): Promise<PullRequestSnapshot> {
    this.#assertPullRequestKey(key);
    const repository = pullRequestKey(this.forge, key.repository.path, key.number).repository;
    const { data } = await this.#http.request(
      "GET",
      `${this.#projectBase(key.repository.path)}` +
        `/merge_requests/${key.number}`,
      { headers: this.#headers() },
    );
    const mergeRequest = record("GitLab MergeRequest", data);
    let sourceRepository = mergeRequest.source_project_id === mergeRequest.target_project_id ? repository : undefined;
    if (!sourceRepository && Number.isSafeInteger(mergeRequest.source_project_id) && Number(mergeRequest.source_project_id) > 0) {
      try {
        const source = await this.#http.request("GET", `${this.#apiBase}/projects/${mergeRequest.source_project_id}`, { headers: this.#headers() });
        const project = record("GitLab source project", source.data);
        sourceRepository = { forge: repository.forge, path: nonEmptyString("GitLab source path", project.path_with_namespace) };
      } catch (error) { if (!(error instanceof ForgeNotFound)) throw error; }
    }

    return {
      ...(typeof mergeRequest.source_branch === "string" ? { sourceBranch: mergeRequest.source_branch } : {}),
      ...(typeof mergeRequest.target_branch === "string" ? { targetBranch: mergeRequest.target_branch } : {}),
      ...(sourceRepository ? { sourceRepository } : {}),
      repository,
      number: key.number,
      title: nonEmptyString("GitLab MergeRequest.title", mergeRequest.title),
      url: nonEmptyString("GitLab MergeRequest.web_url", mergeRequest.web_url),
      state: lifecycle(mergeRequest),
      conflictState: conflictState(mergeRequest),
      mergeReadiness: mergeReadiness(mergeRequest),
      ...(typeof mergeRequest.sha === "string" ? { headSha: mergeRequest.sha } : {}),
      ...(typeof (mergeRequest.diff_refs as Record<string, unknown> | undefined)?.base_sha === "string" ? { baseSha: (mergeRequest.diff_refs as Record<string, string>).base_sha } : {}),
      ...(typeof mergeRequest.updated_at === "string" ? { updatedAt: mergeRequest.updated_at } : {}),
      ...(typeof mergeRequest.merged_at === "string" ? { mergedAt: mergeRequest.merged_at } : {}),
    };
  }

  async getPullRequestReview(key: PullRequestKey): Promise<PullRequestReviewObservation> {
    this.#assertPullRequestKey(key);
    const review = await this.#reviewThreads(key);
    return { key: pullRequestKey(this.forge, key.repository.path, key.number), reviewThreadState: review.state,
      ...(review.activityFingerprint ? { reviewActivityFingerprint: review.activityFingerprint } : {}),
      observedAt: this.#now().toISOString() };
  }

  async listComments(key: PullRequestKey): Promise<readonly Comment[]> {
    this.#assertPullRequestKey(key);
    const rows = await readComments(key, async page => this.#http.request("GET", `${this.#projectBase(key.repository.path)}/merge_requests/${key.number}/discussions?per_page=100&page=${page}`, { headers: this.#headers() }));
    await this.#comments.capture(key, rows, this.#http.signal);
    return rows;
  }
  async getComment(key: PullRequestKey, id: string): Promise<Comment> {
    const found = (await this.listComments(key)).find(c => c.id === id);
    if (!found) throw new ForgeNotFound("comment not found in the specified PR");
    return found;
  }

  #headers(): HeadersInit {
    return {
      Accept: "application/json",
      "PRIVATE-TOKEN": this.#token,
    };
  }

  #projectBase(repository: string): string {
    return `${this.#apiBase}/projects/${encodedProject(repository)}`;
  }

  #assertPullRequestKey(key: PullRequestKey): void {
    assertPullRequestKey(key, this.forge);
  }

  async #reviewThreads(
    key: PullRequestKey,
  ): Promise<{
    readonly state: PullRequestReviewThreadState;
    readonly activityFingerprint?: string;
  }> {
    let page = 1;
    const activityTokens: string[] = [];
    let unresolved = false;
    for (let count = 0; count < MAX_DISCUSSION_PAGES; count += 1) {
      const { data, headers } = await this.#http.request(
        "GET",
        `${this.#projectBase(key.repository.path)}` +
          `/merge_requests/${key.number}/discussions?per_page=100&page=${page}`,
        { headers: this.#headers() },
      );
      const discussions = records("GitLab discussions", data);
      for (const discussion of discussions) {
        const notes = records("GitLab discussion notes", discussion.notes);
        // Only GitLab's resolvable review discussions are merge gates. Plain
        // conversation notes must not become unresolved review threads.
        const resolvable = notes.filter((note) => note.resolvable === true);
        if (resolvable.length === 0) continue;
        if (typeof discussion.id !== "string" || !discussion.id) {
          throw new Error("GitLab review discussion has no id");
        }
        unresolved ||= resolvable.some((note) => note.resolved !== true);
        const noteTokens = notes.map((note) => {
          if (
            (typeof note.id !== "number" && typeof note.id !== "string") ||
            note.id === ""
          ) {
            throw new Error("GitLab review discussion note has no id");
          }
          if (
            note.updated_at !== undefined &&
            typeof note.updated_at !== "string"
          ) {
            throw new Error(
              "GitLab review discussion note updated_at is invalid",
            );
          }
          return [
            String(note.id),
            note.updated_at ?? null,
            note.resolvable === true,
            note.resolved === true,
          ];
        });
        activityTokens.push(JSON.stringify([discussion.id, noteTokens]));
      }

      const nextPage = headers.get("x-next-page");
      if (!nextPage) {
        if (activityTokens.length === 0) return { state: "none" };
        return {
          state: unresolved ? "unresolved" : "resolved",
          activityFingerprint: reviewActivityFingerprint("gitlab", activityTokens),
        };
      }
      const parsed = Number(nextPage);
      if (!Number.isSafeInteger(parsed) || parsed <= page) {
        throw new Error("GitLab discussion pagination is invalid");
      }
      page = parsed;
    }
    throw new Error("GitLab discussion pagination limit exceeded");
  }
}
