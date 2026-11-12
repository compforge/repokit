import { createRequire } from "node:module";
import { open, lstat, readdir, readFile } from "node:fs/promises";
import { join } from "node:path";
import { setTimeout as delay } from "node:timers/promises";

export const missing = (error: unknown): boolean => (error as NodeJS.ErrnoException).code === "ENOENT";
export type Row = Record<string, unknown>;
export function object(value: unknown): Row {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error("Invalid validation history object");
  return value as Row;
}

/**
 * @spec Readers hold Go's advisory lock through replay and evidence reads, without access renewal or pruning.
 * @see {@link component://docs/validation.md}
 */
export async function inspectHistory<T>(directory: string, signal: AbortSignal, read: (events: readonly Row[]) => Promise<T>): Promise<T> {
  signal.throwIfAborted();
  try {
    if (!(await lstat(join(directory, "history"))).isDirectory()) throw new Error("Not a regular history directory");
  } catch (error) { if (missing(error)) return read([]); throw error; }
  if (process.platform !== "darwin" && process.platform !== "linux") throw new Error("Validation history requires Linux or macOS file locking");
  // Keep the native addon external when consumers bundle RepoKit; load only for history access.
  const { flock } = createRequire(import.meta.url)("fs-ext") as typeof import("fs-ext");
  const lock = (fd: number, flags: "exnb" | "un") => new Promise<void>((resolve, reject) => flock(fd, flags, error => error ? reject(error) : resolve()));
  const file = await open(join(directory, "lock"), "a+", 0o600);
  let owned = false;
  try {
    const deadline = AbortSignal.any([signal, AbortSignal.timeout(5_000)]);
    for (;;) {
      deadline.throwIfAborted();
      try { await lock(file.fd, "exnb"); owned = true; break; }
      catch (error) { if (!["EAGAIN", "EWOULDBLOCK"].includes((error as NodeJS.ErrnoException).code ?? "")) throw error; }
      await delay(10, undefined, { signal: deadline });
    }
    return await read(await readEvents(join(directory, "history"), signal));
  } finally {
    try { if (owned) await lock(file.fd, "un"); } finally { await file.close(); }
  }
}

/** Logical tidwall/wal JSON recovery; readers do not rename or remove recovery segments. */
async function readEvents(directory: string, signal: AbortSignal): Promise<Row[]> {
  let segments = (await readdir(directory, { withFileTypes: true }))
    .filter(entry => !entry.isDirectory() && /^\d{20}(?:\.START|\.END)?$/.test(entry.name) && BigInt(entry.name.slice(0, 20)) > 0n)
    .map(entry => entry.name).sort();
  const starts = segments.filter(name => name.endsWith(".START"));
  const ends = segments.filter(name => name.endsWith(".END"));
  if (starts.length > 1 || ends.length > 1 || starts.length && ends.length) throw new Error("Corrupt validation history recovery segments");
  if (starts.length) segments = segments.slice(segments.indexOf(starts[0]));
  if (ends.length) {
    segments = segments.slice(0, segments.indexOf(ends[0]) + 1);
    if (segments.length > 1 && segments.at(-2)!.slice(0, 20) === ends[0].slice(0, 20)) segments.splice(-2, 1);
  }
  const events: Row[] = [];
  let expected: bigint | undefined;
  for (const name of segments) {
    signal.throwIfAborted();
    const first = BigInt(name.slice(0, 20));
    if (expected !== undefined && expected !== first) throw new Error("Noncontiguous validation history segments");
    expected = first;
    const text = await readFile(join(directory, name), { encoding: "utf8", signal });
    if (text && !text.endsWith("\n")) throw new Error("Incomplete validation history record");
    for (const line of text.split("\n").slice(0, -1)) {
      signal.throwIfAborted();
      const record = object(JSON.parse(line));
      if (typeof record.data !== "string") throw new Error("Invalid validation WAL payload");
      let data: string;
      if (record.data.startsWith("+")) data = record.data.slice(1);
      else if (record.data.startsWith("$") && /^(?:[A-Za-z0-9_-]{4})*(?:[A-Za-z0-9_-]{2}==|[A-Za-z0-9_-]{3}=)?$/.test(record.data.slice(1))) data = Buffer.from(record.data.slice(1), "base64url").toString("utf8");
      else throw new Error("Invalid validation WAL encoding");
      events.push(object(JSON.parse(data)));
      expected++;
    }
  }
  return events;
}
