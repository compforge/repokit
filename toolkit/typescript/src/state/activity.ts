import type { CheckoutInfo } from "../git/state.js";
import { basename, join } from "node:path";
import { pathDigest } from "./identity.js";
import type { ObservationStore } from "./store.js";

export async function touchAccess(store: ObservationStore, directory: string, owned: () => void): Promise<void> {
  const record = await store.read<{version:number;lastAccessAt:number}>(join(directory,"access.json")) ?? {version:1,lastAccessAt:0};
  if (record.version !== 1 || !Number.isFinite(record.lastAccessAt) || record.lastAccessAt < 0) throw new Error("Invalid resource access record");
  await store.write(join(directory,"access.json"),{version:1,lastAccessAt:Math.max(record.lastAccessAt,Date.now()/1000)},owned);
}
/** @spec Explicit use renews only the selected object and its owners, never siblings. */
export async function recordAccess(store: ObservationStore, key: string, signal: AbortSignal, checkout?: CheckoutInfo, relative: readonly string[] = []): Promise<void> {
  if ([key,...relative].some(p=>!p || basename(p)!==p || p==="." || p==="..")) throw new Error("Invalid resource path");
  await store.locked(store.path("locks","activity",key),signal,async ()=>store.locked(store.path("repositories",key,"access"),signal,async owned=>{
    const directory = store.path("repositories",key);
    await touchAccess(store,directory,owned);
    if(relative.length>2 && ["prs","clones"].includes(relative[0]!))await touchAccess(store,join(directory,...relative.slice(0,2)),owned);
    if(relative.length) await touchAccess(store,join(directory,...relative),owned);
    if(checkout){
      const clone=join(directory,"clones",pathDigest(checkout.commonDir)), path=join(clone,"checkouts",pathDigest(checkout.root));
      await store.write(join(clone,"clone.json"),{version:1,commonDir:checkout.commonDir},owned);
      const row = await store.read<Record<string,unknown>>(join(path,"checkout.json")) ?? {};
      await store.write(join(path,"checkout.json"),{...row,version:1,clone:pathDigest(checkout.commonDir),commonDir:checkout.commonDir,path:checkout.root},owned);
      await touchAccess(store,clone,owned); await touchAccess(store,path,owned);
    }
  }));
}
