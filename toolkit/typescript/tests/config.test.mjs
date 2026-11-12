import assert from "node:assert/strict";
import { test } from "node:test";
import { mkdtemp, readFile, writeFile, chmod, stat, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { inspect } from "node:util";
import { loadConfig, ForgeConnection } from "../dist/index.js";

const cases = JSON.parse(await readFile(new URL("../../../conformance/config/cases.json",import.meta.url),"utf8"));
async function home(t) {
  const path=await mkdtemp(join(tmpdir(),"repokit-config-"));
  t.after(()=>rm(path,{recursive:true,force:true}));
  return path;
}
for (const row of cases) test(row.name,async t=>{
  for (const name of ["GH_TOKEN","GITHUB_TOKEN","GITLAB_TOKEN","FIXTURE_FORGE_TOKEN"]) {
    const value=process.env[name];
    t.after(()=>{if(value===undefined)delete process.env[name];else process.env[name]=value;});
    delete process.env[name];
  }
  Object.assign(process.env,row.env ?? {});
  const root=await home(t);
  if ("config" in row) await writeFile(join(root,"config.json"),JSON.stringify(row.config),{mode:0o600});
  if (row.loadError) {
    await assert.rejects(loadConfig(root), /config/i);
    return;
  }
  const operation=async()=>{
    const config=await loadConfig(root);
    assert.ok(!JSON.stringify(config).includes("secret-canary"));
    assert.ok(!inspect(config).includes("secret-canary"));
    return config.forge(row.host);
  };
  if (row.error) {
    await assert.rejects(operation,error=>error.message.toLowerCase().includes(row.error)&&!error.message.includes("secret-canary"));
  } else {
    const actual=await operation(), expected=row.expected;
    const connection=new ForgeConnection({...expected,apiHost:expected.apiHost||undefined,home:root});
    assert.equal(actual.identity,connection.identity);
    assert.equal(actual.home,root);
  }
});
test("config home, permissions and snapshot",async t=>{
  const root=await home(t), missing=join(root,"not-created");
  const previous=process.env.REPOKIT_HOME;
  process.env.REPOKIT_HOME=missing;
  t.after(()=>{if(previous===undefined)delete process.env.REPOKIT_HOME;else process.env.REPOKIT_HOME=previous;});
  assert.equal((await loadConfig()).home,missing);
  await assert.rejects(stat(missing),{code:"ENOENT"});
  const path=join(root,"config.json");
  await writeFile(path,JSON.stringify({forges:{"git.example.com":{provider:"gitlab",token:"secret-canary"}}}),{mode:0o600});
  const config=await loadConfig(root);
  assert.ok(!inspect(config).includes("secret-canary"));
  await writeFile(path,'{"token":"secret-canary",');
  assert.equal(config.forge("git.example.com").host,"git.example.com");
  await assert.rejects(loadConfig(root),error=>!error.message.includes("secret-canary"));
  await chmod(path,0o644);
  await assert.rejects(loadConfig(root),/chmod 600/);
});
