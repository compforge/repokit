import { repositoryKey, namePart } from "../../state-paths.js";
import { basename } from "node:path";
import { remote, type Remote } from "../../git/remote.js";
import type { RepositoryKey, PullRequestSnapshot } from "@compforge/harness-common";
import { currentBranch, localBranches, listCheckouts, isAncestor, type CheckoutEntry } from "../../git/state.js";
import { ForgeError, type PullRequestKey } from "../model.js";
import { ObservationEngine } from "./engine.js";
import type { ObservationReader, PullRequestObservationOptions } from "./model.js";
import { digest, ObservationStore } from "./store.js";

export interface BranchPullRequests {
  readonly repository: RepositoryKey;
  readonly branch: string;
  readonly head: string;
  readonly candidates: readonly PullRequestSnapshot[];
  readonly checkouts: readonly CheckoutEntry[];
  readonly selected?: PullRequestSnapshot;
  readonly ambiguous: boolean;
  readonly error?: string;
}
export interface PullRequestContextOptions extends PullRequestObservationOptions {
  readonly checkout: string;
  readonly source: RepositoryKey;
  readonly targets: readonly RepositoryKey[];
}
interface Association {
  readonly version: 1;
  readonly keys?: readonly PullRequestKey[];
  readonly nextRefreshAt: string;
  readonly error?: string;
}
const sameRepository = (a: RepositoryKey | undefined, b: RepositoryKey) => a?.forge.name === b.forge.name && a.path === b.path;

/**
 * @spec Source repository, branch and target repositories bound association discovery. Ambiguity never selects an arbitrary PR.
 * @spec Branch and checkout topology is observed from Git, never used from a persisted projection.
 * @see {@link component://docs/pr.md}
 * @case tests/pr-context.test.mjs
 */
export class PullRequestContext {
  readonly #engine: ObservationEngine;
  readonly #remote: Remote | undefined;
  readonly #store: ObservationStore;
  readonly #controller = new AbortController();
  readonly #now: () => Date;
  constructor(readonly reader: ObservationReader, readonly options: PullRequestContextOptions) {
    if (!options.targets.length || [options.source, ...options.targets].some(repo => repo.forge.name !== reader.forge)) throw new Error("Invalid PR association repository scope");
    this.#remote = remote(options.checkout);
    this.#engine = new ObservationEngine(reader, options);
    this.#store = new ObservationStore(options.stateDir);
    this.#now = options.now ?? (() => new Date());
  }
  #path(branch: string): string {
    const clean = (repo: RepositoryKey) => ({ forge: { name: repo.forge.name }, path: repo.path });
    return this.#store.path("repositories", repositoryKey(this.options.source.forge.name,this.options.source.path),"branches",namePart(branch),"associations", `${digest([digest([this.reader.forge, this.options.connection]), clean(this.options.source), branch, this.options.targets.map(clean)])}.json`);
  }
  async #keys(branch: string, verify: boolean, signal: AbortSignal): Promise<readonly PullRequestKey[]> {
    const { targets } = this.options;
    const path = this.#path(branch);
    return this.#store.locked(path, signal, async owned => {
      let record = await this.#store.read<Association>(path);
      if (!record) {
        record = await this.#store.read<Association>(this.#store.path("associations",basename(path)));
        if(record) await this.#store.write(path,record,owned);
      }
      if (record && (record.version !== 1 || !Number.isFinite(Date.parse(record.nextRefreshAt)) || !record.error && !Array.isArray(record.keys))) throw new ForgeError("Invalid PR association record");
      if (!verify && record && Date.parse(record.nextRefreshAt) > this.#now().getTime()) {
        if (record.error) throw new ForgeError(record.error);
        return record.keys ?? [];
      }
      const keys: PullRequestKey[] = [];
      try {
        for (const target of targets) {
          let cursor: string | undefined;
          const seen = new Set<string>();
          do {
            signal.throwIfAborted();
            const query = () => this.reader.listPullRequests({ scope: { kind: "repository", repository: target }, state: "all", sourceBranch: branch, cursor });
            let aborted: () => void = () => {};
            const cancelled = new Promise<never>((_, reject) => {
              aborted = () => reject(signal.reason);
              signal.addEventListener("abort", aborted, { once: true });
            });
            const page = await Promise.race([this.reader.withSignal ? this.reader.withSignal(signal, query) : query(), cancelled])
              .finally(() => signal.removeEventListener("abort", aborted));
            if (page.issues?.length) throw new ForgeError(page.issues.map(issue => issue.message).join("; "));
            // List APIs may match a same-named branch from another fork; snapshots prove source identity.
            keys.push(...page.items);
            cursor = page.nextCursor;
            if (cursor && seen.has(cursor)) throw new ForgeError("PR association pagination repeated");
            if (cursor) seen.add(cursor);
          } while (cursor);
        }
      } catch (error) {
        signal.throwIfAborted();
        await this.#store.write(path, { version: 1, error: String(error), nextRefreshAt: new Date(this.#now().getTime() + (this.options.retryIntervalMs ?? 30_000)).toISOString() }, owned);
        throw error;
      }
      const unique = [...new Map(keys.map(key => [JSON.stringify([key.repository.forge.name, key.repository.path, key.number]), key])).values()];
      await this.#store.write(path, { version: 1, keys: unique, nextRefreshAt: new Date(this.#now().getTime() + (this.options.openIntervalMs ?? 60_000)).toISOString() }, owned);
      return unique;
    });
  }
  async #branch(branch: string, verify: boolean, external?: AbortSignal, inventory?: {branches: ReadonlyMap<string,string>; checkouts: readonly CheckoutEntry[]}): Promise<BranchPullRequests> {
    const signal = AbortSignal.any([this.#controller.signal, AbortSignal.timeout(this.options.requestTimeoutMs ?? 20_000), ...(external ? [external] : [])]);
    signal.throwIfAborted();
    const head = (inventory?.branches ?? localBranches(this.options.checkout)).get(branch);
    if (!head) throw new Error(`Local branch does not exist: ${branch}`);
    const checkouts = (inventory?.checkouts ?? listCheckouts(this.options.checkout)).filter(entry => entry.branch === branch);
    const candidates: PullRequestSnapshot[] = [], errors: string[] = [];
    try {
      for (const key of await this.#keys(branch, verify, signal)) {
        try {
          let observed = await this.#engine.get(key, { signal });
          if (verify && observed.snapshot?.state !== "merged" && observed.source !== "remote") observed = await this.#engine.get(key, { signal, refresh: true });
          if (!observed.fresh || !observed.snapshot) throw new ForgeError(observed.lastError?.message ?? "PR unavailable");
          const pr = observed.snapshot;
          if (!pr.sourceRepository || !pr.sourceBranch) throw new ForgeError("Observed PR source is unknown");
          if (pr.sourceBranch === branch && sameRepository(pr.sourceRepository, this.options.source)) candidates.push(pr);
        } catch (error) { signal.throwIfAborted(); errors.push(String(error)); }
      }
    } catch (error) { signal.throwIfAborted(); errors.push(String(error)); }
    const opens = candidates.filter(pr => pr.state === "open");
    const matching = opens.length ? opens : candidates.filter(pr => pr.headSha && isAncestor(this.options.checkout, pr.headSha, head));
    return { repository: this.options.source, branch, head, candidates, checkouts, ambiguous: matching.length > 1,
      ...(matching.length === 1 && !errors.length ? { selected: matching[0] } : {}), ...(errors.length ? { error: errors.join("; ") } : {}) };
  }
  async getBranch(branch: string, options: { signal?: AbortSignal } = {}): Promise<BranchPullRequests> {
    return this.#branch(branch, false, options.signal);
  }
  async getCheckout(options: { signal?: AbortSignal } = {}): Promise<BranchPullRequests | undefined> {
    const branch = currentBranch(this.options.checkout);
    return branch ? this.getBranch(branch, options) : undefined;
  }
  async localBranches(options: { limit?: number } = {}): Promise<readonly BranchPullRequests[]> {
    const limit = options.limit ?? 20;
    if (!Number.isSafeInteger(limit) || limit < 1) throw new Error("Invalid discovery limit");
    const branches = localBranches(this.options.checkout), checkouts = listCheckouts(this.options.checkout);
    const due = new Map<string, number>(), unavailable = new Map<string, string>();
    for (const [branch] of branches) {
      try {
        const record = await this.#store.read<Association>(this.#path(branch));
        due.set(branch, record ? Date.parse(record.nextRefreshAt) : 0);
      } catch (error) { unavailable.set(branch, String(error)); }
    }
    const selected = new Set([...due].filter(([,at]) => at <= this.#now().getTime()).sort((a,b) => a[1]-b[1] || a[0].localeCompare(b[0])).slice(0,limit).map(([branch])=>branch));
    const signal = AbortSignal.timeout(this.options.requestTimeoutMs ?? 20_000);
    const results: BranchPullRequests[] = [];
    for (const [branch,head] of branches) {
      let error = unavailable.get(branch) ?? "association discovery pending";
      try {
        if (selected.has(branch) || due.get(branch)! > this.#now().getTime()) {
          results.push(await this.#branch(branch, false, signal, {branches,checkouts}));
          continue;
        }
      } catch (failure) {
        this.#controller.signal.throwIfAborted();
        error = String(failure);
      }
      results.push({ repository: this.options.source, branch, head, checkouts: checkouts.filter(entry=>entry.branch===branch), candidates: [], ambiguous: false, error });
    }
    return results;
  }
  async forPullRequest(key: PullRequestKey): Promise<readonly BranchPullRequests[]> {
    const observed = await this.#engine.get(key, { signal: this.#controller.signal });
    const pr = observed.snapshot;
    if (!observed.fresh || !pr) throw new ForgeError("PR unavailable");
    if (!sameRepository(pr.sourceRepository, this.options.source) || !pr.sourceBranch || !localBranches(this.options.checkout).has(pr.sourceBranch)) return [];
    const branch = await this.getBranch(pr.sourceBranch);
    return branch.candidates.some(pr => pr.number === key.number && sameRepository(pr.repository, key.repository)) ? [branch] : [];
  }
  async verify(previous: BranchPullRequests): Promise<BranchPullRequests> {
    if (JSON.stringify(remote(this.options.checkout)) !== JSON.stringify(this.#remote)) throw new ForgeError("Source repository remote changed during PR decision");
    const current = await this.#branch(previous.branch, true);
    if (current.head !== previous.head || JSON.stringify(current.checkouts) !== JSON.stringify(previous.checkouts)) throw new ForgeError("Local branch or checkout changed during PR decision");
    return current;
  }
  close(): void { this.#controller.abort(); }
}
