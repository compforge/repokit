import type { PullRequestKey } from "./model.js";
/** Opaque, stable PR-local identity: <resource>:<native-id>; adapters own decoding. */
export type CommentID = string;
export type CommentResolution = "unresolved" | "resolved" | "unsupported" | "unknown";
export interface CommentAnchor {
  readonly path: string;
  readonly revision: string;
  readonly side?: "LEFT" | "RIGHT";
  readonly line?: number;
  readonly startLine?: number;
  readonly startSide?: "LEFT" | "RIGHT";
  readonly originalRevision?: string;
  readonly originalLine?: number;
  readonly originalStartLine?: number;
  readonly oldPath?: string;
  readonly outdated?: boolean;
}
/** One published message, including replies. No inferred acceptance or fix verdicts. */
export interface Comment {
  readonly id: CommentID;
  readonly pr: PullRequestKey;
  /** Platform-recoverable structural parent; grouped replies point to their root. */
  readonly replyToId?: CommentID;
  readonly body: string;
  readonly author?: string;
  readonly createdAt: string;
  readonly updatedAt?: string;
  readonly url?: string;
  readonly anchor?: CommentAnchor;
  /** Projected from the containing discussion's platform state. */
  readonly resolution: CommentResolution;
  readonly resolvedBy?: string;
  readonly resolvedAt?: string;
}
export interface CommentReader {
  listComments(pr: PullRequestKey): Promise<readonly Comment[]>;
  getComment(pr: PullRequestKey, id: CommentID): Promise<Comment>;
}
/** Native TS providers implement reads; writes require an explicit implementation. */
export interface CommentForge extends CommentReader {
  createComment(pr: PullRequestKey, body: string, anchor?: CommentAnchor): Promise<Comment>;
  replyComment(pr: PullRequestKey, id: CommentID, body: string): Promise<Comment>;
  updateComment(pr: PullRequestKey, id: CommentID, body: string): Promise<Comment>;
  deleteComment(pr: PullRequestKey, id: CommentID): Promise<{
    readonly pr: PullRequestKey;
    readonly commentId: CommentID;
    readonly deleted: boolean;
  }>;
  resolveComment(pr: PullRequestKey, id: CommentID, resolved: boolean): Promise<Comment>;
}
