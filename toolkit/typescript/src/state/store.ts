import { RepoKitError } from "../errors.js";
import { createHash, randomUUID } from "node:crypto";
import { mkdir, open, readFile, readdir, rename, rm } from "node:fs/promises";
import { dirname, join, resolve } from "node:path";
import { setTimeout as delay } from "node:timers/promises";
import lockfile from "proper-lockfile";

export const digest = (value: unknown): string => createHash("sha256").update(JSON.stringify(value)).digest("hex");
export class ObservationStorageError extends RepoKitError {
  constructor(message: string = "", options?: ErrorOptions) { super("state.storage_failed", message, options); this.name = "ObservationStorageError"; }
}
const missing = (error: unknown) => (error as NodeJS.ErrnoException).code === "ENOENT";

/** Local state only. Callers hold the record lock throughout read/refresh/write. */
export class ObservationStore {
  readonly root: string;
  constructor(root: string) {
    if (!root.trim()) throw new Error("repository home must be explicit and nonempty");
    this.root = resolve(root);
  }
  path(...parts: string[]): string { return join(this.root, ...parts); }
  async read<T>(path: string): Promise<T | undefined> {
    try { return JSON.parse(await readFile(path, "utf8")) as T; }
    catch (error) { if (missing(error)) return undefined; throw new ObservationStorageError(`Cannot read repository state ${path}`, { cause: error }); }
  }
  async write(path: string, value: unknown, assertOwned: () => void): Promise<void> {
    const temporary = `${path}.${randomUUID()}.tmp`;
    try {
      await mkdir(dirname(path), { recursive: true, mode: 0o700 });
      const file = await open(temporary, "wx", 0o600);
      try { await file.writeFile(JSON.stringify(value)); await file.sync(); } finally { await file.close(); }
      assertOwned();
      await rename(temporary, path);
      const directory = await open(dirname(path), "r");
      try { await directory.sync(); } finally { await directory.close(); }
    } catch (error) { throw new ObservationStorageError(`Cannot persist repository state ${path}`, { cause: error }); }
    finally { await rm(temporary, { force: true }).catch(() => {}); }
  }
  async locked<T>(path: string, signal: AbortSignal, operation: (assertOwned: () => void) => Promise<T>): Promise<T> {
    await mkdir(dirname(path), { recursive: true, mode: 0o700 });
    let release: (() => Promise<void>) | undefined, compromised: Error | undefined;
    const deadline = Date.now() + 30_000;
    while (!release) {
      signal.throwIfAborted();
      try {
        release = await lockfile.lock(path, { realpath: false, stale: 30_000, update: 10_000, retries: 0,
          onCompromised: error => { compromised = error; } });
      } catch (error) {
        if ((error as NodeJS.ErrnoException).code !== "ELOCKED" || Date.now() >= deadline) {
          throw new ObservationStorageError(`Cannot acquire repository state lock ${path}`, { cause: error });
        }
        await delay(25, undefined, { signal });
      }
    }
    const assertOwned = () => { signal.throwIfAborted(); if (compromised) throw compromised; };
    try { assertOwned(); return await operation(assertOwned); }
    finally { await release(); }
  }
  async observations(connection: string): Promise<string[]> {
    const directories = async (path: string) => {
      try { return (await readdir(path, { withFileTypes: true })).filter(entry => entry.isDirectory()).map(entry => entry.name); }
      catch (error) { if (missing(error)) return []; throw error; }
    };
    const paths: string[] = [];
    for (const repo of await directories(this.path("repositories"))) {
      for (const number of await directories(this.path("repositories", repo, "prs"))) {
        if (/^[1-9][0-9]*$/.test(number)) {
          paths.push(this.path("repositories", repo, "prs", number, "observations", connection+".json"));
        }
      }
    }
    return paths;
  }
}
