import { test } from 'node:test';
import assert from 'node:assert/strict';
import { mkdtempSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { execFileSync } from 'node:child_process';
import { PullRequestContext, PullRequestObserver } from '../dist/index.js';

const repository={forge:{name:'github'},path:'owner/project'};
function fixture(t) {
  const root=mkdtempSync(join(tmpdir(),'repokit-context-'));
  t.after(()=>rmSync(root,{recursive:true,force:true}));
  const git=(...args)=>execFileSync('git',['-c','core.hooksPath=/dev/null','-C',root,...args],{encoding:'utf8'}).trim();
  git('init','-b','topic'); git('config','user.email','test@example.com'); git('config','user.name','Test'); git('commit','--allow-empty','-m','initial');
  const rows=new Map([[1,{repository,number:1,state:'merged',sourceBranch:'topic',sourceRepository:repository,headSha:git('rev-parse','HEAD')}]]);
  const reader={forge:'github',provider:'github',listPullRequests:async()=>({items:[...rows.values()].map(({repository,number})=>({repository,number}))}),getPullRequest:async key=>rows.get(key.number)};
  const context=new PullRequestContext(reader,{checkout:root,source:repository,targets:[repository],home:join(root,'state'),connection:'account'});
  t.after(()=>context.close());
  return {root,git,rows,reader,context};
}
test('a new PR supersedes terminal association; ambiguity and detached topology stay explicit',async t=>{
  const f=fixture(t), first=await f.context.getCheckout();
  assert.equal(first.selected.number,1);
  f.rows.set(2,{...f.rows.get(1),number:2,state:'open'});
  const current=await f.context.verify(first);
  assert.equal(current.selected.number,2);
  f.rows.set(3,{...f.rows.get(2),number:3});
  assert.equal((await f.context.verify(first)).ambiguous,true);
  f.git('switch','--detach');
  assert.equal(await f.context.getCheckout(),undefined);
  assert.equal((await f.context.getBranch('topic')).checkouts.length,0);
  await assert.rejects(()=>f.context.verify(first),/changed/);
});
test('a failing PR cannot select another terminal candidate for deletion',async t=>{
  const f=fixture(t);
  f.rows.set(2,{...f.rows.get(1),number:2,state:'open'});
  const get=f.reader.getPullRequest;
  f.reader.getPullRequest=async key=>{if(key.number===2)throw Error('offline');return get(key);};
  const view=await f.context.getCheckout();
  assert.equal(view.candidates.length,1);assert.ok(view.error);assert.equal(view.selected,undefined);
});
test('one-shot observer never reads persisted merged again',async t=>{
  const f=fixture(t);
  const options={home:join(f.root,'state'),connection:'account',background:false};
  const observer=new PullRequestObserver(f.reader,options);
  await observer.getPullRequest({repository,number:1});
  f.reader.getPullRequest=async()=>{throw Error('must not read terminal PR');};
  assert.deepEqual((await observer.maintain()).issues,[]);
  assert.equal((await observer.getPullRequest({repository,number:1})).state,'merged');
  await observer.close();
});

test('explicit maintenance discovers configured scopes without a background loop',async t=>{
  const f=fixture(t);
  const observer=new PullRequestObserver(f.reader,{home:join(f.root,'state'),connection:'account',background:false,scopes:[{kind:'repository',repository}]});
  await observer.maintain();
  f.reader.getPullRequest=async()=>{throw Error('discovery did not persist');};
  assert.equal((await observer.getPullRequest({repository,number:1})).state,'merged');
  await observer.close();
});
