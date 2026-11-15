import { basename } from "node:path";
import { resolveHome } from "./home.js";
import { ObservationStore } from "./store.js";
export interface CleanupPolicy {
    repository: number;
    pr: number;
    comment: number;
    branch: number;
    validation: number;
}
export const defaultCleanupPolicy = (): CleanupPolicy => ({ repository: 100, pr: 100, comment: 5000, branch: 200, validation: 1000 });
const object = (value: unknown): value is Record<string, unknown> => !!value && typeof value === "object" && !Array.isArray(value);
/** @spec Omission inherits, zero minimizes retained eligible caches, invalid limits fail. */
export async function loadCleanupPolicy(home?: string, key = ""): Promise<CleanupPolicy> {
    const store = new ObservationStore(resolveHome(home)), policy = defaultCleanupPolicy();
    if (key && (basename(key) !== key || key === "." || key === ".."))
        throw new Error("Invalid repository key");
    const paths = [store.path("config.json"), ...(key ? [store.path("repositories", key, "config.json")] : [])];
    for (const path of paths) {
        const document = await store.read<unknown>(path);
        if (document === undefined)
            continue;
        if (!object(document))
            throw new Error("Configuration must be an object");
        const section = document.cleanup ?? {};
        if (document.cleanup === null || !object(section))
            throw new Error("Invalid cleanup policy");
        for (const [name, count] of Object.entries(section)) {
            if (!Object.hasOwn(policy, name) || !object(count) || Object.keys(count).some(k => k !== "max_count"))
                throw new Error("Invalid resource policy");
            if (Object.hasOwn(count, "max_count")) {
                const value = count.max_count;
                if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0)
                    throw new Error("Invalid max_count");
                policy[name as keyof CleanupPolicy] = value;
            }
        }
    }
    return policy;
}
