import { RepoKitError } from "../errors.js";
import type { RepositoryKey, RepositorySnapshot, PullRequestSnapshot, PullRequestKey, PullRequestState, PullRequestMergeReadiness } from "@compforge/harness-common";
export type { PullRequestKey } from "@compforge/harness-common";

/** Account discovery and repository membership are different query scopes. Authors are platform usernames. */
export type PullRequestScope =
  | { readonly kind: "account"; readonly author: string }
  | { readonly kind: "repository"; readonly repository: RepositoryKey; readonly author?: string };

export interface PageQuery {
  readonly cursor?: string;
  /** Maximum rows per provider request, 1..100; default 50. */
  readonly pageSize?: number;
}

/** Successful rows remain usable when individual rows cannot be decoded. Issues preclude full coverage. */
export interface Page<T> {
  readonly items: readonly T[];
  readonly nextCursor?: string;
  readonly issues?: readonly ForgeReadIssue[];
}

export interface PullRequestQuery extends PageQuery {
  readonly scope: PullRequestScope;
  readonly state?: PullRequestState | "all";
  readonly sourceBranch?: string;
  readonly updatedAfter?: string;
  readonly updatedBefore?: string;
}

export type PullRequestScanCursor = string & { readonly __pullRequestScanCursor: unique symbol };
export interface PullRequestScanQuery {
  readonly scope: PullRequestScope;
  readonly cursor?: PullRequestScanCursor;
  /** Initial history horizon, default seven days. The initial open inventory is also enumerated. */
  readonly initialSince?: string;
  /** Current refresh membership, supplied each call. Omission means no tracked refresh. */
  readonly tracked?: readonly PullRequestKey[];
}
export interface PullRequestScanResult {
  readonly items: readonly PullRequestSnapshot[];
  readonly cursor: PullRequestScanCursor;
  /** More work in this pass. False permits the caller's normal poll interval, even with unresolved issues. */
  readonly pending: boolean;
  /** No unfinished work or retained gaps in this scan. This does not promise a complete event log. */
  readonly complete: boolean;
  readonly issues: readonly ForgeReadIssue[];
}
export interface ForgeReadIssue {
  readonly stage: "list" | "get" | "retry" | "refresh";
  readonly kind: "auth" | "not_found" | "rate_limit" | "transport" | "invalid_data" | "coverage";
  readonly message: string;
  readonly key?: PullRequestKey;
  readonly scope?: PullRequestScope;
  readonly window?: { readonly state: "open" | "all"; readonly updatedAfter: string; readonly updatedBefore: string };
  readonly retryAfterMs?: number;
}

export type PullRequestReviewThreadState = "none" | "unresolved" | "resolved" | "unknown";
/** Review is fetched explicitly; failure never retracts a separately obtained PullRequestSnapshot. */
export interface PullRequestReviewObservation {
  readonly key: PullRequestKey;
  readonly reviewThreadState: PullRequestReviewThreadState;
  readonly reviewActivityFingerprint?: string;
  readonly mergeReadiness?: PullRequestMergeReadiness;
  readonly observedAt: string;
}

/** Read-only provider access. PullRequestObserver owns optional polling and persistence; consumers own business admission. */
export interface ForgeReader {
  readonly forge: string;
  readonly provider: string;
  /** Cooperative cancellation for a group of reads; concurrent callers must remain isolated. */
  withSignal?<T>(signal: AbortSignal, operation: () => Promise<T>): Promise<T>;
  close?(): void | Promise<void>;
  listRepositories(query?: PageQuery): Promise<Page<RepositorySnapshot>>;
  listPullRequests(query: PullRequestQuery): Promise<Page<PullRequestKey>>;
  scanPullRequests(query: PullRequestScanQuery): Promise<PullRequestScanResult>;
  getPullRequest(key: PullRequestKey): Promise<PullRequestSnapshot>;
  getPullRequestReview(key: PullRequestKey): Promise<PullRequestReviewObservation>;
  listPullRequestComments(key: PullRequestKey): Promise<readonly ForgeComment[]>;
}

/** One provider-neutral conversation comment or diff review thread. */
export interface ForgeComment {
  readonly id: string;
  readonly body: string;
  readonly author?: string;
  readonly replyable: boolean;
  readonly replies: readonly ForgeComment[];
  readonly path?: string;
  readonly line?: number;
  readonly createdAt: string;
  readonly resolution?: "unsupported" | "unresolved" | "resolved";
}

/** Connection identity only. Query authors never come from credentials or connection configuration. */
export interface ForgeReaderConfig {
  readonly forge: string;
  readonly provider: "github" | "gitlab";
  readonly host: string;
  readonly token: string;
}
export type ForgeFetch = (input: string | URL | Request, init?: RequestInit) => Promise<Response>;
export interface ForgeReaderOptions {
  readonly signal?: AbortSignal;
  /** Minimum request-start spacing across one reader; default 0. */
  readonly minIntervalMs?: number;
  readonly fetch?: ForgeFetch;
  readonly now?: () => Date;
  /** Per-request deadline including body consumption; default 10 seconds. */
  readonly timeoutMs?: number;
  /** Maximum response body; default 4 MiB. */
  readonly maxResponseBytes?: number;
}
export class ForgeError extends RepoKitError {
  protected static readonly errorCode: string = "forge.request_failed";
  constructor(message: string = "", options?: ErrorOptions) {
    super(new.target.errorCode, message, options);
    this.name = new.target.name;
  }
}
export class ForgeCoverageError extends ForgeError { protected static readonly errorCode = "forge.coverage_incomplete"; }
export class ForgeAuthError extends ForgeError { protected static readonly errorCode = "forge.authentication_required"; }
export class ForgeNotFound extends ForgeError { protected static readonly errorCode = "forge.not_found"; }
export class ForgeRateLimitError extends ForgeError {
  protected static readonly errorCode = "forge.rate_limited";
  constructor(message: string, readonly retryAfterMs: number) { super(message); this.name = "ForgeRateLimitError"; }
}
export function isForgeRateLimitError(error: unknown): error is ForgeRateLimitError {
  // Installed extensions can resolve a separate copy of this package in the same process.
  return error instanceof Error && error.name === "ForgeRateLimitError" && "retryAfterMs" in error
    && typeof error.retryAfterMs === "number" && Number.isFinite(error.retryAfterMs) && error.retryAfterMs >= 0;
}
