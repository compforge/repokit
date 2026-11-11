import type { CheckoutInfo } from "../git/state.js";
import { pathDigest } from "./identity.js";
import type { ObservationStore } from "./store.js";

interface Activity {
  version: number;
  lastAccessAt: number;
  checkouts: Record<string, {path: string; commonDir: string; lastAccessAt: number}>;
}

/**
 * @spec Only use of the selected checkout renews its activity; inventory and maintenance do not.
 * @see {@link component://docs/cleanup.md}
 */
export async function recordAccess(store: ObservationStore, key: string, signal: AbortSignal, checkout?: CheckoutInfo): Promise<void> {
  await store.locked(store.path("locks", "activity", key), signal, async owned => {
    const path = store.path("repositories", key, "activity.json");
    const record = await store.read<Activity>(path) ?? {version: 1, lastAccessAt: 0, checkouts: {}};
    if (record.version !== 1 || !Number.isFinite(record.lastAccessAt) || record.lastAccessAt < 0 || !record.checkouts || Array.isArray(record.checkouts) || typeof record.checkouts !== "object") throw new Error("Invalid repository activity record");
    const now = Date.now() / 1000;
    record.lastAccessAt = Math.max(record.lastAccessAt, now);
    if (checkout) record.checkouts[pathDigest(checkout.root)] = {path: checkout.root, commonDir: checkout.commonDir, lastAccessAt: now};
    await store.write(path, record, owned);
  });
}
