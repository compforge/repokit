import { recordAccess } from "../../state/activity.js";
import { resolveHome } from "../../state/home.js";
import { repositoryKey } from "../../state/identity.js";
import { pullRequestId } from "@compforge/harness-common";
import { setTimeout as delay } from "node:timers/promises";
import { assertPullRequestKey, pullRequestKey } from "../key.js";
import { scopeKey } from "../query.js";
import { readIssue } from "../issues.js";
import { ForgeError, type PullRequestKey, type PullRequestScanCursor, type PullRequestScope } from "../model.js";
import { createPullRequestScanner } from "../scan.js";
import { digest, ObservationStorageError, ObservationStore } from "../../state/store.js";
import type { ObservationReader, PullRequestObservationOptions, PullRequestObservationQuery, PullRequestObservationResult,
  PullRequestWatch, PullRequestObservationBatch, StoredObservation } from "./model.js";

const positive = (name: string, value: number) => {
  if (!Number.isSafeInteger(value) || value < 1 || value > 2_147_483_647) throw new Error(`${name} must be a positive timer-safe integer`);
  return value;
};

/**
 * @spec Successful facts are persisted before discovery checkpoints advance; storage failures reject the operation.
 * @spec Merged lifecycle observations stop routine refresh after persistence. Errors never erase successful facts.
 * @spec Discovery backpressure cannot block active-object refresh. Each PR is refreshed under a cross-process lock.
 * @see {@link component://docs/pr.md#持续观察}
 * @case tests/forge-observation.test.mjs
 */
export class ObservationEngine {
  readonly #store: ObservationStore;
  readonly #connection: string;
  readonly #now: () => Date;
  readonly #open: number;
  readonly #closed: number;
  readonly #retry: number;
  readonly #timeout: number;
  constructor(readonly reader: ObservationReader, options: PullRequestObservationOptions) {
    if (!options.connection.trim()) throw new Error("PR observation connection must be nonempty");
    this.#store = new ObservationStore(resolveHome(options.home));
    this.#connection = digest([reader.forge, options.connection]);
    this.#now = options.now ?? (() => new Date());
    this.#open = positive("openIntervalMs", options.openIntervalMs ?? 60_000);
    this.#closed = positive("closedIntervalMs", options.closedIntervalMs ?? 600_000);
    this.#retry = positive("retryIntervalMs", options.retryIntervalMs ?? 30_000);
    this.#timeout = positive("requestTimeoutMs", options.requestTimeoutMs ?? 20_000);
  }
  #time(): number { const time = this.#now().getTime(); if (!Number.isSafeInteger(time) || time < 0) throw new Error("Invalid observation clock"); return time; }
  #repo(key: PullRequestKey): string { return repositoryKey(key.repository.forge.name, key.repository.path); }
  #path(key: PullRequestKey): string { return this.#store.path("repositories", this.#repo(key), "prs", String(key.number), "observations", this.#connection+".json"); }
  #scope(scope: PullRequestScope): string { return digest([this.#connection, scopeKey(scope, this.reader.forge)]); }
  async #stored(path: string): Promise<StoredObservation | undefined> {
    const record = await this.#store.read<StoredObservation>(path);
    if (record && (record.version !== 1 || !record.observation || !Array.isArray(record.scopeIds) || !Number.isSafeInteger(record.failures))) {
      throw new ObservationStorageError(`Invalid PR observation ${path}`);
    }
    return record?.connection === this.#connection ? record : undefined;
  }
  #fresh(record: StoredObservation, age: number): boolean {
    const observation = record.observation;
    return !observation.lastError && (observation.snapshot?.state === "merged" || Boolean(observation.snapshot && observation.observedAt &&
      !observation.lastError && this.#time() - Date.parse(observation.observedAt) <= age));
  }
  async #request<T>(operation: () => Promise<T>, signal: AbortSignal): Promise<T> {
    const requestSignal = AbortSignal.any([signal, AbortSignal.timeout(this.#timeout)]);
    requestSignal.throwIfAborted();
    let abort: () => void = () => {};
    const cancelled = new Promise<never>((_, reject) => {
      abort = () => reject(new ForgeError("PR read cancelled or timed out"));
      requestSignal.addEventListener("abort", abort, { once: true });
    });
    try {
      return await Promise.race([cancelled, this.reader.withSignal ? this.reader.withSignal(requestSignal, operation) : operation()]);
    } finally { requestSignal.removeEventListener("abort", abort); }
  }
  async get(key: PullRequestKey, query: PullRequestObservationQuery = {}): Promise<PullRequestObservationResult> {
    assertPullRequestKey(key,this.reader.forge);
    await recordAccess(this.#store,repositoryKey(key.repository.forge.name,key.repository.path),query.signal ?? new AbortController().signal);
    return this.#get(key, query);
  }
  async #get(key: PullRequestKey, query: PullRequestObservationQuery, scopeId?: string): Promise<PullRequestObservationResult> {
    assertPullRequestKey(key, this.reader.forge);
    key = pullRequestKey(this.reader.forge, key.repository.path, key.number);
    const age = query.maxAgeMs ?? this.#open;
    if (!Number.isSafeInteger(age) || age < 0) throw new Error("maxAgeMs must be a nonnegative safe integer");
    const signal = query.signal ?? new AbortController().signal;
    const path = this.#path(key);
    return this.#store.locked(path, signal, async assertOwned => {
      const previous = await this.#stored(path);
      if (previous && pullRequestId(previous.observation.key) !== pullRequestId(key)) throw new ObservationStorageError(`PR identity mismatch at ${path}`);
      const scopeIds = [...new Set([...(previous?.scopeIds ?? []), ...(scopeId ? [scopeId] : [])])];
      if (previous && !query.refresh && (this.#fresh(previous, age) ||
        (previous.observation.lastError && Date.parse(previous.observation.nextRefreshAt ?? "") > this.#time()))) {
        if (scopeIds.length !== previous.scopeIds.length) await this.#store.write(path, { ...previous, scopeIds }, assertOwned);
        return { ...previous.observation, fresh: this.#fresh(previous, age), source: "cache" };
      }
      const attemptedAt = new Date(this.#time()).toISOString();
      let record: StoredObservation;
      try {
        const snapshot = await this.#request(() => this.reader.getPullRequest(key), signal);
        if (pullRequestId(snapshot) !== pullRequestId(key) || !["open", "closed", "merged"].includes(snapshot.state ?? "")) throw new Error("Forge returned invalid PR identity or lifecycle");
        if (previous?.observation.snapshot?.state === "merged" && snapshot.state !== "merged") throw new Error("Forge contradicted a confirmed merged lifecycle");
        const now = this.#time();
        record = { version: 1, connection: this.#connection, scopeIds, failures: 0, observation: { key, snapshot, attemptedAt,
          observedAt: new Date(now).toISOString(), ...(snapshot.state === "merged" ? {} : {
            nextRefreshAt: new Date(now + (snapshot.state === "closed" ? this.#closed : this.#open)).toISOString() }) } };
      } catch (error) {
        signal.throwIfAborted();
        const issue = readIssue(error, "refresh", { key });
        const failures = (previous?.failures ?? 0) + 1;
        const backoff = Math.max(issue.retryAfterMs ?? 0, Math.min(3_600_000, this.#retry * 2 ** Math.min(failures - 1, 10)));
        record = { version: 1, connection: this.#connection, scopeIds, failures,
          observation: { ...previous?.observation, key, attemptedAt, lastError: issue,
            nextRefreshAt: new Date(this.#time() + backoff).toISOString() } };
      }
      // Repository metadata is informational; observation.json alone is the successful record boundary.
      const metadata = this.#store.path("repositories", this.#repo(key), "repository.json");
      await this.#store.write(metadata, key.repository, assertOwned);
      await this.#store.write(path, record, assertOwned);
      return { ...record.observation, fresh: this.#fresh(record, age), source: "remote" };
    });
  }

  /** One bounded refresh/discovery round. Callers can inspect issues or drive run() explicitly. */
  async poll(watch: PullRequestWatch = {}): Promise<PullRequestObservationBatch> {
    const signal = watch.signal ?? new AbortController().signal;
    signal.throwIfAborted();
    const scopes = watch.scopes ?? [];
    const scopeIds = new Set(scopes.map(scope => this.#scope(scope)));
    const keys = new Map((watch.keys ?? []).map(key => { assertPullRequestKey(key, this.reader.forge); return [pullRequestId(key), key]; }));
    const due = new Map<string, { key: PullRequestKey; at: number }>();
    for (const key of keys.values()) due.set(pullRequestId(key), { key, at: 0 });
    for (const path of await this.#store.observations(this.#connection)) {
      const record = await this.#stored(path);
      if (!record) continue;
      const observation = record.observation, id = pullRequestId(observation.key);
      const selected = keys.has(id) || record.scopeIds.some(id => scopeIds.has(id)) || (watch.keys === undefined && watch.scopes === undefined);
      if (!selected) continue;
      if (observation.snapshot?.state === "merged") { due.delete(id); continue; }
      const at = Date.parse(observation.nextRefreshAt ?? "") || 0;
      if (at <= this.#time()) due.set(id, { key: observation.key, at }); else due.delete(id);
    }
    const pendingKeys = (watch.refresh === false ? [] : [...due.values()]).sort((a,b) => a.at - b.at || pullRequestId(a.key).localeCompare(pullRequestId(b.key)));
    const observations: PullRequestObservationResult[] = [], issues: PullRequestObservationBatch["issues"][number][] = [];
    for (const { key } of pendingKeys.slice(0, 5)) {
      const result = await this.get(key, { signal });
      observations.push(result);
      if (result.lastError) issues.push(result.lastError);
    }
    let pending = pendingKeys.length > 5;
    // Scope checkpoints are serialized independently of PR refresh. A failed scan cannot undo successful refreshes.
    for (const scope of scopes) {
      const id = this.#scope(scope), path = this.#store.path("scans", `${id}.json`);
      const result = await this.#store.locked(path, signal, async assertOwned => {
        const state = await this.#store.read<{ version: number; cursor: PullRequestScanCursor }>(path);
        if (state && state.version !== 1) throw new ObservationStorageError(`Invalid PR scan state ${path}`);
        let storageFailure: unknown;
        const scanner = createPullRequestScanner({ forge: this.reader.forge,
          listPullRequests: query => this.#request(() => this.reader.listPullRequests(query), signal),
          getPullRequest: async key => {
            let result: PullRequestObservationResult;
            try { result = await this.#get(key, { signal }, id); }
            catch (error) { storageFailure = error; throw error; }
            observations.push(result);
            if (!result.snapshot || !result.fresh || result.lastError) throw new ForgeError(result.lastError?.message ?? "PR observation unavailable");
            return result.snapshot;
          } }, { connection: this.#connection, now: this.#now });
        const operation = () => scanner({ scope, cursor: state?.cursor, initialSince: watch.initialSince });
        const result = this.reader.withSignal ? await this.reader.withSignal(signal, operation) : await operation();
        if (storageFailure) throw storageFailure;
        assertOwned();
        await this.#store.write(path, { version: 1, cursor: result.cursor }, assertOwned);
        return result;
      });
      pending ||= result.pending;
      issues.push(...result.issues);
    }
    // Persisted refresh errors remain visible even during backoff, without causing tight retry loops.
    for (const path of await this.#store.observations(this.#connection)) {
      const record = await this.#stored(path);
      if (record?.observation.lastError && (keys.has(pullRequestId(record.observation.key)) || record.scopeIds.some(id => scopeIds.has(id)) ||
          (watch.keys === undefined && watch.scopes === undefined))) issues.push(record.observation.lastError);
    }
    const uniqueIssues = [...new Map(issues.map(issue => [JSON.stringify(issue), issue])).values()];
    return { observations, issues: uniqueIssues, pending, complete: !pending && uniqueIssues.length === 0 };
  }

  /** Explicit lifecycle: abort stops polling and in-flight cooperative provider work. */
  async *run(watch: PullRequestWatch & { signal: AbortSignal; pollIntervalMs?: number }): AsyncGenerator<PullRequestObservationBatch> {
    const interval = positive("pollIntervalMs", watch.pollIntervalMs ?? this.#open);
    try {
      while (!watch.signal.aborted) {
        const batch = await this.poll(watch);
        yield batch;
        await delay(batch.pending ? Math.min(interval, 1000) : interval, undefined, { signal: watch.signal });
      }
    } catch (error) { if (!watch.signal.aborted) throw error; }
  }
}
