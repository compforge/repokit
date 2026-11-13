import { RepoKitError } from "../errors.js";

/**
 * @spec History failures expose the same code, operation and subject as the Go API; cause is retained for diagnosis.
 * @see {@link component://docs/validation.md}
 */
export class ValidationHistoryError extends RepoKitError {
  constructor(code: string, message: string, op: string, subject: string, cause?: unknown) {
    super(code, message, {op, subject, cause});
    this.name = "ValidationHistoryError";
  }
}

export function historyError(error: unknown, op: string, subject: string, signal?: AbortSignal): ValidationHistoryError {
  if (error instanceof ValidationHistoryError) return error;
  const reason = signal?.aborted ? signal.reason : error;
  const problem = reason as {name?: string; code?: string; cause?: {name?: string}} | undefined;
  let code = "validation.history_failed";
  if (problem?.name === "TimeoutError" || problem?.cause?.name === "TimeoutError") code = "execution.deadline_exceeded";
  else if (signal?.aborted || problem?.name === "AbortError") code = "execution.canceled";
  else if (problem?.code === "EACCES" || problem?.code === "EPERM") code = "filesystem.permission_denied";
  else if (problem?.code === "ENOENT") code = "filesystem.not_found";
  return new ValidationHistoryError(code, error instanceof Error ? error.message : String(error), op, subject, error);
}
