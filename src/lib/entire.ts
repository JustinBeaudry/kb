/**
 * Entire CLI detection and checkpoint resolution.
 *
 * Gateway utilities — KB never parses Entire's internal formats directly.
 * All data flows through `entire explain` or `entire sessions info`.
 */

/**
 * Check if `entire` CLI is on PATH.
 */
export async function isEntireOnPath(): Promise<boolean> {
  return !!Bun.which("entire");
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

    const match = output.match(/Entire-Checkpoint:\s*([a-f0-9]+)/i);
    return match?.[1] ?? null;
  } catch {
    return null;
  }
}
