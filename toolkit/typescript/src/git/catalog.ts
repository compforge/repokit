import { checkoutEnvironment } from "./environment.js";
import { execFile } from "node:child_process";
import { constants } from "node:fs";
import { lstat, open, realpath } from "node:fs/promises";
import { dirname, join } from "node:path";
import { compare, needsContent, validPath, type Catalog } from "../layout.js";

const maxFileBytes = 2 << 20;
const maxTotalBytes = 128 << 20;

/** Only Git is launched. One bounded process per catalog/batch, never per source file. */
export class Git {
  constructor(readonly root: string, readonly signal: AbortSignal) {}

  run(args: string[], input?: string, allowMissing = false, maxBuffer = 16 << 20): Promise<Buffer> {
    this.signal.throwIfAborted();
    return new Promise((resolve, reject) => {
      const child = execFile("git", ["-C", this.root, ...args], {
        env: checkoutEnvironment(), encoding: "buffer", signal: this.signal, maxBuffer, killSignal: "SIGKILL",
      }, (error, stdout) => {
        if (error && !(allowMissing && error.code === 1)) reject(error);
        else resolve(stdout);
      });
      // A failed/aborted Git process may close stdin before consuming a batch.
      // The process callback owns reporting that failure.
      child.stdin?.on("error", () => {});
      child.stdin?.end(input);
    });
  }

  async gitlinkOID(name: string, oid: string): Promise<string> {
    const path = join(this.root, name);
    try { await lstat(join(path, ".git")); }
    catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") return oid; throw error; }
    if (await realpath(path) !== path) throw new Error(`symlinked gitlink checkout: ${name}`);
    const child = new Git(path, this.signal);
    const root = (await child.run(["rev-parse", "--show-toplevel"])).toString().replace(/\n$/, "");
    if (root !== path) throw new Error(`invalid gitlink checkout: ${name}`);
    return (await child.run(["rev-parse", "--verify", "HEAD"])).toString().trim();
  }

  async catalog(head: string, staged: boolean): Promise<Catalog> {
    const committed = head !== "" || staged;
    const output = await this.run(head ? ["ls-tree", "-r", "-z", "--full-tree", head] : ["ls-files", "--stage", "-z"]);
    const entries = new Map<string, string>();
    for (const entry of output.toString().split("\0")) {
      if (!entry) continue;
      const tab = entry.indexOf("\t");
      const name = entry.slice(tab + 1);
      const fields = entry.slice(0, tab).split(/\s+/);
      if (tab < 0 || fields.length !== 3 || !validPath(name)) throw new Error("invalid Git catalog entry");
      if (!head && fields[2] !== "0") throw new Error(`unmerged index entry: ${name}`);
      if (fields[0] === "160000") continue;
      if (fields[0] === "120000" && committed) {
        if (needsContent(name)) throw new Error(`metadata ${name} is a symlink`);
        continue;
      }
      if (!["100644", "100755", "120000"].includes(fields[0])) throw new Error(`unsupported Git entry mode for ${name}`);
      entries.set(name, fields[head ? 2 : 1]);
    }
    if (!committed) {
      const others = await this.run(["ls-files", "-z", "--others", "--exclude-standard"]);
      for (const name of others.toString().split("\0")) {
        if (!name || name.endsWith("/")) continue;
        if (!validPath(name)) throw new Error("unsafe repository path");
        entries.set(name, "");
      }
    }
    if (entries.size > 10_000) throw new Error("repository exceeds 10000 files");
    const files = new Map<string, Buffer | null>();
    const selected: [string, string][] = [];
    let total = 0;
    for (const name of [...entries.keys()].sort(compare)) {
      this.signal.throwIfAborted();
      if (committed) {
        files.set(name, null);
        if (needsContent(name)) selected.push([name, entries.get(name)!]);
        continue;
      }
      const full = join(this.root, name);
      let info;
      try { info = await lstat(full); }
      catch (error) { if ((error as NodeJS.ErrnoException).code === "ENOENT") continue; throw error; }
      if (!info.isFile()) {
        if (needsContent(name)) throw new Error(`metadata ${name} is not a regular file`);
        continue;
      }
      if (await realpath(dirname(full)) !== dirname(full)) {
        if (needsContent(name)) throw new Error(`metadata ${name} has a symlinked directory`);
        continue;
      }
      files.set(name, null);
      if (!needsContent(name)) continue;
      if (info.size > maxFileBytes) throw new Error(`metadata ${name} exceeds ${maxFileBytes} bytes`);
      const file = await open(full, constants.O_RDONLY | constants.O_NOFOLLOW | constants.O_NONBLOCK);
      try {
        if (!(await file.stat()).isFile()) throw new Error(`metadata ${name} is not a regular file`);
        // Bound the actual read as well as stat: a concurrent writer may grow it.
        const buffer = Buffer.alloc(maxFileBytes + 1);
        let used = 0;
        while (used < buffer.length) {
          this.signal.throwIfAborted();
          const { bytesRead } = await file.read(buffer, used, buffer.length - used, null);
          if (!bytesRead) break;
          used += bytesRead;
        }
        if (used > maxFileBytes) throw new Error(`metadata ${name} exceeds ${maxFileBytes} bytes`);
        total += used;
        if (total > maxTotalBytes) throw new Error("metadata exceeds 128 MiB");
        files.set(name, Buffer.from(buffer.subarray(0, used)));
      } finally { await file.close(); }
    }
    if (selected.length) await this.readBlobs(selected, files);
    return files;
  }

  private async readBlobs(selected: [string, string][], files: Map<string, Buffer | null>): Promise<void> {
    const input = selected.map(([, oid]) => oid).join("\n") + "\n";
    // Check immutable object sizes before allowing their bytes into the process.
    const checked = (await this.run(["cat-file", "--batch-check"], input)).toString().trimEnd().split("\n");
    if (checked.length !== selected.length) throw new Error("invalid Git blob batch");
    let total = 0;
    const sizes = checked.map((line, i) => {
      const [oid, type, sizeText] = line.split(" ");
      const size = Number(sizeText);
      if (oid !== selected[i][1] || type !== "blob" || !Number.isSafeInteger(size) || size < 0) throw new Error("invalid Git blob");
      if (size > maxFileBytes) throw new Error(`metadata ${selected[i][0]} exceeds ${maxFileBytes} bytes`);
      total += size;
      if (total > maxTotalBytes) throw new Error("metadata exceeds 128 MiB");
      return size;
    });
    const data = await this.run(["cat-file", "--batch"], input, false, total + selected.length * 128);
    let offset = 0;
    selected.forEach(([name, oid], i) => {
      const end = data.indexOf(10, offset);
      if (end < 0 || data.subarray(offset, end).toString() !== `${oid} blob ${sizes[i]}`) throw new Error("invalid Git blob header");
      offset = end + 1;
      if (data[offset + sizes[i]] !== 10) throw new Error("truncated Git blob");
      files.set(name, Buffer.from(data.subarray(offset, offset + sizes[i])));
      offset += sizes[i] + 1;
    });
    if (offset !== data.length) throw new Error("unexpected Git blob bytes");
  }
}
