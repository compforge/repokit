import { AsyncLocalStorage } from "node:async_hooks";
import { setTimeout as delay } from "node:timers/promises";
import { ForgeRateLimitError, ForgeError, ForgeAuthError, ForgeNotFound } from "./model.js";

const DEFAULT_RATE_LIMIT_BACKOFF_MS = 60_000;
export type ForgeHttpClientOptions = Pick<import("./model.js").ForgeReaderOptions,
  "fetch" | "timeoutMs" | "maxResponseBytes" | "signal" | "minIntervalMs">;

/**
 * @spec One provider connection shares pacing and rate-limit cooldown; concurrent call signals/deadlines stay isolated.
 * @spec Requests and bodies are bounded; caller cancellation and close abort network work. No automatic retries.
 * @spec Diagnostics omit query strings, request headers and response bodies.
 * @link docs/pr.md
 */
export class ForgeHttpClient {
  readonly #fetch: import("./model.js").ForgeFetch;
  readonly #timeoutMs: number;
  readonly #maxResponseBytes: number;
  readonly #minIntervalMs: number;
  readonly #signal?: AbortSignal;
  readonly #abort = new AbortController();
  readonly #calls = new AsyncLocalStorage<AbortSignal>();
  readonly #deadline = new AsyncLocalStorage<number>();
  #nextStart = 0;
  #blockedUntil = 0;

  constructor(options: ForgeHttpClientOptions = {}) {
    this.#fetch = options.fetch ?? globalThis.fetch;
    this.#timeoutMs = options.timeoutMs ?? 10_000;
    this.#maxResponseBytes = options.maxResponseBytes ?? 4 * 1024 * 1024;
    this.#minIntervalMs = options.minIntervalMs ?? 0;
    this.#signal = options.signal;
    if (!Number.isSafeInteger(this.#timeoutMs) || this.#timeoutMs < 1 || this.#timeoutMs > 2_147_483_647) throw new Error("Forge timeoutMs must be a positive timer-safe integer");
    if (!Number.isSafeInteger(this.#maxResponseBytes) || this.#maxResponseBytes < 1) throw new Error("Forge maxResponseBytes must be a positive safe integer");
    if (!Number.isSafeInteger(this.#minIntervalMs) || this.#minIntervalMs < 0 || this.#minIntervalMs > 2_147_483_647) throw new Error("Forge minIntervalMs must be a nonnegative timer-safe integer");
  }
  withinDeadline<T>(timeoutMs: number, operation: () => Promise<T>): Promise<T> {
    if (!Number.isSafeInteger(timeoutMs) || timeoutMs < 1) throw new Error("Forge aggregate timeout must be a positive safe integer");
    return this.#deadline.run(Math.min(this.#deadline.getStore() ?? Infinity, Date.now() + timeoutMs), operation);
  }
  get signal(): AbortSignal {
    return AbortSignal.any([this.#abort.signal, ...(this.#signal ? [this.#signal] : []), ...(this.#calls.getStore() ? [this.#calls.getStore()!] : [])]);
  }
  withSignal<T>(signal: AbortSignal, operation: () => Promise<T>): Promise<T> {
    const parent = this.#calls.getStore();
    return this.#calls.run(parent ? AbortSignal.any([parent, signal]) : signal, operation);
  }
  close(): void { this.#abort.abort(new ForgeError("Forge client closed")); }

  async request(method: string, url: string, init: Omit<RequestInit, "method" | "signal"> = {}): Promise<{ readonly data: unknown; readonly headers: Headers }> {
    const destination = new URL(url);
    const label = `${method} ${destination.origin}${destination.pathname}`;
    const now = Date.now();
    const remaining = (this.#deadline.getStore() ?? Infinity) - now;
    if (remaining <= 0) throw new ForgeError(`Forge read deadline exceeded before ${label}`);
    if (this.#blockedUntil > now) throw new ForgeRateLimitError("Forge requests paused after rate limiting", this.#blockedUntil - now);
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), Math.min(this.#timeoutMs, remaining));
    const signal = AbortSignal.any([controller.signal, this.#abort.signal, ...(this.#signal ? [this.#signal] : []), ...(this.#calls.getStore() ? [this.#calls.getStore()!] : [])]);
    const start = Math.max(now, this.#nextStart);
    this.#nextStart = start + this.#minIntervalMs;
    try {
      if (signal.aborted) throw new ForgeError(`${label}: cancelled`);
      if (start > now) await delay(start - now, undefined, { signal });
      if (this.#blockedUntil > Date.now()) throw new ForgeRateLimitError("Forge requests paused after rate limiting", this.#blockedUntil - Date.now());
      const response = await this.#fetch(url, { ...init, method, signal });
      const body = await this.#readBody(response);
      if (!response.ok) {
        if (rateLimited(response, body)) throw this.rateLimit(`${label} returned ${response.status}`, retryAfterMs(response.headers));
        const ErrorType = response.status === 401 || response.status === 403 ? ForgeAuthError : response.status === 404 ? ForgeNotFound : ForgeError;
        throw new ErrorType(`${label} returned ${response.status}`);
      }
      try { return { data: body ? JSON.parse(body) as unknown : undefined, headers: response.headers }; }
      catch { throw new ForgeError(`${label} returned invalid JSON`); }
    } catch (error) {
      if (error instanceof ForgeError) throw error;
      throw new ForgeError(`${label}: ${signal.aborted ? "cancelled or timed out" : "transport failure"}`);
    } finally { clearTimeout(timer); }
  }
  rateLimit(detail: string, delayMs = DEFAULT_RATE_LIMIT_BACKOFF_MS): ForgeRateLimitError {
    const boundedDelay = Number.isFinite(delayMs) ? Math.max(1, delayMs) : DEFAULT_RATE_LIMIT_BACKOFF_MS;
    this.#blockedUntil = Math.max(this.#blockedUntil, Date.now() + boundedDelay);
    return new ForgeRateLimitError(detail, boundedDelay);
  }
  async #readBody(response: Response): Promise<string> {
    if (Number(response.headers.get("content-length")) > this.#maxResponseBytes) {
      await response.body?.cancel();
      throw new ForgeError(`exceeded ${this.#maxResponseBytes} response bytes`);
    }
    if (!response.body) return "";
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let size = 0, text = "";
    try {
      while (true) {
        const { done, value } = await reader.read();
        if (done) break;
        size += value.byteLength;
        if (size > this.#maxResponseBytes) throw new ForgeError(`exceeded ${this.#maxResponseBytes} response bytes`);
        text += decoder.decode(value, { stream: true });
      }
      return text + decoder.decode();
    } finally { await reader.cancel().catch(() => {}); reader.releaseLock(); }
  }
}

function rateLimited(response: Response, body: string): boolean {
  return response.status === 429 ||
    (
      response.status === 403 &&
      (
        response.headers.get("x-ratelimit-remaining") === "0" ||
        /rate.?limit/i.test(body)
      )
    );
}

function retryAfterMs(headers: Headers): number {
  const retryAfter = headers.get("retry-after");
  if (retryAfter) {
    const seconds = Number(retryAfter);
    if (Number.isFinite(seconds) && seconds >= 0) return seconds * 1_000;
    const date = Date.parse(retryAfter);
    if (Number.isFinite(date)) return Math.max(1, date - Date.now());
  }
  const reset = Number(headers.get("x-ratelimit-reset"));
  if (Number.isFinite(reset) && reset > 0) {
    return Math.max(1, reset * 1_000 - Date.now());
  }
  return DEFAULT_RATE_LIMIT_BACKOFF_MS;
}

export function record(name: string, value: unknown): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value)) {
    throw new Error(`${name} must be an object`);
  }
  return value as Record<string, unknown>;
}

export function records(name: string, value: unknown): Record<string, unknown>[] {
  if (!Array.isArray(value)) {
    throw new Error(`${name} must be an array`);
  }
  return value.map((item, index) => record(`${name}[${index}]`, item));
}

export function nonEmptyString(name: string, value: unknown): string {
  if (typeof value !== "string" || !value.trim()) {
    throw new Error(`${name} must be a non-empty string`);
  }
  return value;
}

export function reviewActivityFingerprint(
  provider: "github" | "gitlab",
  tokens: readonly string[],
): string {
  return createHash("sha256")
    .update(JSON.stringify([provider, [...tokens].sort()]))
    .digest("hex");
}
import { createHash } from "node:crypto";
