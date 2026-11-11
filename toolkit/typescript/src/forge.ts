export type { PullRequest, PullRequestState, PullRequestSnapshot } from "@compforge/harness-common";
import type { PullRequest } from "@compforge/harness-common";
export * from "./forge/model.js";
export { GitHubForgeReader } from "./forge/github.js";
export { GitLabForgeReader } from "./forge/gitlab.js";
export { ForgeHttpClient, type ForgeHttpClientOptions } from "./forge/http.js";
export { createPullRequestScanner } from "./forge/scan.js";
export { createForgeReader } from "./forge/create.js";
export { iterateRepositories } from "./forge/repositories.js";
export { iteratePullRequests } from "./forge/pull-requests.js";

import type { ForgeComment } from "./forge/model.js";
/** Opaque handles supplied by a repository write adapter; separate from comment content. */
export interface ForgeCommentTarget {
  readonly replyRef?: string;
  readonly resolveRef?: string;
}

/** Comment facts plus optional write handles from a repository-bound adapter. */
export interface RepositoryForgeComment extends ForgeComment {
  readonly target?: ForgeCommentTarget;
  readonly replies: readonly RepositoryForgeComment[];
}

export interface Release {
  readonly tag: string;
  readonly name: string;
  readonly target: string;
  readonly webUrl: string;
  readonly createdAt?: string;
}

export type { PullRequestMergeReadiness } from "@compforge/harness-common";
import type { PullRequestMergeReadiness } from "@compforge/harness-common";

/** Repository-bound Forge workflow contract. Native TypeScript adapters implement the separate read-only ForgeReader. */
export interface RepositoryForge {
  readonly provider: "github" | "gitlab";
  create(input: { readonly sourceBranch: string; readonly targetBranch: string; readonly title: string; readonly body?: string }): Promise<PullRequest>;
  get(number: number): Promise<PullRequest>;
  description(number: number): Promise<string>;
  update(number: number, fields: { readonly title?: string; readonly body?: string; readonly targetBranch?: string }): Promise<PullRequest>;
  close(number: number): Promise<PullRequest>;
  pullRequestsForBranch(branch: string): Promise<readonly PullRequest[]>;
  recent(limit: number): Promise<readonly PullRequest[]>;
  defaultBranch(): Promise<string>;
  createRelease(input: { readonly tag: string; readonly target: string; readonly name?: string; readonly notes?: string }): Promise<Release>;
  latestRelease(): Promise<Release | undefined>;
  comments(number: number): Promise<readonly RepositoryForgeComment[]>;
  comment(number: number, body: string, options?: { readonly replyable?: boolean; readonly path?: string; readonly line?: number }): Promise<void>;
  reply(number: number, target: ForgeCommentTarget, body: string): Promise<void>;
  resolveComment(number: number, target: ForgeCommentTarget): Promise<void>;
  mergeReadiness(number: number): Promise<PullRequestMergeReadiness>;
}

export function parsePullRequestNumber(value: string): number | undefined {
  const match = value.match(/\/(?:pull|merge_requests)\/(\d+)/) ?? value.trim().match(/^[!#]?(\d+)$/);
  return match?.[1] === undefined ? undefined : Number.parseInt(match[1], 10);
}


export { PullRequestObserver, PullRequestStatusUnavailable } from "./forge/observation/service.js";
export type { PullRequestStatus, PullRequestObserverOptions, PullRequestObserverHealth } from "./forge/observation/service.js";
export { PullRequestContext } from "./forge/observation/context.js";
export type { BranchPullRequests, PullRequestContextOptions } from "./forge/observation/context.js";

export { ForgeConnection, detectProvider, type ForgeConnectionConfig } from "./forge/connection.js";
