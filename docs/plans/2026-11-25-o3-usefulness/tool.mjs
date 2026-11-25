// Disposable recording adapter for the O3 usefulness trial. Not a production
// API or a sandbox. Same budget for both memory arms: 16 calls, 128 KiB total
// visible output, 16,000 bytes per call. Every call is recorded in trace.jsonl.
import { appendFileSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const trial = dirname(fileURLToPath(import.meta.url));
const config = JSON.parse(readFileSync(join(trial, 'config.json'), 'utf8'));
const statePath = join(trial, 'state.json');
const state = JSON.parse(readFileSync(statePath, 'utf8'));
const [op, ...args] = process.argv.slice(2);
const started = performance.now();
let stdout = '', stderr = '', exitCode = 0;
const env = { PATH: '', HOME: config.home, LANG: 'en_US.UTF-8', TERM: 'dumb' };
const run = (argv) => {
  const result = Bun.spawnSync(argv, { cwd: trial, env, timeout: 30000 });
  stdout = result.stdout.toString(); stderr = result.stderr.toString(); exitCode = result.exitCode;
};
const usage = config.arm === 'local'
  ? 'Operations: guide; kb map|get-node|get|recall|list-topics ARGS.'
  : 'Operations: guide; remote orient|search|fetch|status ARGS.';
try {
  if (state.calls >= 16 || state.bytes >= 131072) throw new Error('Trial retrieval budget exhausted. Answer from evidence already obtained.');
  if (op === 'guide') {
    stdout = readFileSync(join(trial, 'guide.md'), 'utf8');
  } else if (op === 'kb' && config.arm === 'local') {
    if (!['map', 'get-node', 'get', 'recall', 'list-topics'].includes(args[0])) throw new Error('Only kb map, get-node, get, recall and list-topics are available.');
    if (args.some((v) => /^(--vault-path|--vaultPath|-p(?:=|$))/.test(v))) throw new Error('The vault is selected automatically.');
    run([config.bun, config.cli, ...args, '-p', join(trial, 'vault')]);
  } else if (op === 'remote' && config.arm === 'remote') {
    if (!['orient', 'search', 'fetch', 'status'].includes(args[0])) throw new Error('Only remote orient, search, fetch and status are available.');
    if (args.some((v) => /^--(profile|endpoint)(=|$)/.test(v))) throw new Error('The profile and endpoint are selected automatically.');
    run([config.bun, config.cli, 'remote', args[0], '--profile', config.profile, '--endpoint', config.endpoint, ...args.slice(1)]);
  } else {
    throw new Error(usage);
  }
} catch (error) {
  exitCode = 2; stderr = error instanceof Error ? error.message : String(error);
}
const raw = stdout + (stderr ? `\nSTDERR: ${stderr}` : '');
const allowance = Math.max(0, Math.min(16000, 131072 - state.bytes - 220));
const rawBuffer = Buffer.from(raw);
const truncated = rawBuffer.length > allowance;
const decoder = new TextDecoder('utf-8', { fatal: false });
let body = decoder.decode(rawBuffer.subarray(0, allowance), { stream: true });
if (truncated) body += '\n[OUTPUT TRUNCATED: narrow the query or read a smaller range.]\n';
const visible = `Call ${state.calls + 1}/16 | operation=${op} ${args[0] ?? ''} | exit=${exitCode}\n${body}`;
const entry = { sequence: state.calls + 1, op, args, exit_code: exitCode, stdout, stderr,
  raw_stdout_bytes: Buffer.byteLength(stdout), raw_stderr_bytes: Buffer.byteLength(stderr),
  visible_bytes: Buffer.byteLength(visible), truncated, duration_ms: +(performance.now() - started).toFixed(3) };
appendFileSync(join(trial, 'trace.jsonl'), JSON.stringify(entry) + '\n');
writeFileSync(statePath, JSON.stringify({ calls: state.calls + 1, bytes: state.bytes + entry.visible_bytes }));
process.stdout.write(visible);
process.exitCode = exitCode;
