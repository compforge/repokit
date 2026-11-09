import type { PullRequestSnapshot } from "@compforge/harness-common";
import type { ForgeReader, ForgeReadIssue, PullRequestKey, PullRequestScope } from "../model.js";

export interface PullRequestObservation {
  readonly key: PullRequestKey;
  readonly snapshot?: PullRequestSnapshot;
  readonly observedAt?: string;
  readonly attemptedAt: string;
  readonly nextRefreshAt?: string;
  readonly lastError?: ForgeReadIssue;
}
export interface PullRequestObservationResult extends PullRequestObservation {
  /** Lifecycle freshness only; a merged lifecycle is terminal, other snapshot fields still have observedAt. */
  readonly fresh: boolean;
  readonly source: "cache" | "remote";
}
export interface PullRequestObservationOptions {
  readonly home?: string;
  /** Stable access/configuration revision, changed whenever credentials or permissions change. Never a token. */
  readonly connection: string;
  readonly openIntervalMs?: number;
  readonly closedIntervalMs?: number;
  readonly retryIntervalMs?: number;
  readonly requestTimeoutMs?: number;
  readonly now?: () => Date;
}
export interface PullRequestObservationQuery {
  readonly maxAgeMs?: number;
  readonly refresh?: boolean;
  readonly signal?: AbortSignal;
}
export interface PullRequestWatch {
  readonly refresh?: boolean;
  readonly keys?: readonly PullRequestKey[];
  readonly scopes?: readonly PullRequestScope[];
  readonly initialSince?: string;
  readonly signal?: AbortSignal;
}
export interface PullRequestObservationBatch {
  readonly observations: readonly PullRequestObservationResult[];
  readonly issues: readonly ForgeReadIssue[];
  readonly pending: boolean;
  readonly complete: boolean;
}
export type ObservationReader = Pick<ForgeReader, "provider" | "forge" | "listPullRequests" | "getPullRequest" | "withSignal">;
export interface StoredObservation {
  version: 1;
  connection: string;
  observation: PullRequestObservation;
  scopeIds: string[];
  failures: number;
}
