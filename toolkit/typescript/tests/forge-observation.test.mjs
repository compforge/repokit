import { digest } from "../dist/storage.js";
import assert from 'node:assert/strict';
import { test } from 'node:test';
import { mkdtemp, readFile, readdir, mkdir, rm, appendFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { spawn } from 'node:child_process';
import { AsyncLocalStorage } from 'node:async_hooks';
import { PullRequestObserver as PublicObserver, PullRequestStatusUnavailable, ObservationStorageError, ForgeError, createPullRequestScanner } from '../dist/index.js';

import { ObservationEngine as PullRequestObserver } from '../dist/forge/observation/engine.js';

const repository = {forge:{name:'fixture'},path:'org/repo'};
const key = (number=1) => ({repository,number});
async function fixture(t) {
  const stateDir=await mkdtemp(join(tmpdir(),'repokit-observer-'));
  t.after(()=>rm(stateDir,{recursive:true,force:true}));
  let now=Date.parse('2026-11-02T00:00:00Z'), state='open', failure=false, calls=0, listed=[];
  const reader={forge:'fixture', async listPullRequests(){return {items:listed};}, async getPullRequest(k){
    calls++;if(failure)throw new ForgeError('offline');return {...k,state,title:'PR'};
  }};
  const options={stateDir,connection:'account-a',now:()=>new Date(now),openIntervalMs:10,closedIntervalMs:100,retryIntervalMs:10};
  return {stateDir,reader,options, observer:new PullRequestObserver(reader,options), advance:n=>now+=n,setState:s=>state=s,setFailure:b=>failure=b,setListed:l=>listed=l,calls:()=>calls};
}

test('restart reuses persisted facts; merged stops polling but force reads remain explicit',async t=>{
  const f=await fixture(t);
  assert.equal((await f.observer.get(key())).source,'remote');
  const restarted=new PullRequestObserver(f.reader,f.options);
  assert.equal((await restarted.get(key())).source,'cache');assert.equal(f.calls(),1);
  f.setState('merged');f.advance(11);
  assert.equal((await restarted.get(key())).snapshot.state,'merged');
  f.advance(100000);await restarted.poll();assert.equal(f.calls(),2);
  assert.equal((await restarted.get(key())).fresh,true);
  f.setState('open');const contradicted=await restarted.get(key(),{refresh:true});
  assert.equal(contradicted.snapshot.state,'merged');assert.match(contradicted.lastError.message,/contradicted/);
  // A different access identity cannot receive another connection's merged cache.
  const other=new PullRequestObserver(f.reader,{...f.options,connection:'account-b'});
  assert.equal((await other.get(key())).snapshot.state,'open');
});

test('closed is refreshed and can reopen; failures retain facts and honor backoff',async t=>{
  const f=await fixture(t);f.setState('closed');await f.observer.get(key());
  f.setState('open');f.advance(20);await f.observer.poll();assert.equal(f.calls(),1);
  f.advance(81);assert.equal((await f.observer.poll()).observations[0].snapshot.state,'open');
  f.advance(11);f.setFailure(true);const failed=await f.observer.get(key());
  assert.equal(failed.fresh,false);assert.equal(failed.snapshot.state,'open');
  assert.equal(failed.lastError.kind,'transport');
  const count=f.calls();assert.equal((await f.observer.poll()).complete,false);assert.equal(f.calls(),count);
  f.setFailure(false);f.advance(11);assert.equal((await f.observer.poll()).complete,true);
});

test('a never-observed failure remains unknown and run is explicitly cancellable',async t=>{
  const f=await fixture(t);f.setFailure(true);
  const result=await f.observer.get(key());assert.equal(result.snapshot,undefined);assert.equal(result.fresh,false);
  const controller=new AbortController();const stream=f.observer.run({keys:[key()],signal:controller.signal,pollIntervalMs:10});
  assert.equal((await stream.next()).done,false);controller.abort();assert.equal((await stream.next()).done,true);
});

test('concurrent observer instances share one remote refresh',async t=>{
  const f=await fixture(t);
  const second=new PullRequestObserver(f.reader,f.options);
  const results=await Promise.all([f.observer.get(key()),second.get(key())]);
  assert.equal(f.calls(),1);assert.deepEqual(new Set(results.map(r=>r.source)),new Set(['remote','cache']));
  const repos=await readdir(join(f.stateDir,'repositories'));
  const dir=join(f.stateDir,'repositories',repos[0],'prs','1');
  assert((await readdir(join(dir,'observations'))).includes(digest(['fixture','account-a'])+'.json'));
});

test('separate processes serialize refresh and reread the winning observation',async t=>{
  const f=await fixture(t), log=join(f.stateDir,'calls.txt');
  const module= new URL('../dist/index.js',import.meta.url).href;
  const script=`import {PullRequestObserver} from ${JSON.stringify(module)};
    import {appendFile} from 'node:fs/promises';
    const reader={forge:'fixture',listPullRequests:async()=>({items:[]}),getPullRequest:async key=>{
      await appendFile(${JSON.stringify(log)},'call\\n');await new Promise(r=>setTimeout(r,100));return {...key,state:'open'};}};
    const observer=new PullRequestObserver(reader,{stateDir:${JSON.stringify(f.stateDir)},connection:'account-a'});
    try { await observer.getPullRequest(${JSON.stringify(key())}); } finally { await observer.close(); }`;
  const child=()=>new Promise((res,rej)=>{const p=spawn(process.execPath,['--input-type=module','-e',script]);let errors='';p.stderr.on('data',v=>errors+=v);p.on('error',rej);p.on('exit',code=>code?rej(new Error(errors)):res());});
  await Promise.all([child(),child()]);assert.equal((await readFile(log,'utf8')).trim(),'call');
});

test('discovery persists facts before its checkpoint and merged overlaps use cache',async t=>{
  const f=await fixture(t);f.setListed([key()]);f.setState('merged');
  let result=await f.observer.poll({scopes:[{kind:'repository',repository}]});
  assert(result.observations.some(o=>o.snapshot.state==='merged'));
  const calls=f.calls();
  result=await new PullRequestObserver(f.reader,f.options).poll({scopes:[{kind:'repository',repository}]});
  assert.equal(f.calls(),calls);assert.equal(result.complete,true);
  assert.equal((await readdir(join(f.stateDir,'scans'))).filter(n=>n.endsWith('.json')).length,1);
});

test('observation write failure rejects and never advances the discovery checkpoint',async t=>{
  const f=await fixture(t);f.setListed([key()]);
  f.reader.getPullRequest=async k=>{
    const repos=await readdir(join(f.stateDir,'repositories'));
    await mkdir(join(f.stateDir,'repositories',repos[0],'prs','1','observations',digest(['fixture','account-a'])+'.json'));
    return {...k,state:'merged'};
  };
  await assert.rejects(f.observer.poll({scopes:[{kind:'repository',repository}]}),ObservationStorageError);
  assert.equal((await readdir(join(f.stateDir,'scans'))).filter(n=>n.endsWith('.json')).length,0);
});

test('full discovery backlog cannot block tracked reads; removed tracked errors disappear',async()=>{
  const epoch=Date.parse('2026-11-02T00:00:00Z');let recover=false;
  const rows=Array.from({length:501},(_,i)=>({key:key(i+1),time:epoch+(i+1)*1000}));
  const calls=[];
  const reader={forge:'fixture',async listPullRequests(q){
    const selected=rows.filter(r=>r.time>=Date.parse(q.updatedAfter)&&r.time<=Date.parse(q.updatedBefore));
    return {items:selected.slice(0,50).map(r=>r.key),...(selected.length>50?{nextCursor:'more'}:{})};
  },async getPullRequest(k){calls.push(k.number);if(k.number===600 || (k.number<=500&&!recover))throw new ForgeError('offline');return {...k,state:'merged'};}};
  const scan=createPullRequestScanner(reader,{connection:'a',now:()=>new Date(epoch+600000)});
  let cursor,result;
  for(let i=0;i<300;i++){
    result=await scan({scope:{kind:'repository',repository},cursor,tracked:[key(600),key(601)]});cursor=result.cursor;
    assert(result.items.some(p=>p.number===601));
    if(result.issues.some(i=>i.message.includes('backlog is full')))break;
  }
  assert(result.issues.some(i=>i.message.includes('backlog is full')));
  assert.equal(JSON.parse(cursor).failedReads.length,500);
  calls.length=0;
  result=await scan({scope:{kind:'repository',repository},cursor,tracked:[key(601)]});cursor=result.cursor;
  assert(calls.includes(601));assert(!calls.includes(600));assert(!result.issues.some(i=>i.key?.number===600));
  recover=true;let found=false;
  for(let i=0;i<300;i++){
    result=await scan({scope:{kind:'repository',repository},cursor,tracked:[key(601)]});cursor=result.cursor;
    found ||= result.items.some(p=>p.number===501);
    if(result.complete)break;
  }
  assert(found);assert.equal(result.complete,true);
});

test('public status query hides cache details and maintenance discovers a merge',async t=>{
  const f=await fixture(t);
  const service=new PublicObserver(f.reader,f.options);t.after(()=>service.close());
  const status=await service.getPullRequest(key());
  assert.deepEqual(Object.keys(status).sort(),['key','observedAt','state']);
  assert.equal(status.state,'open');
  f.setState('merged');f.advance(11);
  for(let i=0;i<100 && f.calls()<2;i++)await new Promise(r=>setTimeout(r,10));
  assert.equal((await service.getPullRequest(key())).state,'merged');
  await service.close();assert.equal(service.health.running,false);
  await assert.rejects(service.getPullRequest(key()));
});

test('public query rejects unavailable state and provider timeout is bounded',async t=>{
  const f=await fixture(t);
  f.reader.getPullRequest=()=>new Promise(()=>{});
  const service=new PublicObserver(f.reader,{...f.options,requestTimeoutMs:20});t.after(()=>service.close());
  await assert.rejects(service.getPullRequest(key()),error=>error instanceof PullRequestStatusUnavailable && error.issue.kind==='transport');
});

test('a stale lock from an exited process is recovered',async t=>{
  const {utimes}=await import('node:fs/promises');
  const f=await fixture(t);await f.observer.get(key());
  const repos=await readdir(join(f.stateDir,'repositories'));
  const lock=join(f.stateDir,'repositories',repos[0],'prs','1','observations',digest(['fixture','account-a'])+'.json.lock');
  await mkdir(lock);await utimes(lock,new Date(0),new Date(0));
  assert.equal((await f.observer.get(key())).source,'cache');
  await assert.rejects(readFile(lock),{code:'ENOENT'});
});

test('foreground cancellation does not save a late remote result',async t=>{
  const f=await fixture(t);let release;
  f.reader.getPullRequest=k=>new Promise(resolve=>{release=()=>resolve({...k,state:'merged'});});
  const controller=new AbortController();const request=f.observer.get(key(),{signal:controller.signal});
  const rejection=assert.rejects(request);
  for(let i=0;i<100&&!release;i++)await new Promise(r=>setTimeout(r,2));
  assert(release);controller.abort();await rejection;release();
  await new Promise(r=>setTimeout(r,5));
  const repos=await readdir(join(f.stateDir,'repositories'));
  await assert.rejects(readFile(join(f.stateDir,'repositories',repos[0],'prs','1','observations',digest(['fixture','account-a'])+'.json')),{code:'ENOENT'});
});

// Model a cooperative provider: work remains active until its scoped signal aborts.
function cooperativeRead(reader, method) {
  const context = new AsyncLocalStorage();
  const started = Promise.withResolvers();
  const stopped = Promise.withResolvers();
  reader.withSignal = (signal, operation) => context.run(signal, operation);
  reader[method] = async () => {
    const signal = context.getStore();
    started.resolve(signal);
    assert(signal instanceof AbortSignal, 'provider work must receive a scoped AbortSignal');
    await new Promise((_, reject) => {
      const abort = () => { stopped.resolve(signal); reject(signal.reason); };
      if (signal.aborted) abort();
      else signal.addEventListener('abort', abort, { once: true });
    });
  };
  return { started: started.promise, stopped: stopped.promise };
}

for (const trigger of ['caller abort', 'observer close', 'request timeout']) {
  test(`cooperative detail work stops on ${trigger}`, { timeout: 5000 }, async t => {
    const f = await fixture(t);
    const provider = cooperativeRead(f.reader, 'getPullRequest');
    const service = new PublicObserver(f.reader, {
      ...f.options, openIntervalMs: 60_000, retryIntervalMs: 60_000,
      requestTimeoutMs: trigger === 'request timeout' ? 50 : 2000,
    });
    const controller = new AbortController();
    try {
      const outcome = Promise.allSettled([service.getPullRequest(key(), { signal: controller.signal })]);
      const signal = await provider.started;
      assert(signal instanceof AbortSignal, 'cancellation must reach the provider, not just the waiting caller');
      assert.equal(signal.aborted, false);
      if (trigger === 'caller abort') controller.abort();
      if (trigger === 'observer close') await service.close();
      const [result] = await outcome;
      assert.equal(result.status, 'rejected');
      assert.equal(signal.aborted, true);
      assert.equal(await provider.stopped, signal);
      if (trigger === 'request timeout') {
        assert(result.reason instanceof PullRequestStatusUnavailable);
        assert.equal(result.reason.issue.kind, 'transport');
      }
    } finally { await service.close(); }
  });
}

test('observer close stops cooperative discovery list work without saving a checkpoint', { timeout: 5000 }, async t => {
  const f = await fixture(t);
  const provider = cooperativeRead(f.reader, 'listPullRequests');
  const service = new PublicObserver(f.reader, { ...f.options, scopes: [{ kind: 'repository', repository }] });
  try {
    const signal = await provider.started;
    assert(signal instanceof AbortSignal, 'discovery must propagate cancellation to listPullRequests');
    assert.equal(signal.aborted, false);
    await service.close();
    assert.equal(signal.aborted, true);
    assert.equal(await provider.stopped, signal);
    assert.equal(service.health.running, false);
    assert.equal((await readdir(join(f.stateDir, 'scans'))).filter(name => name.endsWith('.json')).length, 0);
  } finally { await service.close(); }
});

test('connections retain independent discovery membership and refresh inventory',async t=>{
 const f=await fixture(t);f.setListed([key()]);
 const watch={scopes:[{kind:'repository',repository}]};
 await f.observer.poll(watch);
 const other=new PullRequestObserver(f.reader,{...f.options,connection:'account-b'});
 await other.get(key());f.advance(11);f.setListed([]);
 assert.equal((await f.observer.poll({refresh:true})).observations.length,1);
 assert.equal((await other.poll()).observations.length,1);
});
