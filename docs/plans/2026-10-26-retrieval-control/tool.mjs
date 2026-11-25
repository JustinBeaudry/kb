// Disposable experiment adapter. This is not a production KB API or a sandbox.
import { appendFileSync, readFileSync, writeFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const trial = dirname(fileURLToPath(import.meta.url));
const config = JSON.parse(readFileSync(join(trial, 'config.json'), 'utf8'));
const statePath = join(trial, 'state.json');
const state = JSON.parse(readFileSync(statePath, 'utf8'));
const [op, ...args] = process.argv.slice(2);
const vault = join(trial, 'vault');
const started = performance.now();
let stdout = '', stderr = '', exitCode = 0;
const run = (argv, cwd = vault, env = { PATH: '', LANG: 'en_US.UTF-8', TERM: 'dumb' }) => {
  const result = Bun.spawnSync(argv, { cwd, env, timeout: 10000 });
  stdout = result.stdout.toString(); stderr = result.stderr.toString(); exitCode = result.exitCode;
};
try {
  if (state.calls >= 16 || state.bytes >= 131072) throw new Error('Trial retrieval budget exhausted. Answer from evidence already obtained.');
  if (op === 'guide') {
    stdout = readFileSync(join(trial, 'guide.md'), 'utf8');
  } else if (op === 'files') {
    run([config.rg, '--files', 'wiki']);
    if (args[0]) stdout = stdout.split('\n').filter(line => line.toLowerCase().includes(args[0].toLowerCase())).join('\n') + '\n';
  } else if (op === 'search') {
    const context = Number(args[1] ?? 2);
    if (!args[0] || !Number.isInteger(context) || context < 0 || context > 8) throw new Error('Usage: search REGEX [context_lines 0..8].');
    run([config.rg, '--line-number', '--no-heading', '--color', 'never', '--ignore-case', '--context', String(context), '--glob', '*.md', '--', args[0], 'wiki', 'index.md', 'context.md', 'KB.md']);
  } else if (op === 'read') {
    const file = args[0] ?? '';
    if (!/^(wiki\/[a-z0-9-]+\.md|index\.md|context\.md|KB\.md)$/.test(file)) throw new Error('Read only a known wiki Markdown path, index.md, context.md or KB.md.');
    const start = Number(args[1] ?? 1), end = Number(args[2] ?? start + 79);
    if (!Number.isInteger(start) || !Number.isInteger(end) || start < 1 || end < start || end - start > 1023) throw new Error('Use a positive inclusive start/end range of at most 1024 lines.');
    const lines = readFileSync(join(vault, file), 'utf8').split('\n');
    stdout = lines.slice(start - 1, end).map((line, index) => `${file}:${start + index}:${line}`).join('\n') + '\n';
  } else if (op === 'kb') {
    if (!config.kb_enabled) throw new Error('This arm has file tools only.');
    if (!['map', 'get-node', 'get', 'recall', 'list-topics'].includes(args[0])) throw new Error('Only curated retrieval commands are available.');
    if (args.some(value => /^(--vault|-p(?:=|$))/.test(value))) throw new Error('The fixture vault is selected automatically.');
    run([config.bun, config.cli, ...args, '-p', vault]);
  } else {
    throw new Error('Operations: guide; files [name-fragment]; search REGEX [context]; read PATH [start] [end]; kb COMMAND ARGS (KB-enabled only).');
  }
} catch (error) {
  exitCode = 2; stderr = error instanceof Error ? error.message : String(error);
}
const raw = stdout + (stderr ? `\nSTDERR: ${stderr}` : '');
const allowance = Math.max(0, Math.min(16000, 131072 - state.bytes - 220));
const rawBuffer = Buffer.from(raw);
const truncated = rawBuffer.length > allowance;
// Keep byte accounting exact; UTF-8 decoder drops a cut final codepoint.
const decoder = new TextDecoder('utf-8', { fatal: false });
let body = decoder.decode(rawBuffer.subarray(0, allowance), { stream: true });
if (truncated) body += '\n[OUTPUT TRUNCATED: narrow the query or read a smaller range.]\n';
const visible = `Call ${state.calls + 1}/16 | operation=${op} | exit=${exitCode}\n${body}`;
const entry = { sequence: state.calls + 1, op, args, exit_code: exitCode, stdout, stderr, visible,
  raw_stdout_bytes: Buffer.byteLength(stdout), raw_stderr_bytes: Buffer.byteLength(stderr),
  visible_bytes: Buffer.byteLength(visible), truncated, duration_ms: +(performance.now() - started).toFixed(3) };
appendFileSync(join(trial, 'trace.jsonl'), JSON.stringify(entry) + '\n');
writeFileSync(statePath, JSON.stringify({ calls: state.calls + 1, bytes: state.bytes + entry.visible_bytes }));
process.stdout.write(visible);
process.exitCode = exitCode;
