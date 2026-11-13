/** Machine-readable RepoKit failure facts, shared with Go's errs.Error. */
export interface RepoKitErrorOptions {
  readonly op?: string;
  readonly subject?: string;
  readonly hint?: string;
  readonly uncertain?: boolean;
  readonly httpStatus?: number;
  /** RFC3339 timestamp, preserving the source precision. */
  readonly retryAt?: string;
  readonly cause?: unknown;
}

/**
 * @spec JSON and text expose the same failure facts; cause stays in-process and is never serialized.
 * @see {@link component://docs/errors.md}
 */
export class RepoKitError extends Error {
  readonly op: string;
  readonly subject: string;
  readonly hint: string;
  readonly uncertain: boolean;
  readonly httpStatus: number;
  readonly retryAt?: string;

  constructor(readonly code: string, message: string, options: RepoKitErrorOptions = {}) {
    super(message, {cause: options.cause});
    this.name = "RepoKitError";
    this.op = options.op ?? "";
    this.subject = options.subject ?? "";
    this.hint = options.hint ?? "";
    this.uncertain = options.uncertain ?? false;
    this.httpStatus = options.httpStatus ?? 0;
    this.retryAt = options.retryAt;
  }

  toJSON(): Record<string, unknown> {
    return {code: this.code, message: this.message,
      ...(this.op ? {op: this.op} : {}), ...(this.subject ? {subject: this.subject} : {}),
      ...(this.hint ? {hint: this.hint} : {}), ...(this.uncertain ? {uncertain: true} : {}),
      ...(this.httpStatus ? {httpStatus: this.httpStatus} : {}), ...(this.retryAt ? {retryAt: this.retryAt} : {})};
  }

  text(): string {
    let text = `[${this.code}]`;
    if (this.op) text += ` ${this.op}`;
    if (this.subject) text += ` (${this.subject})`;
    text += `: ${this.message}`;
    if (this.uncertain) text += "; outcome unknown";
    if (this.httpStatus) text += `; HTTP ${this.httpStatus}`;
    if (this.retryAt) text += `; retry after ${this.retryAt}`;
    if (this.hint) text += `\nHint: ${this.hint}`;
    return text;
  }
}
