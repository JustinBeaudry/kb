/**
 * Entire CLI detection and checkpoint resolution.
 *
 * Gateway utilities — Cairn never parses Entire's internal formats directly.
 * All data flows through `entire explain` or `entire sessions info`.
 */

/**
 * Check if `entire` CLI is on PATH.
 */
export async function isEntireOnPath(): Promise<boolean> {
  try {
    const proc = Bun.spawn(["which", "entire"], {
      stdout: "ignore",
      stderr: "ignore",
    });
    return (await proc.exited) === 0;
  } catch {
    return false;
  }
}

/**
 * Check if Entire is enabled in the given project directory.
 * Runs `entire status` and checks for the "Enabled" indicator in stdout.
 * `entire status` always exits 0 regardless of state, so we parse the output.
 */
export async function isEntireEnabled(cwd: string): Promise<boolean> {
  if (!(await isEntireOnPath())) return false;

  try {
    const proc = Bun.spawn(["entire", "status"], {
      cwd,
      stdout: "pipe",
      stderr: "ignore",
    });
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    if (exitCode !== 0) return false;

    return output.includes("Enabled");
  } catch {
    return false;
  }
}

/**
 * Get checkpoint ID from the most recent commit message in the given cwd.
 * Reads the `Entire-Checkpoint:` trailer from HEAD.
 * Returns null if no checkpoint trailer found.
 */
export async function getHeadCheckpointId(cwd: string): Promise<string | null> {
  try {
    const proc = Bun.spawn(
      ["git", "log", "-1", "--format=%B", "HEAD"],
      { cwd, stdout: "pipe", stderr: "ignore" }
    );
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    if (exitCode !== 0) return null;

    const match = output.match(/Entire-Checkpoint:\s*([a-f0-9]+)/);
    return match ? match[1] : null;
  } catch {
    return null;
  }
}

/**
 * Run `entire explain --checkpoint <id> --short` and return the output.
 * Returns null on failure.
 */
export async function explainCheckpoint(
  checkpointId: string,
  cwd: string
): Promise<string | null> {
  try {
    const proc = Bun.spawn(
      ["entire", "explain", "--checkpoint", checkpointId, "--short", "--no-pager"],
      { cwd, stdout: "pipe", stderr: "ignore" }
    );
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    return exitCode === 0 ? output.trim() : null;
  } catch {
    return null;
  }
}

/**
 * Run `entire explain --checkpoint <id>` (default detail) and return the output.
 * Returns null on failure.
 */
export async function explainCheckpointFull(
  checkpointId: string,
  cwd: string
): Promise<string | null> {
  try {
    const proc = Bun.spawn(
      ["entire", "explain", "--checkpoint", checkpointId, "--no-pager"],
      { cwd, stdout: "pipe", stderr: "ignore" }
    );
    const output = await new Response(proc.stdout).text();
    const exitCode = await proc.exited;
    return exitCode === 0 ? output.trim() : null;
  } catch {
    return null;
  }
}
