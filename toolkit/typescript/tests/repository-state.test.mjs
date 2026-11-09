import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, realpathSync, rmSync, existsSync, writeFileSync, unlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import test from "node:test";
import { RepositoryState } from "../dist/index.js";
import { repositoryKey,namePart,pathDigest } from "../dist/state-paths.js";
import { checkoutEnvironment } from "../dist/git/environment.js";

const git=(root,...args)=>execFileSync("git",["-C",root,"-c","core.hooksPath=/dev/null","-c","commit.gpgsign=false",...args],{encoding:"utf8",stdio:"pipe"}).trim();
function fixture(t){const root=realpathSync(mkdtempSync(join(tmpdir(),"repo-state-")));t.after(()=>rmSync(root,{recursive:true,force:true}));const repo=join(root,"repo");mkdirSync(repo);git(repo,"init","-qb","main");git(repo,"config","user.name","Fixture");git(repo,"config","user.email","fixture@example.invalid");git(repo,"commit","--allow-empty","-qm","initial");git(repo,"remote","add","origin","https://github.com/owner/project.git");return {root,repo};}
test("shared identity and Git environment contract",()=>{
 const fixture=JSON.parse(readFileSync(new URL("../../../conformance/repository-state.json",import.meta.url)));
 for(const row of fixture.repositories)assert.equal(repositoryKey(row.forge,row.path),row.key);
 for(const row of fixture.branches)assert.equal(namePart(row.name),row.key);
 const env=JSON.parse(readFileSync(new URL("../../../conformance/git/environment.json",import.meta.url)));
 const source=Object.fromEntries([...env.removed,...env.preserved].map(k=>[k,"value"]));source.GIT_CONFIG_KEY_0="core.worktree";source.GIT_CONFIG_VALUE_0="wrong";
 assert.deepEqual(checkoutEnvironment(source),Object.fromEntries(env.preserved.map(k=>[k,"value"])));
});
test("branch cache and disposable checkout state preserve history",async t=>{
 const {root,repo}=fixture(t),linked=join(root,"linked"),state=new RepositoryState(join(root,"state"));
 git(repo,"worktree","add","-qb","feature/a",linked,"HEAD");
 const directory=await state.observe(repo),key=pathDigest(linked);
 assert.equal(realpathSync(join(directory,"branches","feature%2Fa","worktrees",key,"path")),linked);
 assert.equal((await state.branch(repo,"feature/a",{maxAgeMs:3600000})).source,"cache");
 unlinkSync(join(directory,"checkouts",key,"observation.json"));
 await assert.rejects(state.branch(repo,"feature/a",{maxAgeMs:3600000}),/Incomplete checkout observation/);
 await state.observe(repo);
 mkdirSync(join(directory,"history"));writeFileSync(join(directory,"history","keep"),"keep");
 git(repo,"worktree","remove",linked);await state.observe(repo,{reconcile:true});
 assert.equal(existsSync(join(directory,"checkouts",key)),false);
 assert.deepEqual((await state.branch(repo,"feature/a")).checkouts,[]);
 assert.equal(readFileSync(join(directory,"history","keep"),"utf8"),"keep");
 git(repo,"branch","-D","feature/a");await state.observe(repo,{reconcile:true});
 assert.equal(existsSync(join(directory,"branches","feature%2Fa")),false);
});
test("same repository clones retain independent heads; inherited Git context is ignored",async t=>{
 const {root,repo}=fixture(t),other=join(root,"other"),state=new RepositoryState(join(root,"state"));
 git(repo,"branch","topic");git(repo,"tag","topic");git(repo,"clone",repo,other);git(other,"remote","set-url","origin","git@github.com:owner/project.git");
 git(other,"-c","user.name=Fixture","-c","user.email=fixture@example.invalid","commit","--allow-empty","-qm","next");git(other,"branch","topic");
 const directory=await state.observe(repo);await state.observe(other);
 const previous={...process.env};t.after(()=>{for(const k of Object.keys(process.env))if(!(k in previous))delete process.env[k];Object.assign(process.env,previous);});
 process.env.GIT_DIR=join(repo,".git");process.env.GIT_WORK_TREE=repo;process.env.GIT_INDEX_FILE=join(repo,".git","index");
 const first=await state.branch(repo,"topic",{maxAgeMs:3600000}),second=await state.branch(other,"topic",{maxAgeMs:3600000});
 assert.notEqual(first.head,second.head);assert.equal((await state.branch(other,"topic")).head,second.head);
 const path=join(directory,"branches","topic","observation.json"),before=readFileSync(path,"utf8");
 await assert.rejects(state.observe(join(root,"missing"),{reconcile:true}));assert.equal(readFileSync(path,"utf8"),before);
});
