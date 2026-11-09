import test from "node:test";
import assert from "node:assert/strict";
import { mkdtempSync,realpathSync,readFileSync,existsSync,rmSync } from "node:fs";
import { join } from "node:path";
import { tmpdir } from "node:os";
import { resolveHome,RepositoryState,ForgeConnection } from "../dist/index.js";

test("shared home precedence resolves without writes",t=>{
 const root=realpathSync(mkdtempSync(join(tmpdir(),"repokit-home-"))),user=join(root,"user"),cwd=process.cwd(),oldHome=process.env.HOME,oldRoot=process.env.REPOKIT_HOME;
 t.after(()=>{process.chdir(cwd);if(oldHome===undefined)delete process.env.HOME;else process.env.HOME=oldHome;if(oldRoot===undefined)delete process.env.REPOKIT_HOME;else process.env.REPOKIT_HOME=oldRoot;rmSync(root,{recursive:true,force:true});});
 process.env.HOME=user;process.chdir(root);
 const cases=JSON.parse(readFileSync(new URL("../../../conformance/home.json",import.meta.url)));
 const expand=value=>value===null?undefined:value.replaceAll("USER",user).replaceAll("CWD",root);
 for(const row of cases){if(row.environment===null)delete process.env.REPOKIT_HOME;else process.env.REPOKIT_HOME=expand(row.environment);const expected=expand(row.expected);assert.equal(resolveHome(expand(row.explicit)),expected);assert.equal(existsSync(expected),false);}
 process.env.REPOKIT_HOME=join(root,"shared");assert.equal(new RepositoryState().store.root,new ForgeConnection({host:"github.com",token:"fixture"}).home);
});
