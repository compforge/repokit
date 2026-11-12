import { ValidationHistoryError, historyError } from "./errors.js";
import { readFile, realpath } from "node:fs/promises";
import { join, resolve } from "node:path";
import { Git } from "../git/catalog.js";
import { parseRemoteUrl } from "../git/remote.js";
import { resolveHome } from "../state/home.js";
import { localKey, repositoryKey } from "../state/identity.js";
import { inspectHistory, missing, object, type Row } from "./store.js";

export interface ValidationHistoryOptions { readonly home?: string; readonly historyDir?: string; readonly timeoutMs?: number; readonly signal?: AbortSignal }
export interface ValidationRunQuery extends ValidationHistoryOptions {
  readonly repository: string; readonly checkout?: string; readonly component?: string;
  readonly check?: string; readonly status?: string; readonly limit?: number; readonly offset?: number;
  readonly includeInputs?: boolean;
}
export interface ValidationRun {
  readonly runID: string; readonly checkout: string; readonly component: string; readonly check: string;
  readonly scope: string; readonly status: string; readonly startedAt: string; readonly finishedAt?: string;
  readonly durationSeconds: number; readonly inputsConsistent: boolean | null; readonly inputID: string;
  readonly reuseCount: number; readonly lastReusedAt?: string;
  readonly inputs?: ValidationRunInputSummary;
}
export interface ValidationRunInputSummary { readonly snapshot?: string; readonly complete: boolean; readonly diagnostics: readonly {artifact: string; message: string}[] }
export interface ValidationRunList { readonly repository: string; readonly runs: readonly ValidationRun[]; readonly total: number; readonly offset: number; readonly limit: number; readonly hasMore: boolean }
export interface ValidationRunInputs { readonly schemaVersion: 1; readonly checkout: string; readonly snapshot: string; readonly files: Readonly<Record<string, {kind: string; digest: string; version?: boolean}>> }
export interface ValidationRunDetail {
  readonly repository: string; readonly run: ValidationRun; readonly result: Readonly<Row>;
  readonly inputs: ValidationRunInputs | null; readonly complete: boolean;
  readonly diagnostics: readonly {artifact: string; message: string}[];
}
interface Run { start: Row; check: string; result?: Row; finishedAt?: string; consistent: boolean; reuseCount: number; lastReusedAt?: string }
function fail(message: string): never { throw new Error(message); }
function text(value: unknown): string {
  if (value == null) return "";
  if (typeof value !== "string") fail("Expected string");
  return value;
}
function time(value: unknown): string {
  const result = text(value);
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d{1,9})?(?:Z|[+-]\d\d:\d\d)$/.test(result) || !Number.isFinite(Date.parse(result))) fail("Invalid history timestamp");
  return result;
}
// Preserve Go's nanosecond ordering instead of rounding distinct runs to milliseconds.
function instant(value: string): bigint {
  const fraction = /\.(\d+)(?:Z|[+-])/.exec(value)?.[1] ?? "";
  return BigInt(Date.parse(value.replace(/\.\d+/, ""))) * 1_000_000n + BigInt(fraction.padEnd(9, "0"));
}
function compareTime(a: string, b: string): number {
  const left = instant(a), right = instant(b);
  if (left < right) return -1;
  return left > right ? 1 : 0;
}
function number(value: unknown): number {
  if (value == null) return 0;
  if (typeof value !== "number" || !Number.isFinite(value)) fail("Invalid history number");
  return value;
}
function replay(events: readonly Row[]): Map<string, Run> {
  const runs = new Map<string, Run>();
  for (const event of events) {
    const check = object(event.check), name = text(check.check), id = text(event.run), at = time(event.at);
    text(event.component); text(event.inputs); text(check.status);
    number(check.durationSeconds); number(check.lookupSeconds);
    if (check.checkedAt != null) time(check.checkedAt);
    if (event.schemaVersion !== 2 || !text(event.checkout) || !id || at.startsWith("0001-01-01T00:00:00") || !["lint", "test"].includes(name)) fail("Invalid validation history event");
    const run = runs.get(id);
    if (event.type === "started") {
      if (run) fail(`Duplicate run ${id}`);
      runs.set(id, { start: event, check: name, consistent: false, reuseCount: 0 });
    } else if (event.type === "finished") {
      if (!run) continue;
      if (run.result || run.start.checkout !== event.checkout || run.start.component !== event.component || run.check !== name || run.start.inputs !== event.inputs) fail(`Invalid completion for run ${id}`);
      if (event.inputsConsistent != null && typeof event.inputsConsistent !== "boolean") fail("Invalid input consistency");
      run.result = check; run.finishedAt = at; run.consistent = event.inputsConsistent === true;
    } else if (event.type === "reused") {
      if (!run) continue;
      if (!run.result || !run.consistent || run.result.status !== "passed" || run.start.checkout !== event.checkout || run.start.component !== event.component || run.check !== name) fail(`Invalid reuse of run ${id}`);
      run.reuseCount++;
      if (!run.lastReusedAt || compareTime(at, run.lastReusedAt) > 0) run.lastReusedAt = at;
    } else fail("Unknown validation history event");
  }
  return runs;
}
function result(run: Run): Row {
  const check = object(run.result ?? run.start.check);
  if (check.runID !== run.start.run || check.check !== run.check) fail("History check identity does not match its Run");
  return { ...check, status: run.result ? text(check.status) : "unfinished" };
}
function summary(run: Run): ValidationRun {
  const check = result(run);
  return { runID: text(run.start.run), checkout: text(run.start.checkout), component: text(run.start.component), check: run.check,
    scope: text(check.scope), status: text(check.status), startedAt: text(run.start.at), durationSeconds: number(check.durationSeconds),
    inputsConsistent: run.result ? run.consistent : null, inputID: text(run.start.inputs), reuseCount: run.reuseCount,
    ...(run.finishedAt ? {finishedAt: run.finishedAt} : {}), ...(run.lastReusedAt ? {lastReusedAt: run.lastReusedAt} : {}) };
}
function budget(options: ValidationHistoryOptions): AbortSignal {
  if (options.timeoutMs === undefined) return options.signal ?? new AbortController().signal;
  const timeout = AbortSignal.timeout(options.timeoutMs);
  return options.signal ? AbortSignal.any([timeout, options.signal]) : timeout;
}
async function location(repository: string, options: ValidationHistoryOptions, signal: AbortSignal) {
  const git = new Git(repository, signal);
  await git.run(["rev-parse", "--show-toplevel"]);
  const origin = (await git.run(["config", "--get", "remote.origin.url"], undefined, true)).toString().trim();
  const remote = parseRemoteUrl(origin);
  let key: string;
  if (remote) {
    let forge = remote.host;
    if (forge === "github.com") forge = "github";
    if (forge === "gitlab.com") forge = "gitlab";
    key = repositoryKey(forge, remote.path);
  } else {
    const common = (await git.run(["rev-parse", "--path-format=absolute", "--git-common-dir"])).toString().trim();
    key = localKey(await realpath(common));
  }
  return { key, directory: join(options.historyDir || join(resolveHome(options.home), "repositories"), key) };
}

/**
 * @spec Go-equivalent filtering and pagination of retained executions; reuse never reorders runs.
 * Historical success is never evidence that the current checkout passed.
 * includeInputs reads only the selected page's manifests under the replay lock, never logs.
 * @see {@link component://docs/validation.md}
 */
export async function listValidationRuns(query: ValidationRunQuery): Promise<ValidationRunList> {
  let signal: AbortSignal | undefined;
  const op = "validation.history.list";
  const invalid = (message: string): never => { throw new ValidationHistoryError("validation.invalid_query", message, op, query.repository); };
  try {
    const limit = query.limit === undefined || query.limit === 0 ? 20 : query.limit, offset = query.offset ?? 0;
    if (!Number.isInteger(limit) || limit < 1 || limit > 100 || !Number.isInteger(offset) || offset < 0) invalid("Limit must be 1–100 and offset must be nonnegative");
    if (query.check && !["lint", "test"].includes(query.check)) invalid("Check must be lint or test");
    if (query.status && !["passed", "failed", "cancelled", "uncertain", "unfinished"].includes(query.status)) invalid("Invalid run status");
    signal = budget(query);
    const budgetSignal = signal, {key, directory} = await location(query.repository, query, budgetSignal);
    let checkout = query.checkout ? resolve(query.checkout) : "";
    if (checkout) { try { checkout = await realpath(checkout); } catch (error) { if (!missing(error)) throw error; } }
    return await inspectHistory(directory, budgetSignal, async events => {
      const runs: ValidationRun[] = [];
      for (const run of replay(events).values()) {
        budgetSignal.throwIfAborted();
        if (checkout && run.start.checkout !== checkout || query.component && run.start.component !== query.component || query.check && run.check !== query.check) continue;
        const row = summary(run);
        if (!query.status || row.status === query.status) runs.push(row);
      }
      runs.sort((a, b) => compareTime(b.startedAt, a.startedAt) || Buffer.compare(Buffer.from(a.runID), Buffer.from(b.runID)));
      const page = runs.slice(offset, offset + limit);
      const selected = query.includeInputs ? await Promise.all(page.map(async run => {
        const evidence = await inputEvidence(directory, run, budgetSignal);
        const inputs: ValidationRunInputSummary = {complete: evidence.inputs !== null, diagnostics: evidence.diagnostics,
          ...(evidence.inputs ? {snapshot: evidence.inputs.snapshot} : {})};
        return {...run, inputs};
      })) : page;
      budgetSignal.throwIfAborted();
      return {repository: key, runs: selected, total: runs.length, offset, limit, hasMore: offset + limit < runs.length};
    });
  } catch (error) { throw historyError(error, op, query.repository, signal); }
}
async function output(path: string, id: string, signal: AbortSignal): Promise<string | undefined> {
  let data: string;
  try { data = await readFile(path, {encoding: "utf8", signal}); } catch (error) { if (missing(error)) return undefined; throw error; }
  const lines = data.split("\n");
  for (let i = 0; i < lines.length - 1; i++) {
    signal.throwIfAborted();
    const record = object(JSON.parse(lines[i]));
    if (record.runID === id) return text(record.output);
  }
  if (lines.at(-1)) fail("Incomplete output record");
  return undefined;
}
/** @spec Missing evidence yields incomplete detail; corrupt history never yields successful partial data. */
export async function readValidationRun(repository: string, runID: string, options: ValidationHistoryOptions = {}): Promise<ValidationRunDetail> {
  const op = "validation.history.show";
  let signal: AbortSignal | undefined;
  try {
    if (!/^[A-Z2-7]+$/.test(runID)) throw new ValidationHistoryError("validation.invalid_run_id", "Invalid run ID", op, runID);
    signal = budget(options);
    const budgetSignal = signal, {key, directory} = await location(repository, options, budgetSignal);
    return await inspectHistory(directory, budgetSignal, async events => {
      const stored = replay(events).get(runID);
      if (!stored) throw new ValidationHistoryError("validation.run_not_found", "Run is not present in retained repository history", op, runID);
      const run = summary(stored), check = result(stored), diagnostics: {artifact: string; message: string}[] = [];
      const evidence = await inputEvidence(directory, run, budgetSignal);
      const inputs = evidence.inputs;
      diagnostics.push(...evidence.diagnostics);
      let stdout = "", stderr = "";
      try {
        const prefix = join(directory, "logs", new Date(run.startedAt).toISOString().slice(0, 10));
        const out = await output(prefix + ".stdout.jsonl", runID, budgetSignal), err = await output(prefix + ".stderr.jsonl", runID, budgetSignal);
        if (out !== undefined && err !== undefined) { stdout = out; stderr = err; }
        else if (out !== undefined || err !== undefined) fail("Incomplete daily output for run " + runID);
        else { stdout = await readFile(join(directory, "logs", runID, "stdout"), {encoding: "utf8", signal: budgetSignal}); stderr = await readFile(join(directory, "logs", runID, "stderr"), {encoding: "utf8", signal: budgetSignal}); }
      } catch (error) { budgetSignal.throwIfAborted(); diagnostics.push({artifact: "output", message: String(error)}); }
      budgetSignal.throwIfAborted();
      return {repository: key, run, result: {...check, stdout, stderr}, inputs, complete: diagnostics.length === 0, diagnostics};
    });
  } catch (error) { throw historyError(error, op, runID, signal); }
}

async function inputEvidence(directory: string, run: ValidationRun, signal: AbortSignal) {
  let inputs: ValidationRunInputs | null = null;
  const diagnostics: {artifact: string; message: string}[] = [];
  try {
    if (!/^[A-Z2-7]+$/.test(run.inputID)) fail("Invalid input record ID");
    const raw = object(JSON.parse(await readFile(join(directory, "inputs", run.inputID + ".json"), {encoding: "utf8", signal})));
    if (raw.schemaVersion !== 1 || raw.checkout !== run.checkout || !text(raw.snapshot)) fail("Input manifest identity or schema is invalid");
    object(raw.files);
    for (const value of Object.values(object(raw.files))) {
      const entry = object(value);
      text(entry.kind); text(entry.digest);
      if (entry.version != null && typeof entry.version !== "boolean") fail("Invalid input version marker");
    }
    inputs = raw as unknown as ValidationRunInputs;
  } catch (error) { signal.throwIfAborted(); diagnostics.push({artifact: "inputs", message: String(error)}); }
  signal.throwIfAborted();
  return {inputs, diagnostics};
}
