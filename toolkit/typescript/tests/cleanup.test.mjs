import test from "node:test";
import assert from "node:assert/strict";
import { mkdtemp, mkdir, writeFile, readFile, rm, readdir } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { cleanComments, cleanPullRequests } from "../dist/state/cleanup.js";
import { loadCleanupPolicy, defaultCleanupPolicy } from "../dist/state/policy.js";
async function put(path, value) { await mkdir(join(path, ".."), {recursive:true}); await writeFile(path, JSON.stringify(value)); }
const corpus=JSON.parse(await readFile(new URL("../../../conformance/cleanup-policy.json",import.meta.url),"utf8"));
for(const row of corpus.cases)test("cleanup policy: "+row.name,async t=>{
 const home=await mkdtemp(join(tmpdir(),"repokit-clean-"));t.after(()=>rm(home,{recursive:true,force:true}));
 await put(join(home,"config.json"),row.home);await put(join(home,"repositories/fixture/config.json"),row.repository);
 if(row.invalid)await assert.rejects(loadCleanupPolicy(home,"fixture"));else assert.deepEqual(await loadCleanupPolicy(home,"fixture"),row.expected);
});
test("whole collections, protected old PR, released leases",async t=>{
 const home=await mkdtemp(join(tmpdir(),"repokit-clean-"));t.after(()=>rm(home,{recursive:true,force:true}));const root=join(home,"repositories/fixture/prs");
 for(const [n,size] of [[1,3],[2,2]]){
  await put(join(root,String(n),"access.json"),{version:1,lastAccessAt:n});
  await put(join(root,String(n),"a.json"),{version:1,scopeIds:n===1?["watch"]:[]});
  await put(join(root,String(n),"comments/access.json"),{version:1,lastAccessAt:n});
  for(let i=0;i<size;i++)for(const connection of ["a","b"])await put(join(root,String(n),"comments",String(i),connection+".json"),{version:1});
 }
 await mkdir(join(root,"1/comments/empty"));
 const options={home,repositoryKey:"fixture",policy:{...defaultCleanupPolicy(),pr:1,comment:3}};
 const protectedPR=await cleanPullRequests(options);assert.equal(protectedPR.counts[0].after,2);assert.equal(protectedPR.counts[0].over_budget,1);
 const preview=await cleanComments({...options,dryRun:true}),actual=await cleanComments(options);
 assert.deepEqual(preview.counts,actual.counts);assert.equal(actual.counts[0].before,5);assert.equal(actual.counts[0].after,2);
 assert.equal(preview.actions[0].estimatedBytes,actual.actions[0].estimatedBytes);
 assert.deepEqual((await cleanComments(options)).actions,[]);
 for(const kind of ["activity","resources"])assert.deepEqual(await readdir(join(home,"locks",kind)),[]);
});
