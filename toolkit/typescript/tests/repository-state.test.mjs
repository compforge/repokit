import assert from "node:assert/strict";
import { mkdtempSync, mkdirSync, readFileSync, realpathSync, rmSync, existsSync, writeFileSync, unlinkSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import test from "node:test";
import { ForgeConnection } from "../dist/forge/connection.js";
import { digest } from "../dist/state/store.js";
import { RepositoryState } from "../dist/index.js";
import { repositoryKey,namePart,pathDigest } from "../dist/state/identity.js";
import { checkoutEnvironment } from "../dist/git/environment.js";

const git=(root,...args)=>execFileSync("git",["-C",root,"-c","core.hooksPath=/dev/null","-c","commit.gpgsign=false",...args],{encoding:"utf8",stdio:"pipe"}).trim();
function fixture(t){const root=realpathSync(mkdtempSync(join(tmpdir(),"repo-state-")));t.after(()=>rmSync(root,{recursive:true,force:true}));const repo=join(root,"repo");mkdirSync(repo);git(repo,"init","-qb","main");git(repo,"config","user.name","Fixture");git(repo,"config","user.email","fixture@example.invalid");git(repo,"commit","--allow-empty","-qm","initial");git(repo,"remote","add","origin","https://github.com/owner/project.git");return {root,repo};}
test("shared identity and Git environment contract",()=>{
 const fixture=JSON.parse(readFileSync(new URL("../../../conformance/repository-state.json",import.meta.url)));
 for(const row of fixture.connections){const connection=new ForgeConnection(row);assert.equal(connection.identity,row.identity);assert.equal(digest([connection.name,connection.identity]),row.connection);}
 for(const row of fixture.repositories)assert.equal(repositoryKey(row.forge,row.path),row.key);
 for(const row of fixture.branches)assert.equal(namePart(row.name),row.key);
 const env=JSON.parse(readFileSync(new URL("../../../conformance/git/environment.json",import.meta.url)));
 for(const row of env.configurations)assert.deepEqual(checkoutEnvironment(row.input),row.output);
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
 const repaired=await state.branch(repo,"feature/a",{maxAgeMs:3600000});
 assert.equal(repaired.source,"git");assert.equal(repaired.checkouts.length,1);
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

test("partial publication repairs only the requested branch",async t=>{
 const {root,repo}=fixture(t),state=new RepositoryState(join(root,"state"));git(repo,"branch","other");
 const directory=await state.observe(repo),path=join(directory,"branches","main","observation.json"),write=state.store.write.bind(state.store);
 state.store.write=async (target,...args)=>{if(target===path)throw Error("interrupted publication");return write(target,...args);};
 await assert.rejects(state.observe(repo),/interrupted publication/);state.store.write=write;
 const unrelated=join(directory,"branches","other","observation.json");writeFileSync(unrelated,"{");
 for(const maxAgeMs of [3600000,0]) {const row=await state.branch(repo,"main",{maxAgeMs});assert.equal(row.source,"git");assert.equal(row.checkouts.length,1);assert.equal(readFileSync(unrelated,"utf8"),"{");}
});

test("reclamation retains unknown state and surviving source directories",async t=>{
 const {root,repo}=fixture(t),state=new RepositoryState(join(root,"state")),linked=join(root,"linked");
 git(repo,"worktree","add","-qb","topic",linked,"HEAD");const directory=await state.observe(repo),record=join(directory,"checkouts",pathDigest(linked));
 const metadata=git(linked,"rev-parse","--absolute-git-dir");rmSync(metadata,{recursive:true});
 await state.observe(repo,{reconcile:true});assert(existsSync(linked));assert(existsSync(record));
 rmSync(linked,{recursive:true});writeFileSync(join(record,"keep"),"unknown");
 await assert.rejects(state.observe(repo,{reconcile:true}),/Unrecognized checkout state/);assert.equal(readFileSync(join(record,"keep"),"utf8"),"unknown");
 unlinkSync(join(record,"keep"));unlinkSync(join(record,"path"));writeFileSync(join(record,"path"),"not a symlink");
 await assert.rejects(state.observe(repo,{reconcile:true}),/not a symlink/);assert(existsSync(join(record,"observation.json")));
});
test("unknown primary location cannot authorize checkout reclamation",async t=>{
 const {root,repo}=fixture(t),state=new RepositoryState(join(root,"state")),linked=join(root,"linked");
 git(repo,"init","--separate-git-dir",join(root,"metadata"));git(repo,"worktree","add","-qb","topic",linked,"HEAD");
 const directory=await state.observe(repo);await state.observe(linked,{reconcile:true});
 const row=await state.branch(linked,"main",{maxAgeMs:3600000});assert.equal(row.complete,false);assert.deepEqual(row.checkouts,[]);
 assert(existsSync(join(directory,"checkouts",pathDigest(repo),"observation.json")));
});
