import type { PullRequestKey } from "./model.js";
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
  readonly id: string;
  readonly pr: PullRequestKey;
  /** Provider-confirmed direct parent. Absence does not prove a root. */
  readonly replyToId?: string;
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
  getComment(pr: PullRequestKey, id: string): Promise<Comment>;
}
/** Native TS providers implement reads; writes require an explicit implementation. */
export interface CommentForge extends CommentReader {
  createComment(pr: PullRequestKey, body: string, anchor?: CommentAnchor): Promise<Comment>;
  replyComment(pr: PullRequestKey, id: string, body: string): Promise<Comment>;
  updateComment(pr: PullRequestKey, id: string, body: string): Promise<Comment>;
  deleteComment(pr: PullRequestKey, id: string): Promise<{
    readonly pr: PullRequestKey;
    readonly commentId: string;
    readonly deleted: boolean;
  }>;
  resolveComment(pr: PullRequestKey, id: string, resolved: boolean): Promise<Comment>;
}
