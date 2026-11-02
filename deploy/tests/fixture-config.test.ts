import { test, expect } from "bun:test";
import { mkdtemp, readFile, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

test("compiled fixture config supplies its unique logical worker identity across restore", async () => {
 const directory = await mkdtemp(join(tmpdir(),"kb-fixture-config-"));
 try {
  const child=Bun.spawn(["go","run","../deploy/fixture-config.go",directory],{cwd:resolve("server"),stdout:"pipe",stderr:"pipe"});
  const [output,error,exit]=await Promise.all([new Response(child.stdout).text(),new Response(child.stderr).text(),child.exited]);
  expect(exit).toBe(0);expect(output).toBe("");expect(error).toBe("");
  const metadata: {compose_project:string}=JSON.parse(await readFile(join(directory,"runtime.json"),"utf8"));
  for(const name of ["worker.env","worker-restored.env"]){
   const raw=await readFile(join(directory,name),"utf8");
   const entry=raw.split("\n").find(line=>line.startsWith("KB_WORKER_ID="));
   expect(entry).toBe(`KB_WORKER_ID='${metadata.compose_project}-worker'`);
  }
  for(const name of ["api.env","api-restored.env","runtime.env","runtime-restored.env"]){
   const raw=await readFile(join(directory,name),"utf8");
   expect(raw.split("\n").some(line=>line.startsWith("KB_WORKER_ID="))).toBe(false);
  }
 } finally {await rm(directory,{recursive:true,force:true});}
});
