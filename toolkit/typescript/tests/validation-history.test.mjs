import assert from "node:assert/strict";
import { test } from "node:test";
import { execFileSync } from "node:child_process";
import { mkdtemp, mkdir, readFile, writeFile, readdir, open, rm, realpath } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { createRequire, syncBuiltinESMExports } from "node:module";
import { listValidationRuns, readValidationRun } from "../dist/index.js";

const corpus = JSON.parse(await readFile(new URL("../../../conformance/history/runs.json", import.meta.url), "utf8"));
async function fixture(t, events = corpus.events) {
  const temp = await realpath(await mkdtemp(join(tmpdir(), "repokit-history-")));
  t.after(() => rm(temp, {recursive:true,force:true}));
  const root = join(temp,"checkout"), home = join(temp,"home");
  await mkdir(root);
  execFileSync("git",["init","-q",root]);
  execFileSync("git",["-C",root,"remote","add","origin","https://github.com/example/history.git"]);
  const directory = join(home,"repositories","github--example--history");
  if (events !== null) {
    await mkdir(join(directory,"history"),{recursive:true});
    await writeFile(join(directory,"history","00000000000000000001"),encode(events));
  }
  return {root,home,directory,query:{repository:root,home}};
}
const encode = events => events.map((e,i)=>JSON.stringify({index:String(i+1),data:"+"+JSON.stringify(e)})+"\n").join("");
for (const row of corpus.cases) test(row.name, async t => {
  const {query} = await fixture(t,[...corpus.events,...row.extra??[]]);
  const operation = () => listValidationRuns({...query,...row.query});
  if (row.error) { await assert.rejects(operation, {code: row.errorCode, op: "validation.history.list", subject: query.repository}); return; }
  const report = await operation();
  assert.deepEqual(report.runs.map(run=>run.runID),row.ids);
  assert.equal(report.total,row.total); assert.equal(report.hasMore,row.hasMore);
  const passed = report.runs.find(run=>run.runID==="AAA");
  if (passed) assert.equal(passed.reuseCount,1);
  const unfinished = report.runs.find(run=>run.runID==="CCC");
  if (unfinished) { assert.equal(unfinished.status,"unfinished"); assert.equal(unfinished.inputsConsistent,null); }
});
// @case Both query APIs honor REPOKIT_HOME; reading an absent home does not create it.
test("missing store stays absent and home environment is shared",async t=>{
  const {root,home,directory} = await fixture(t,null), previous=process.env.REPOKIT_HOME;
  process.env.REPOKIT_HOME=home;
  t.after(()=>{if(previous===undefined)delete process.env.REPOKIT_HOME;else process.env.REPOKIT_HOME=previous;});
  assert.equal((await listValidationRuns({repository:root})).total,0);
  await assert.rejects(readdir(home),{code:"ENOENT"});
  await mkdir(join(directory,"history"),{recursive:true});
  await writeFile(join(directory,"history","00000000000000000001"),encode(corpus.events));
  const runs = await listValidationRuns({repository:root});
  assert.deepEqual(runs.runs.map(run=>run.runID),["CCC","BBB","AAA"]);
  assert.equal(runs.total,3);
  const detail = await readValidationRun(root,"AAA");
  assert.equal(detail.run.runID,"AAA");
  assert.equal(detail.result.status,"passed");
});
test("detail keeps missing evidence distinct from a missing run",async t=>{
  const {root,home,directory} = await fixture(t);
  const missing = await readValidationRun(root,"AAA",{home});
  assert.equal(missing.complete,false); assert.equal(missing.inputs,null);
  assert.deepEqual(missing.diagnostics.map(row=>row.artifact),["inputs","output"]);
  await assert.rejects(readValidationRun(root,"ZZZ",{home}),{code:"validation.run_not_found"});
  await mkdir(join(directory,"inputs")); await mkdir(join(directory,"logs"));
  await writeFile(join(directory,"inputs","INPUT.json"),JSON.stringify({schemaVersion:1,checkout:"/retained/checkout",snapshot:"digest",files:{}}));
  for (const stream of ["stdout","stderr"]) await writeFile(join(directory,"logs",`2026-11-12.${stream}.jsonl`),JSON.stringify({runID:"AAA",output:stream})+"\n");
  const found=await readValidationRun(root,"AAA",{home});
  assert.equal(found.complete,true); assert.equal(found.inputs.snapshot,"digest");
  assert.equal(found.result.stdout,"stdout"); assert.equal(found.result.stderr,"stderr");
});
test("WAL corruption is an error, never empty history",async t=>{
  const {query,directory}=await fixture(t);
  await writeFile(join(directory,"history","00000000000000000001"),'{"data":"+incomplete');
  await assert.rejects(listValidationRuns(query),/Incomplete/);
});
test("logical truncation recovery does not mutate segments",async t=>{
  const {query,directory}=await fixture(t,corpus.events.slice(0,3));
  const path=join(directory,"history");
  assert.equal((await listValidationRuns(query)).total,2);
  await writeFile(join(path,"00000000000000000001.START"),encode(corpus.events));
  const before=await readdir(path);
  const contents=await Promise.all(before.map(name=>readFile(join(path,name),"utf8")));
  const recovered=await listValidationRuns(query);
  assert.deepEqual(recovered.runs.map(run=>run.runID),["CCC","BBB","AAA"]);
  assert.equal(recovered.total,3);
  assert.deepEqual(await readdir(path),before);
  assert.deepEqual(await Promise.all(before.map(name=>readFile(join(path,name),"utf8"))),contents);
});
test("advisory lock wait obeys cancellation",{timeout:4_000},async t=>{
  const {query,directory}=await fixture(t);
  const fsExt=createRequire(import.meta.url)("fs-ext");
  const {flockSync,flock}=fsExt;
  let attempts=0;
  fsExt.flock=(fd,flags,callback)=>{if(flags==="exnb")attempts++;return flock(fd,flags,callback);};
  t.after(()=>{fsExt.flock=flock;});
  const lock=await open(join(directory,"lock"),"a+");
  flockSync(lock.fd,"exnb");
  try {
    const started=performance.now();
    await assert.rejects(listValidationRuns({...query,timeoutMs:1_000}),{code:"execution.deadline_exceeded", op:"validation.history.list", subject:query.repository});
    assert.ok(attempts>0,"query must contend on the advisory lock before cancellation");
    assert.ok(performance.now()-started<2_500,"caller timeout must take effect before the 5 second lock cap");
  }
  finally {flockSync(lock.fd,"un");await lock.close();}
  assert.equal((await listValidationRuns(query)).total,3);
});

const contracts = JSON.parse(await readFile(new URL("../../../conformance/history/contracts.json", import.meta.url), "utf8"));
// @case Shared history failures classify list and detail identically across Go and TypeScript.
for (const row of contracts.errors) test(row.name, async t => {
  const {root, home, directory, query} = await fixture(t);
  if (row.mode === "corrupt") await writeFile(join(directory,"history","00000000000000000001"),"broken\n");
  const signal = row.mode === "cancelled" ? AbortSignal.abort() : row.mode === "deadline" ? AbortSignal.abort(new DOMException("deadline", "TimeoutError")) : undefined;
  const operation = row.operation === "list" ? () => listValidationRuns({...query,signal}) : () => readValidationRun(root,row.runID,{home,signal});
  await assert.rejects(operation, {code:row.code, op:`validation.history.${row.operation}`, subject:row.operation === "list" ? root : row.runID});
});
for (const row of contracts.inputs) test(row.name, async t => {
  const {root,home,directory,query} = await fixture(t);
  if (row.inputs) {
    await mkdir(join(directory,"inputs"));
    await writeFile(join(directory,"inputs","INPUT.json"),JSON.stringify(row.inputs));
  }
  const report = await listValidationRuns({...query,includeInputs:true,limit:1,offset:2});
  assert.deepEqual(report.runs.map(run=>run.runID),["AAA"]);
  const summary = report.runs[0].inputs;
  assert.equal(summary.complete,row.complete);
  assert.equal(summary.snapshot,row.complete ? "digest" : undefined);
  assert.deepEqual(summary.diagnostics.map(issue=>issue.artifact),row.complete ? [] : ["inputs"]);
  const detail = await readValidationRun(root,"AAA",{home});
  assert.equal(detail.complete,false,"inputs do not prove output availability");
  assert.equal(detail.inputs !== null,row.complete);
  assert.deepEqual(detail.diagnostics.map(issue=>issue.artifact),row.complete ? ["output"] : ["inputs","output"]);
  assert.equal((await listValidationRuns(query)).runs[0].inputs,undefined);
});
// @case A page replays WAL once, reads only its input manifests and never opens daily output logs.
test("input summaries have bounded evidence IO", async t => {
  const {directory,query} = await fixture(t);
  await mkdir(join(directory,"inputs"));
  await writeFile(join(directory,"inputs","INPUT.json"),JSON.stringify(contracts.inputs[0].inputs));
  const fs = createRequire(import.meta.url)("node:fs/promises"), original = fs.readFile, paths=[];
  fs.readFile = async (path,...args) => { paths.push(String(path)); return original(path,...args); };
  syncBuiltinESMExports();
  try {
    const report=await listValidationRuns({...query,includeInputs:true,limit:1,offset:2});
    assert.equal(report.runs[0].inputs.complete,true);
    assert.deepEqual(paths.filter(path=>path.startsWith(directory)),[
      join(directory,"history","00000000000000000001"), join(directory,"inputs","INPUT.json")
    ]);
  } finally { fs.readFile=original; syncBuiltinESMExports(); }
});
