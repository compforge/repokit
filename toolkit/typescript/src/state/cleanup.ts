import { lstat, readdir, rm } from "node:fs/promises";
import { basename, isAbsolute, join, relative, sep } from "node:path";
import { resolveHome } from "./home.js";
import { pathDigest } from "./identity.js";
import { loadCleanupPolicy, type CleanupPolicy } from "./policy.js";
import { ObservationStore } from "./store.js";
export interface CacheCleanOptions {
    home?: string;
    repositoryKey: string;
    cloneKey?: string;
    prNumber?: number;
    policy?: CleanupPolicy;
    dryRun?: boolean;
    expire?: boolean;
    accessBefore?: number;
    merged?: ReadonlySet<number>;
    signal?: AbortSignal;
}
export interface CacheAction {
    action: string;
    status: "planned" | "completed" | "skipped";
    reason: string;
    path: string;
    estimatedBytes?: number;
}
export interface ResourceCount {
    repository: string;
    resource: string;
    before: number;
    after: number;
    max_count: number;
    over_budget: number;
}
export interface CacheCleanReport {
    actions: CacheAction[];
    counts: ResourceCount[];
    errors: string[];
    affected: string[];
    dry_run: boolean;
}
const missing = (e: unknown) => (e as NodeJS.ErrnoException).code === "ENOENT";
async function directories(path: string): Promise<string[]> {
    try {
        const info = await lstat(path);
        if (!info.isDirectory() || info.isSymbolicLink())
            throw new Error("Resource collection must be a real directory");
        return (await readdir(path, { withFileTypes: true })).filter(e => e.isDirectory() && !e.isSymbolicLink()).map(e => join(path, e.name)).sort();
    }
    catch (e) {
        if (missing(e))
            return [];
        throw e;
    }
}
async function exists(path: string): Promise<boolean> { try {
    await lstat(path);
    return true;
}
catch (e) {
    if (missing(e))
        return false;
    throw e;
} }
async function bytes(path: string): Promise<number> { const info = await lstat(path); if (info.isSymbolicLink())
    return 0; if (!info.isDirectory())
    return info.size; let size = 0; for (const name of await readdir(path))
    size += await bytes(join(path, name)); return size; }
class Cleanup {
    readonly store: ObservationStore;
    readonly root: string;
    readonly signal: AbortSignal;
    readonly before: number;
    readonly report: CacheCleanReport;
    owned: () => void = () => { };
    constructor(readonly options: CacheCleanOptions, readonly policy: CleanupPolicy) {
        if (!options.repositoryKey || [options.repositoryKey, options.cloneKey].some(k => k !== undefined && (!k || basename(k) !== k || k === "." || k === "..")))
            throw new Error("Invalid resource key");
        if (options.prNumber !== undefined && (!Number.isSafeInteger(options.prNumber) || options.prNumber < 0))
            throw new Error("Invalid PR number");
        if (Object.values(policy).some(n => !Number.isSafeInteger(n) || n < 0))
            throw new Error("Invalid cleanup policy");
        this.store = new ObservationStore(resolveHome(options.home));
        this.root = this.store.path("repositories", options.repositoryKey);
        this.signal = options.signal ?? new AbortController().signal;
        this.before = options.accessBefore ?? Date.now() / 1000;
        this.report = { actions: [], counts: [], errors: [], affected: [], dry_run: options.dryRun ?? false };
    }
    async access(path: string): Promise<number> {
        const r = await this.store.read<{
            version: number;
            lastAccessAt: number;
        }>(join(path, "access.json"));
        if (!r)
            return 0;
        if (r.version !== 1 || !Number.isFinite(r.lastAccessAt) || r.lastAccessAt < 0)
            throw new Error("Invalid resource access");
        return r.lastAccessAt;
    }
    async remove(path: string, resource: string, reason: string): Promise<void> {
        this.signal.throwIfAborted();
        if (this.report.affected.some(p => path === p || path.startsWith(p + sep)))
            return;
        if (!await exists(path))
            return;
        const info = await lstat(path), rel = relative(this.root, path);
        if (info.isSymbolicLink() || !rel || rel === ".." || rel.startsWith(".." + sep) || isAbsolute(rel))
            throw new Error("Invalid cleanup boundary");
        const estimatedBytes = await bytes(path);
        if (!this.options.dryRun) {
            this.owned();
            await rm(path, { recursive: true });
        }
        this.report.actions.push({ action: "remove_" + resource, status: this.options.dryRun ? "planned" : "completed", reason, path, estimatedBytes });
        this.report.affected.push(path);
    }
    async prPaths(): Promise<string[]> { return (await directories(join(this.root, "prs"))).filter(p => /^[1-9][0-9]*$/.test(basename(p)) && (!this.options.prNumber || Number(basename(p)) === this.options.prNumber)); }
    async branches(): Promise<string[]> { const paths: string[] = []; for (const clone of await directories(join(this.root, "clones"))) {
        if (this.options.cloneKey && basename(clone) !== this.options.cloneKey)
            continue;
        paths.push(...await directories(join(clone, "branches")));
    } return paths; }
    async tracked(path: string): Promise<boolean> {
        for (const name of await readdir(path)) {
            if (!name.endsWith(".json") || name === "access.json")
                continue;
            const r = await this.store.read<{
                version: number;
                scopeIds?: string[];
                observation?: {
                    nextRefreshAt?: string | null;
                };
            }>(join(path, name));
            if (!r || r.version !== 1)
                throw new Error("Invalid PR record");
            if (r.scopeIds?.length || r.observation?.nextRefreshAt != null)
                return true;
        }
        return false;
    }
    async budget(resource: "pr" | "comment" | "branch", maximum: number): Promise<void> {
        const paths = resource === "branch" ? await this.branches() : await this.prPaths(), rows: {
            path: string;
            access: number;
            count: number;
            protection: string;
        }[] = [];
        for (let path of paths) {
            this.signal.throwIfAborted();
            let count = 1, protection = "";
            if (resource === "comment") {
                if (this.report.affected.includes(path))
                    continue;
                path = join(path, "comments");
                count = 0;
                for (const comment of await directories(path))
                    if ((await readdir(comment, { withFileTypes: true })).some(e => e.isFile() && e.name.endsWith(".json")))
                        count++;
                if (!count)
                    continue;
            }
            else if (resource === "pr" && await this.tracked(path))
                protection = "observation_tracked";
            else if (resource === "branch" && !await exists(join(path, "branch.json")) && !await exists(join(path, "associations")))
                continue;
            rows.push({ path, count, access: await this.access(path), protection });
        }
        rows.sort((a, b) => a.access - b.access || (a.path < b.path ? -1 : a.path > b.path ? 1 : 0));
        const count: ResourceCount = { repository: this.options.repositoryKey, resource, before: rows.reduce((n, r) => n + r.count, 0), after: 0, max_count: maximum, over_budget: 0 };
        count.after = count.before;
        let cutoff = rows.length, retained = 0;
        while (cutoff > 0 && retained < maximum) {
            if (retained > 0 && retained + rows[cutoff - 1]!.count > maximum)
                break;
            cutoff--;
            retained += rows[cutoff]!.count;
        }
        for (const [index, row] of rows.entries()) {
            this.signal.throwIfAborted();
            const merged = resource === "comment" && this.options.merged?.has(Number(basename(join(row.path, ".."))));
            if (!this.options.expire && index >= cutoff && !merged)
                continue;
            if (row.access > this.before)
                row.protection = "access_changed";
            if (row.protection) {
                this.report.actions.push({ action: "retain_" + resource, status: "skipped", reason: row.protection, path: row.path });
                continue;
            }
            const reason = merged ? "pr_merged" : this.options.expire ? "inactive" : "max_count";
            try {
                if (resource === "branch") {
                    for (const name of ["branch.json", "associations"])
                        await this.remove(join(row.path, name), "branch_cache", reason);
                }
                else
                    await this.remove(row.path, resource, reason);
                if (resource === "pr")
                    for (const branch of await this.branches())
                        await this.remove(join(branch, "associations"), "branch_associations", "pr_evicted");
                count.after -= row.count;
            }
            catch (e) {
                this.signal.throwIfAborted();
                this.report.errors.push(String(e));
            }
        }
        count.over_budget = Math.max(0, count.after - maximum);
        this.report.counts.push(count);
    }
    async clones(): Promise<void> {
        for (const clone of await directories(join(this.root, "clones"))) {
            this.signal.throwIfAborted();
            if (this.options.cloneKey && basename(clone) !== this.options.cloneKey)
                continue;
            const r = await this.store.read<{
                version: number;
                commonDir: string;
            }>(join(clone, "clone.json"));
            if (!r)
                continue;
            if (r.version !== 1 || typeof r.commonDir !== "string" || !isAbsolute(r.commonDir) || pathDigest(r.commonDir) !== basename(clone))
                throw new Error("Invalid clone registration");
            for (const checkout of await directories(join(clone, "checkouts"))) {
                const row = await this.store.read<{
                    path: string;
                    clone: string;
                    commonDir: string;
                }>(join(checkout, "checkout.json"));
                if (!row)
                    continue;
                if (row.commonDir !== r.commonDir || row.clone !== basename(clone) || typeof row.path !== "string" || !isAbsolute(row.path) || pathDigest(row.path) !== basename(checkout))
                    throw new Error("Invalid checkout registration");
                if (!await exists(row.path))
                    await this.remove(checkout, "checkout_registration", "path_missing");
            }
            if (!await exists(r.commonDir))
                await this.remove(join(clone, "clone.json"), "clone_registration", "path_missing");
        }
    }
    async run(domain: string): Promise<CacheCleanReport> {
        this.signal.throwIfAborted();
        const parent = join(this.root, "..");
        if (await exists(parent) && !(await lstat(parent)).isDirectory())
            throw new Error("Repositories must be a real directory");
        if (!await exists(this.root))
            return this.report;
        if (!(await lstat(this.root)).isDirectory())
            throw new Error("Repository cache must be a real directory");
        const lockPath = this.store.lockPath(join(this.root, "cleanup")) + ".lock";
        if (await exists(lockPath) && Date.now() - (await lstat(lockPath)).mtimeMs <= 30000) {
            this.report.actions.push({ action: "retain_repository", status: "skipped", reason: "state_locked", path: this.root });
            return this.report;
        }
        return this.store.locked(this.store.path("locks", "activity", this.options.repositoryKey), this.signal, async () => this.store.locked(join(this.root, "cleanup"), this.signal, async (owned) => {
            this.owned = owned;
            if (domain === "repository" && await this.access(this.root) > this.before) {
                this.report.actions.push({ action: "retain_repository", status: "skipped", reason: "access_changed", path: this.root });
                return this.report;
            }
            for (const resource of ["pr", "comment", "branch"] as const) {
                if (domain !== resource && domain !== "repository")
                    continue;
                try {
                    await this.budget(resource, this.policy[resource]);
                }
                catch (e) {
                    this.signal.throwIfAborted();
                    this.report.errors.push(resource + ": " + String(e));
                }
            }
            if (domain === "clone" || domain === "repository")
                try {
                    await this.clones();
                }
                catch (e) {
                    this.signal.throwIfAborted();
                    this.report.errors.push("clone: " + String(e));
                }
            if (domain === "repository" && this.options.expire)
                for (const name of ["metadata.json", "remotes"])
                    await this.remove(join(this.root, name), "repository_cache", "inactive");
            return this.report;
        }));
    }
}
async function clean(options: CacheCleanOptions, domain: string): Promise<CacheCleanReport> { return new Cleanup(options, options.policy ?? await loadCleanupPolicy(options.home, options.repositoryKey)).run(domain); }
/** @spec Independent cleanup and global composition use identical ownership and protection rules. */
export const cleanRepository = (options: CacheCleanOptions): Promise<CacheCleanReport> => clean(options, "repository");
export const cleanPullRequests = (options: CacheCleanOptions): Promise<CacheCleanReport> => clean(options, "pr");
export const cleanComments = (options: CacheCleanOptions): Promise<CacheCleanReport> => clean(options, "comment");
export const cleanBranches = (options: CacheCleanOptions): Promise<CacheCleanReport> => clean(options, "branch");
export const cleanClones = (options: CacheCleanOptions): Promise<CacheCleanReport> => clean(options, "clone");
