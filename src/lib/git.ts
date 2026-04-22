export type FileAction = "created" | "modified" | "deleted" | "renamed" | "unknown";

export interface FileChange {
  path: string;
  action: FileAction;
}

async function runGit(
  args: string[],
  cwd: string
): Promise<{ stdout: string; stderr: string; exitCode: number }> {
  const proc = Bun.spawn(["git", ...args], {
    cwd,
    stdout: "pipe",
    stderr: "pipe",
  });
  const [stdout, stderr] = await Promise.all([
    new Response(proc.stdout).text(),
    new Response(proc.stderr).text(),
  ]);
  const exitCode = await proc.exited;
  return { stdout, stderr, exitCode };
}

export async function headCommit(cwd: string): Promise<string | null> {
  const { stdout, exitCode } = await runGit(["rev-parse", "HEAD"], cwd);
  if (exitCode !== 0) return null;
  const sha = stdout.trim();
  return /^[0-9a-f]{40}$/.test(sha) ? sha : null;
}

export async function currentBranch(cwd: string): Promise<string | null> {
  const { stdout, exitCode } = await runGit(["branch", "--show-current"], cwd);
  if (exitCode !== 0) return null;
  const branch = stdout.trim();
  return branch.length > 0 ? branch : null;
}

function actionFromStatusCode(code: string): FileAction {
  switch (code[0]) {
    case "A":
      return "created";
    case "M":
      return "modified";
    case "D":
      return "deleted";
    case "R":
      return "renamed";
    default:
      return "unknown";
  }
}

export async function filesChangedSince(
  fromSha: string,
  cwd: string
): Promise<FileChange[]> {
  const { stdout, exitCode } = await runGit(
    ["diff", "--name-status", "--no-renames", `${fromSha}..HEAD`],
    cwd
  );
  if (exitCode !== 0) return [];
  return parseNameStatus(stdout);
}

function parseNameStatus(stdout: string): FileChange[] {
  const changes: FileChange[] = [];
  for (const line of stdout.split("\n")) {
    if (line.length === 0) continue;
    const [code, ...rest] = line.split("\t");
    if (!code || rest.length === 0) continue;
    const path = rest[rest.length - 1]!;
    changes.push({ path, action: actionFromStatusCode(code) });
  }
  return changes;
}

export async function uncommittedChanges(cwd: string): Promise<FileChange[]> {
  const { stdout, exitCode } = await runGit(
    ["status", "--porcelain=v1", "--untracked-files=all"],
    cwd
  );
  if (exitCode !== 0) return [];

  const changes: FileChange[] = [];
  for (const line of stdout.split("\n")) {
    if (line.length < 4) continue;
    const xy = line.slice(0, 2);
    const path = line.slice(3);
    let action: FileAction;
    if (xy === "??") action = "created";
    else if (xy.includes("A")) action = "created";
    else if (xy.includes("D")) action = "deleted";
    else if (xy.includes("R")) action = "renamed";
    else if (xy.includes("M")) action = "modified";
    else action = "unknown";
    changes.push({ path, action });
  }
  return changes;
}
