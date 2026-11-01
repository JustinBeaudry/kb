import { test, expect } from "bun:test";
import { mkdtemp, mkdir, writeFile, readFile, chmod, rm } from "node:fs/promises";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const fake = `#!/usr/bin/env bun
const fs=require('node:fs'),path=require('node:path');
const dir=process.env.KB_TEST_DIR,statePath=process.env.KB_TEST_STATE,scenario=process.env.KB_TEST_SCENARIO;
const state=fs.existsSync(statePath)?JSON.parse(fs.readFileSync(statePath,'utf8')):{objectPolls:0,statusPolls:0,setup:0,journal:0,policy:0,sink:'',policyInputs:[]};
const args=process.argv.slice(2),name=path.basename(process.argv[1]);
function done(code=0){fs.writeFileSync(statePath,JSON.stringify(state));process.exit(code)}
if(name==='go'){
 if(args.includes('buckets')){state.bucketsVerified=true;done()}
 fs.writeFileSync(path.join(dir,'runtime.json'),JSON.stringify({api_url:'http://127.0.0.1:0',object_url:'http://127.0.0.1:0',compose_project:'kbq-0123456789ab',config_file:path.join(dir,'compose.env'),credential_file:path.join(dir,'credential'),owner_public_key:'public-fixture-key'}));
 fs.writeFileSync(path.join(dir,'compose.env'),'');fs.writeFileSync(path.join(dir,'policy.json'),'fixture-policy-payload');done();
}
if(name==='sleep')done();
if(name==='curl'){
 if(args.some(x=>x.includes('/livez')||x.includes('/readyz')))done();
 state.objectPolls++;if(state.objectPolls<3)done(7);process.stdout.write('403');done();
}
if(name!=='docker')done(99);
if(args.includes('port')){process.stdout.write(args.includes('8080')?'127.0.0.1:18080':args.includes('8335')?'127.0.0.1:18335':'127.0.0.1:18333');done()}
if(args.includes('cp')){fs.writeFileSync(args.at(-1),state.sink);done()}
if(args.includes('shell')){
 if(scenario==='delayed'&&state.objectPolls<3){process.stderr.write('object_not_ready');done(1)}
 state.bucketInit=true;done();
}
if(args.includes('/usr/local/bin/kb-supervisor')&&args.includes('status')){
 state.statusPolls++;
 const samples=JSON.parse(fs.readFileSync(process.env.KB_TEST_STATUS_SAMPLES,'utf8'));
 const delayed=scenario==='delayed';
 const name=delayed&&state.statusPolls===1?'schema_pending':delayed&&state.statusPolls===2?'unknown':delayed&&state.statusPolls===3?'active':'ready';
 process.stdout.write(JSON.stringify(samples[name]));done();
}
const command=args.find(x=>x.includes('kb-admin setup'));
let action=command?'setup':args.includes('journal-init')?'journal':args.includes('policy-update')?'policy':null;
if(!action)done();
if(scenario==='delayed'&&state.statusPolls<4){process.stderr.write('host_not_ready');done(1)}
state[action]++;
if(command&&command.includes('exec 3>')&&!command.includes('exec 3>>'))state.sink='';
if(action==='setup'&&['unknown','delivery','mixed-result'].includes(scenario)){
 state.sink+='fixture-protected-bearer\\n';
 if(scenario==='mixed-result'){process.stdout.write('fixture-protected-bearer\\n');process.stderr.write('setup: maintenance: admission refused; retry after the current maintenance operation completes\\n')}
 else process.stderr.write(scenario==='unknown'?'setup: unknown_outcome: credential transfer may have committed\\n':'setup: administration_unavailable: credential transfer failed\\n');
 done(1);
}
if(action==='setup'&&scenario==='contention'&&state.setup===1){process.stderr.write('setup: maintenance: admission refused; retry after the current maintenance operation completes\\n');done(1)}
if(action==='setup')state.sink+='fixture-protected-bearer\\n';
if(action==='policy'){
 state.policyInputs.push(fs.readFileSync(0,'utf8'));
 if(scenario==='contention'&&state.policy===1){process.stderr.write('policy-update: maintenance: admission refused; retry after the current maintenance operation completes\\n');done(1)}
}
done();
`;

async function exercise(scenario: string) {
  const root = await mkdtemp(join(tmpdir(), "kb-compose-boundary-"));
  const directory = join(root, "fixture");
  const bin = join(root, "bin");
  const statePath = join(root, "state.json");
  const samplesPath = join(root, "status-samples.json");
  const helper = Bun.spawn(["go", "run", "./internal/acceptance/testdata/maintenance-json"], { cwd: resolve("server"), stdout: "pipe", stderr: "pipe" });
  const [samples, helperError, helperExit] = await Promise.all([new Response(helper.stdout).text(), new Response(helper.stderr).text(), helper.exited]);
  expect(helperExit).toBe(0);
  expect(helperError).toBe("");
  expect(samples).toContain('"active":0');
  expect(samples).toContain('"unknown":0');
  expect(samples).toContain('"phase":"bootstrap"');
  expect(samples).not.toContain('"Active":');
  await writeFile(samplesPath, samples, { mode: 0o600 });
  await mkdir(directory, { mode: 0o700 });
  await mkdir(bin, { mode: 0o700 });
  for (const name of ["docker", "go", "curl", "sleep"]) {
    const path = join(bin, name);
    await writeFile(path, fake);
    await chmod(path, 0o755);
  }
  const process = Bun.spawn(["/bin/sh", resolve("deploy/compose-smoke.sh"), "up", directory], {
    env: { ...Bun.env, PATH: `${bin}:${Bun.env.PATH}`, KB_TEST_DIR: directory, KB_TEST_STATE: statePath, KB_TEST_SCENARIO: scenario, KB_TEST_STATUS_SAMPLES: samplesPath },
    stdout: "pipe", stderr: "pipe",
  });
  const [stdout, stderr, exit] = await Promise.all([new Response(process.stdout).text(), new Response(process.stderr).text(), process.exited]);
  const state: Record<string, unknown> = JSON.parse(await readFile(statePath, "utf8"));
  return { root, directory, stdout, stderr, exit, state };
}

test("bootstrap waits for delayed object and schema readiness", async () => {
  const result = await exercise("delayed");
  try {
    expect(result.exit).toBe(0);
    expect(result.state.objectPolls).toBeGreaterThanOrEqual(3);
    expect(result.state.statusPolls).toBeGreaterThanOrEqual(4);
    expect(result.state.setup).toBe(1);
    expect(result.stdout).not.toContain("fixture-protected-bearer");
  } finally { await rm(result.root, { recursive: true, force: true }); }
});

test("bootstrap retries only known admission refusal and reopens policy input", async () => {
  const result = await exercise("contention");
  try {
    expect(result.exit).toBe(0);
    expect(result.state.setup).toBe(2);
    expect(result.state.policy).toBe(2);
    expect(result.state.policyInputs).toEqual(["fixture-policy-payload", "fixture-policy-payload"]);
    expect(await readFile(join(result.directory, "credential"), "utf8")).toBe("fixture-protected-bearer\n");
  } finally { await rm(result.root, { recursive: true, force: true }); }
});

for (const scenario of ["unknown", "delivery", "mixed-result"]) test(`bootstrap preserves the sink and never retries ${scenario}`, async () => {
  const result = await exercise(scenario);
  try {
    expect(result.exit).not.toBe(0);
    expect(result.state.setup).toBe(1);
    expect(result.state.sink).toBe("fixture-protected-bearer\n");
    expect(result.state.journal).toBe(0);
    expect(result.stdout + result.stderr).not.toContain("fixture-protected-bearer");
  } finally { await rm(result.root, { recursive: true, force: true }); }
});
