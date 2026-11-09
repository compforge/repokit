import { realpathSync } from "node:fs";
import { lstat, mkdir, readdir, rename, rmdir, symlink, unlink } from "node:fs/promises";
import { dirname, join } from "node:path";
import { randomUUID } from "node:crypto";
import { ObservationStore } from "./storage.js";
import { localKey, namePart, pathDigest, repositoryKey } from "./state-paths.js";
import { checkoutInfo, listCheckouts, localBranches, type CheckoutEntry } from "./git/state.js";
import { runGit } from "./git/operations.js";
import { parseRemoteUrl } from "./git/remote.js";

interface CheckoutRecord { version: 1; clone: string; commonDir: string; path: string; head: string; branch?: string | null; primary: boolean; observedAt: number }
interface CloneBranch { complete: boolean; head: string; commonDir: string; observedAt: number; checkouts: string[] }
interface BranchRecord { version: 1; branch: string; clones: Record<string, CloneBranch> }
export interface BranchObservation { complete: boolean; branch: string; head: string; checkouts: readonly CheckoutEntry[]; observedAt: number; source: "cache" | "git" }
const missing = (error: unknown) => (error as NodeJS.ErrnoException).code === "ENOENT";
async function children(path: string): Promise<string[]> { try { return await readdir(path); } catch(error) { if(missing(error)) return []; throw error; } }
function location(repo: string) {
  const info = checkoutInfo(repo);
  const origin = runGit(repo,["config","--get","remote.origin.url"],5000,true);
  if (!origin.ok && !(origin.code === 1 && !origin.stderr)) throw new Error(`Cannot observe origin: ${origin.stderr}`);
  const remote = origin.ok ? parseRemoteUrl(origin.stdout.trim()) : undefined;
  const forge = remote ? ({ "github.com":"github", "gitlab.com":"gitlab" }[remote.host] ?? remote.host) : "";
  return { info, key: remote ? repositoryKey(forge,remote.path) : localKey(info.commonDir), identity: remote ? {forge:{name:forge},path:remote.path} : {local:info.commonDir} };
}
/** Repository-local observations share storage with Forge facts and execution history.
 * @spec One clone never replaces another clone's branch heads or checkout records.
 * Persisted topology is not authority to mutate Git resources.
 * @see {@link component://docs/state.md}
 */
export class RepositoryState {
  readonly store: ObservationStore;
  constructor(stateDir: string) { this.store = new ObservationStore(stateDir); }
  async branch(repo: string, branch: string, options: {maxAgeMs?: number; signal?: AbortSignal} = {}): Promise<BranchObservation | undefined> {
    const maxAge = options.maxAgeMs ?? 0, signal = options.signal ?? new AbortController().signal;
    if (!Number.isFinite(maxAge) || maxAge < 0) throw new Error("maxAgeMs must be nonnegative and finite");
    const {key,info} = location(repo), directory = this.store.path("repositories",key), clone = pathDigest(info.commonDir);
    const read = (source: "cache"|"git") => this.store.locked(join(directory,"topology"),signal,async () => {
      const record = await this.store.read<BranchRecord>(join(directory,"branches",namePart(branch),"observation.json"));
      if (!record) return undefined;
      if (record.version !== 1 || record.branch !== branch) throw new Error("Invalid branch observation identity");
      const observed = record.clones[clone]; if (!observed) return undefined;
      const checkouts: CheckoutEntry[] = [];
      for (const checkout of observed.checkouts) {
        if (!/^[0-9a-f]{32}$/.test(checkout)) throw new Error("Invalid checkout observation key");
        const row = await this.store.read<CheckoutRecord>(join(directory,"checkouts",checkout,"observation.json"));
        if (!row || row.version !== 1 || row.clone !== clone || row.branch !== branch || row.observedAt !== observed.observedAt) throw new Error("Incomplete checkout observation; observe the repository again");
        checkouts.push({path:row.path,sha:row.head,branch,primary:row.primary});
      }
      return {complete:observed.complete === true,branch,head:observed.head,checkouts,observedAt:observed.observedAt,source};
    });
    const cached = await read("cache");
    if (cached && Date.now()-cached.observedAt*1000 >= 0 && Date.now()-cached.observedAt*1000 < maxAge) return cached;
    await this.observe(repo,{signal});
    return read("git");
  }
  async observe(repo: string, options: {reconcile?: boolean; signal?: AbortSignal} = {}): Promise<string> {
    const {key,info,identity} = location(repo), directory = this.store.path("repositories",key), clone = pathDigest(info.commonDir);
    const signal = options.signal ?? new AbortController().signal;
    await this.store.locked(join(directory,"topology"),signal,async owned => {
      const branches = localBranches(repo), entries = listCheckouts(repo).map(entry => {
        if(!entry.path)return entry;
        try{return {...entry,path:realpathSync(entry.path)};}catch(error){if(missing(error))return entry;throw error;}
      }), at = Date.now()/1000;
      if (location(repo).key !== key) throw new Error("Repository identity changed during observation");
      const rows = new Map<string,CheckoutRecord>(entries.filter(e => e.path).map(e => [pathDigest(e.path!), {version:1,clone,commonDir:info.commonDir,path:e.path!,head:e.sha,branch:e.branch ?? null,primary:e.primary,observedAt:at}]));
      const previous = new Map<string,CheckoutRecord>();
      for (const checkout of await children(join(directory,"checkouts"))) {
        const row = await this.store.read<CheckoutRecord>(join(directory,"checkouts",checkout,"observation.json"));
        if (row?.clone === clone) previous.set(checkout,row);
      }
      await this.store.write(join(directory,"repository.json"),identity,owned);
      for (const [checkout,row] of rows) {
        const root = join(directory,"checkouts",checkout);
        await this.store.write(join(root,"observation.json"),row,owned);
        await this.link(join(root,"path"),row.path,owned);
      }
      const names = new Set(branches.keys()), known = new Set([...previous.keys(),...rows.keys()]);
      for (const name of await children(join(directory,"branches"))) {
        const record = await this.store.read<BranchRecord>(join(directory,"branches",name,"observation.json"));
        if (record?.clones[clone]) names.add(record.branch);
      }
      for (const branch of names) {
        const root = join(directory,"branches",namePart(branch)), path = join(root,"observation.json");
        const record = await this.store.read<BranchRecord>(path) ?? {version:1,branch,clones:{}};
        if (record.version !== 1 || record.branch !== branch) throw new Error("Branch identity collision");
        const checkouts = [...rows].filter(([,row])=>row.branch===branch).map(([key])=>key).sort();
        if (branches.has(branch)) record.clones[clone] = {complete:entries.filter(e=>e.branch===branch).every(e=>e.path !== undefined),head:branches.get(branch)!,commonDir:info.commonDir,observedAt:at,checkouts};
        else delete record.clones[clone];
        await this.store.write(path,record,owned);
        for (const checkout of known) {
          const link = join(root,"worktrees",checkout);
          if (checkouts.includes(checkout)) await this.link(link,"../../../checkouts/"+checkout,owned);
          else { try { if ((await lstat(link)).isSymbolicLink()) { owned(); await unlink(link); } } catch(error) { if(!missing(error)) throw error; } }
        }
        if (options.reconcile && !Object.keys(record.clones).length && !(await children(root)).some(n=>!["worktrees","observation.json"].includes(n)) && !(await children(join(root,"worktrees"))).length) {
          owned(); try {await rmdir(join(root,"worktrees"));} catch(error) {if(!missing(error))throw error;}
          await unlink(path); await rmdir(root);
        }
      }
      if (options.reconcile && entries.every(e=>e.path)) for (const [checkout,row] of previous) {
        if (rows.has(checkout)) continue;
        try { await lstat(row.path); continue; } catch(error) {if(!missing(error))throw error;}
        const root = join(directory,"checkouts",checkout);
        if (!(await lstat(root)).isDirectory()) throw new Error("Checkout state is not a directory");
        const names = await children(root);
        if (names.some(n=>!["path","observation.json"].includes(n))) throw new Error(`Unrecognized checkout state retained: ${root}`);
        if (names.includes("path") && !(await lstat(join(root,"path"))).isSymbolicLink()) throw new Error("Checkout path marker is not a symlink");
        owned(); for(const name of names) await unlink(join(root,name)); await rmdir(root);
      }
    });
    return directory;
  }
  private async link(path: string,target: string,owned:()=>void):Promise<void> {
    await mkdir(dirname(path),{recursive:true,mode:0o700});
    try {if(!(await lstat(path)).isSymbolicLink())throw new Error(`Refusing to replace non-link ${path}`);}catch(error){if(!missing(error))throw error;}
    const temporary = path+"."+randomUUID()+".tmp";
    try {await symlink(target,temporary);owned();await rename(temporary,path);}finally{try{await unlink(temporary);}catch(error){if(!missing(error))throw error;}}
  }
}
