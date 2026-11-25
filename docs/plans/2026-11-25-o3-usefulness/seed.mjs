// Seeds the synthetic corpus into the LOCAL compose fixture only, through the
// public HTTP API (collector upload, agent candidate) and host kb-admin verify.
// Usage: bun seed.mjs FIXTURE_DIR RECEIPT_OUT
// Credentials stay in FIXTURE_DIR (mode 0600); the receipt holds no secrets.
import { createHash } from 'node:crypto';
import { chmodSync, readFileSync, writeFileSync } from 'node:fs';
import { join, resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { projects, records } from './corpus.mjs';

const [fixture, receiptPath, suffix = ''] = process.argv.slice(2);
if (!fixture || !receiptPath) throw new Error('usage: seed.mjs FIXTURE_DIR RECEIPT_OUT');
const repo = resolve(dirname(fileURLToPath(import.meta.url)), '../../..');
const runtime = JSON.parse(readFileSync(join(fixture, 'runtime.json'), 'utf8'));
if (!/^http:\/\/127\.0\.0\.1:\d+$/.test(runtime.api_url)) throw new Error('fixture API must be loopback');
const api = runtime.api_url;
const compose = ['docker', 'compose', '--env-file', join(fixture, 'compose.env'), '-p', runtime.compose_project, '-f', join(repo, 'deploy/compose.test.yaml')];
const sha = (s) => createHash('sha256').update(s).digest('hex');

function sh(args, input) {
  const r = Bun.spawnSync([...compose, ...args], { stdin: input === undefined ? 'ignore' : Buffer.from(input) });
  if (r.exitCode !== 0) throw new Error(`compose ${args.slice(0, 6).join(' ')} failed: ${r.stderr.toString().slice(0, 400)}`);
  return r.stdout.toString();
}
const admin = (args, input) => sh(['exec', '-T', 'graph', '/usr/local/bin/kb-runtime-entrypoint', '/usr/local/bin/kb-admin', ...args], input === undefined ? undefined : JSON.stringify(input));

function enroll(clientID, project, role, caps) {
  sh(['exec', '-T', 'graph', '/usr/local/bin/kb-runtime-entrypoint', '/bin/sh', '-c', 'umask 077; exec 3>/data/kb/o3-credential; exec /usr/local/bin/kb-admin enroll --client-id "$1" --project "$2" --role "$3" --capabilities "$4" --credential-fd 3', 'sh', clientID, project, role, caps]);
  const target = join(fixture, `${clientID}.credential`);
  sh(['cp', 'graph:/data/kb/o3-credential', target]);
  chmodSync(target, 0o600);
  sh(['exec', '-T', 'graph', '/bin/sh', '-c', 'rm -f /data/kb/o3-credential']);
  return readFileSync(target, 'utf8').trim();
}
async function http(token, method, path, body) {
  const res = await fetch(api + path, { method, headers: { Authorization: `Bearer ${token}`, 'Content-Type': 'application/json' }, body: body === undefined ? undefined : JSON.stringify(body) });
  const text = await res.text();
  if (res.status >= 300) throw new Error(`${method} ${path} -> ${res.status} ${text.slice(0, 300)}`);
  return JSON.parse(text);
}

const receipt = { api_url_redacted: 'http://127.0.0.1:<port>', policy_revision: 'o3-policy-1', projects: {}, records: [] };
for (const project of Object.values(projects)) {
  try { admin(['policy-update'], { ProjectID: project, ExpectedRevision: '', Policy: { Revision: 'o3-policy-1', RawHistoryRetention: 7776000000000000, InactiveTaskRetention: 2592000000000000, BackupRetention: 2592000000000000, SpoolLimitBytes: 10485760 } }); } catch (error) { if (!admin(['policy'], { ProjectID: project }).includes('o3-policy-1')) throw error; }
  const collector = enroll(`o3-collector-${project}${suffix}`, project, 'collector', 'artifacts:write');
  const seeder = enroll(`o3-seeder-${project}${suffix}`, project, 'agent', 'status:read,evidence:read,candidate:write');
  enroll(`o3-solver-${project}${suffix}`, project, 'agent', 'status:read,retrieval:read,evidence:read');
  receipt.projects[project] = { collector: `o3-collector-${project}${suffix}`, seeder: `o3-seeder-${project}${suffix}`, solver: `o3-solver-${project}${suffix}`, solver_capabilities: 'status:read,retrieval:read,evidence:read' };
  for (const record of records.filter((r) => r.project === project)) {
    let head = '';
    const revisions = [];
    for (const [n, text] of record.revisions.entries()) {
      const digest = sha(text);
      const task = `seed-${record.id}`, session = `seed-session-${record.id}-${n + 1}`;
      const scope = { project_id: project, task_id: task, session_id: session };
      const locator = `${record.id}/r${n + 1}`;
      const art = await http(collector, 'POST', '/v1/artifacts', { bytes: Buffer.byteLength(text), content_base64: Buffer.from(text).toString('base64'), digest, policy_revision: 'o3-policy-1', scope, source_locator: locator, source_path: `${record.id}.txt` });
      const artifactID = art.artifact.id;
      await http(collector, 'POST', `/v1/artifacts/${artifactID}:complete`, { digest });
      await http(seeder, 'GET', '/v1/status');
      const cand = await http(seeder, 'POST', '/v1/candidates', { claim_id: `candidate-${record.id}-r${n + 1}`, scope, text, kind: 'finding', depends_on: [], applicability: {}, evidence: [{ artifact_id: artifactID, digest, locator, source_revision: 'source-v1', applicability: {} }] });
      const candidateRevision = cand.claim.revision;
      const entry = { revision: n + 1, artifact_id: artifactID, digest, candidate_revision: candidateRevision, status: record.status };
      if (record.status === 'verified') {
        const draft = { ProjectID: project, CandidateRevision: candidateRevision, ExpectedCandidateHead: candidateRevision, Action: 'verify', IdempotencyKey: `o3-publish-${record.id}-r${n + 1}`, TargetID: record.id, ExpectedTargetHead: head, TargetText: text, TargetKind: 'finding', Excerpts: [{ ArtifactID: artifactID, SourceRevision: 'source-v1', SourceDigest: digest, Start: 0, End: Buffer.byteLength(text), ExpectedExcerptDigest: digest }] };
        const preview = JSON.parse(admin(['verify', '--preview'], draft));
        const published = JSON.parse(admin(['verify'], { ...draft, ExpectedDigest: preview.Digest }));
        if (!published.TargetRevision) throw new Error(`publish failed for ${record.id}`);
        head = published.TargetRevision;
        entry.target_revision = head;
      }
      revisions.push(entry);
      console.log(`seeded ${project} ${record.id} r${n + 1} ${record.status}`);
    }
    receipt.records.push({ id: record.id, project, status: record.status, revisions });
  }
}
writeFileSync(receiptPath, JSON.stringify(receipt, null, 2) + '\n');
console.log(JSON.stringify({ ok: true, records: receipt.records.length }));
