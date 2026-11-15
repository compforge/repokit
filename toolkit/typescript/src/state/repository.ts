import { RepoKitError } from "../errors.js";
import { recordAccess } from "./activity.js";
import { resolveHome } from "./home.js";
import { realpathSync } from "node:fs";
import { lstat, readdir, rmdir, unlink } from "node:fs/promises";
import { join } from "node:path";
import { ObservationStore } from "./store.js";
import { localKey, namePart, pathDigest, repositoryKey } from "./identity.js";
import { checkoutInfo, listCheckouts, localBranches, type CheckoutEntry } from "../git/state.js";
import { runGit } from "../git/operations.js";
import { parseRemoteUrl } from "../git/remote.js";

interface CheckoutRecord { version: 1; clone: string; commonDir: string; path: string; head: string; branch?: string | null; primary: boolean; observedAt: number }
interface CloneBranch { complete: boolean; head: string; commonDir: string; observedAt: number; checkouts: string[] }
interface BranchRecord extends CloneBranch { version: 1; branch: string }
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
class IncompleteObservation extends RepoKitError {
  constructor(message: string = "", options?: ErrorOptions) { super("state.incomplete", message, options); this.name = "IncompleteObservation"; }
}
/** Repository-local observations share storage with Forge facts and execution history.
 * @spec One clone never replaces another clone's branch heads or checkout records.
 * Persisted topology is not authority to mutate Git resources.
 * @see {@link component://docs/home.md}
 */
export class RepositoryState {
  readonly store: ObservationStore;
  constructor(home?: string) { this.store = new ObservationStore(resolveHome(home)); }
  async branch(repo: string, branch: string, options: {maxAgeMs?: number; signal?: AbortSignal} = {}): Promise<BranchObservation | undefined> {
    const maxAge = options.maxAgeMs ?? 0, signal = options.signal ?? new AbortController().signal;
    if (!Number.isFinite(maxAge) || maxAge < 0) throw new Error("maxAgeMs must be nonnegative and finite");
    const {key,info} = location(repo), directory = this.store.path("repositories",key), clone = pathDigest(info.commonDir);
    await recordAccess(this.store,key,signal,info,["clones",clone,"branches",namePart(branch)]);
    const read = (source: "cache"|"git") => this.store.locked(join(directory,"topology"),signal,async () => {
      const record = await this.store.read<BranchRecord>(join(directory,"clones",clone,"branches",namePart(branch),"branch.json"));
      if (!record) return undefined;
      if (record.version !== 1 || record.branch !== branch) throw new Error("Invalid branch observation identity");
      const observed = record; if (!observed.head) return undefined;
      const checkouts: CheckoutEntry[] = [];
      for (const checkout of observed.checkouts) {
        if (!/^[0-9a-f]{32}$/.test(checkout)) throw new Error("Invalid checkout observation key");
        const row = await this.store.read<CheckoutRecord>(join(directory,"clones",clone,"checkouts",checkout,"checkout.json"));
        if (!row || row.version !== 1 || row.clone !== clone || row.branch !== branch || row.observedAt !== observed.observedAt) throw new IncompleteObservation("Incomplete checkout observation");
        checkouts.push({path:row.path,sha:row.head,branch,primary:row.primary});
      }
      return {complete:observed.complete === true,branch,head:observed.head,checkouts,observedAt:observed.observedAt,source};
    });
    let cached: BranchObservation | undefined;
    if(maxAge>0) {try {cached=await read("cache");} catch(error) {if(!(error instanceof IncompleteObservation))throw error;}}
    if (cached && Date.now()-cached.observedAt*1000 >= 0 && Date.now()-cached.observedAt*1000 < maxAge) return cached;
    await this.observeBranch(repo,{signal},branch);
    return read("git");
  }
  async observe(repo: string, options: {reconcile?: boolean; signal?: AbortSignal; recordUse?: boolean} = {}): Promise<string> {
    if(options.recordUse !== false){const {key,info}=location(repo);await recordAccess(this.store,key,options.signal ?? new AbortController().signal,info);}
    return this.observeBranch(repo,options);
  }
  private async observeBranch(repo: string, options: {reconcile?: boolean; signal?: AbortSignal}, selectedBranch?: string): Promise<string> {
    const {key,info,identity} = location(repo), directory = this.store.path("repositories",key), clone = pathDigest(info.commonDir);
    const signal = options.signal ?? new AbortController().signal;
    await this.store.locked(join(directory,"topology"),signal,async owned => {
      const branches = localBranches(repo), entries = listCheckouts(repo).map(entry => {
        if(!entry.path)return entry;
        try{return {...entry,path:realpathSync(entry.path)};}catch(error){if(missing(error))return entry;throw error;}
      }), at = Date.now()/1000;
      if (location(repo).key !== key) throw new Error("Repository identity changed during observation");
      const rows = new Map<string,CheckoutRecord>(entries.filter(e => e.path && (selectedBranch === undefined || e.branch === selectedBranch)).map(e => [pathDigest(e.path!), {version:1,clone,commonDir:info.commonDir,path:e.path!,head:e.sha,branch:e.branch ?? null,primary:e.primary,observedAt:at}]));
      const previous = new Map<string,CheckoutRecord>();
      for (const checkout of selectedBranch === undefined ? await children(join(directory,"clones",clone,"checkouts")) : []) {
        const row = await this.store.read<CheckoutRecord>(join(directory,"clones",clone,"checkouts",checkout,"checkout.json"));
        if (row?.clone === clone) previous.set(checkout,row);
      }
      await this.store.write(join(directory,"repository.json"),identity,owned);
      await this.store.write(join(directory,"clones",clone,"clone.json"),{version:1,commonDir:info.commonDir},owned);
      for (const [checkout,row] of rows) {
        const root = join(directory,"clones",clone,"checkouts",checkout);
        await this.store.write(join(root,"checkout.json"),row,owned);
      }
      const names = new Set(selectedBranch === undefined ? branches.keys() : [selectedBranch]);
      for (const name of selectedBranch === undefined ? await children(join(directory,"clones",clone,"branches")) : []) {
        const record = await this.store.read<BranchRecord>(join(directory,"clones",clone,"branches",name,"branch.json"));
        if (record?.commonDir === info.commonDir) names.add(record.branch);
      }
      for (const branch of names) {
        const root = join(directory,"clones",clone,"branches",namePart(branch)), path = join(root,"branch.json");
        const record = await this.store.read<BranchRecord>(path) ?? {version:1,branch,commonDir:info.commonDir,head:"",complete:true,observedAt:at,checkouts:[]};
        if (record.version !== 1 || record.branch !== branch) throw new Error("Branch identity collision");
        const checkouts = [...rows].filter(([,row])=>row.branch===branch).map(([key])=>key).sort();
        if (branches.has(branch)) Object.assign(record, {complete:entries.filter(e=>e.branch===branch).every(e=>e.path !== undefined),head:branches.get(branch)!,commonDir:info.commonDir,observedAt:at,checkouts});
        else Object.assign(record,{head:"",checkouts:[],observedAt:at});
        await this.store.write(path,record,owned);
        if(options.reconcile && !record.head) {
          owned(); await unlink(path);
          if (!(await children(root)).length) await rmdir(root);
        }
      }
      if (options.reconcile && entries.every(e=>e.path)) for (const [checkout,row] of previous) {
        if (rows.has(checkout)) continue;
        try { await lstat(row.path); continue; } catch(error) {if(!missing(error))throw error;}
        const root = join(directory,"clones",clone,"checkouts",checkout);
        if (!(await lstat(root)).isDirectory()) throw new Error("Checkout state is not a directory");
        const names = await children(root);
        if (names.some(n=>!["checkout.json","access.json"].includes(n))) throw new Error(`Unrecognized checkout state retained: ${root}`);
        owned(); for(const name of names) await unlink(join(root,name)); await rmdir(root);
      }
    });
    return directory;
  }
}
