import { describe, it, expect, beforeEach, afterEach } from "bun:test";
import { mkdtempSync, rmSync, writeFileSync, mkdirSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import {
  headCommit,
  currentBranch,
  filesChangedSince,
  uncommittedChanges,
} from "../src/lib/git";

const describeGit = Bun.which("git") === null ? describe.skip : describe;

async function git(cwd: string, ...args: string[]): Promise<string> {
  const proc = Bun.spawn(["git", ...args], { cwd, stdout: "pipe", stderr: "pipe" });
  const out = await new Response(proc.stdout).text();
  await proc.exited;
  return out.trim();
}

async function makeGitRepo(): Promise<string> {
  const dir = mkdtempSync(join(tmpdir(), "cairn-git-"));
  await git(dir, "init", "-q", "--initial-branch=main");
  await git(dir, "config", "user.email", "test@cairn.local");
  await git(dir, "config", "user.name", "Test");
  await git(dir, "config", "commit.gpgsign", "false");
  return dir;
}

describeGit("headCommit", () => {
  let dir: string;
  afterEach(() => { if (dir) rmSync(dir, { recursive: true, force: true }); });

  it("returns the HEAD SHA after a commit", async () => {
    dir = await makeGitRepo();
    writeFileSync(join(dir, "a.txt"), "hello\n");
    await git(dir, "add", "a.txt");
    await git(dir, "commit", "-q", "-m", "initial");
    const expected = await git(dir, "rev-parse", "HEAD");

    const sha = await headCommit(dir);
    expect(sha).toBe(expected);
    expect(sha).toMatch(/^[0-9a-f]{40}$/);
  });

  it("returns null in a non-git directory", async () => {
    dir = mkdtempSync(join(tmpdir(), "cairn-nogit-"));
    const sha = await headCommit(dir);
    expect(sha).toBeNull();
  });

  it("returns null in a git repo with no commits", async () => {
    dir = await makeGitRepo();
    const sha = await headCommit(dir);
    expect(sha).toBeNull();
  });
});

describeGit("currentBranch", () => {
  let dir: string;
  afterEach(() => { if (dir) rmSync(dir, { recursive: true, force: true }); });

  it("returns the current branch name", async () => {
    dir = await makeGitRepo();
    writeFileSync(join(dir, "a.txt"), "x");
    await git(dir, "add", "a.txt");
    await git(dir, "commit", "-q", "-m", "init");

    const branch = await currentBranch(dir);
    expect(branch).toBe("main");
  });

  it("returns null in a non-git directory", async () => {
    dir = mkdtempSync(join(tmpdir(), "cairn-nogit-"));
    const branch = await currentBranch(dir);
    expect(branch).toBeNull();
  });
});

describeGit("filesChangedSince", () => {
  let dir: string;
  afterEach(() => { if (dir) rmSync(dir, { recursive: true, force: true }); });

  it("returns committed changes between a commit SHA and HEAD", async () => {
    dir = await makeGitRepo();
    writeFileSync(join(dir, "a.txt"), "a");
    await git(dir, "add", "a.txt");
    await git(dir, "commit", "-q", "-m", "first");
    const base = await git(dir, "rev-parse", "HEAD");

    writeFileSync(join(dir, "b.txt"), "b");
    writeFileSync(join(dir, "a.txt"), "aa");
    await git(dir, "add", "a.txt", "b.txt");
    await git(dir, "commit", "-q", "-m", "second");

    const changes = await filesChangedSince(base, dir);
    expect(changes).toEqual(
      expect.arrayContaining([
        { path: "a.txt", action: "modified" },
        { path: "b.txt", action: "created" },
      ])
    );
    expect(changes.length).toBe(2);
  });

  it("reports deletions", async () => {
    dir = await makeGitRepo();
    writeFileSync(join(dir, "a.txt"), "a");
    await git(dir, "add", "a.txt");
    await git(dir, "commit", "-q", "-m", "first");
    const base = await git(dir, "rev-parse", "HEAD");

    await git(dir, "rm", "-q", "a.txt");
    await git(dir, "commit", "-q", "-m", "remove a");

    const changes = await filesChangedSince(base, dir);
    expect(changes).toEqual([{ path: "a.txt", action: "deleted" }]);
  });

  it("returns empty array in non-git dir", async () => {
    dir = mkdtempSync(join(tmpdir(), "cairn-nogit-"));
    const changes = await filesChangedSince("deadbeef", dir);
    expect(changes).toEqual([]);
  });

  it("returns empty array when nothing changed between two refs", async () => {
    dir = await makeGitRepo();
    writeFileSync(join(dir, "a.txt"), "a");
    await git(dir, "add", "a.txt");
    await git(dir, "commit", "-q", "-m", "first");
    const base = await git(dir, "rev-parse", "HEAD");

    const changes = await filesChangedSince(base, dir);
    expect(changes).toEqual([]);
  });
});

describeGit("uncommittedChanges", () => {
  let dir: string;
  afterEach(() => { if (dir) rmSync(dir, { recursive: true, force: true }); });

  it("reports untracked, modified, and staged files", async () => {
    dir = await makeGitRepo();
    writeFileSync(join(dir, "tracked.txt"), "original");
    await git(dir, "add", "tracked.txt");
    await git(dir, "commit", "-q", "-m", "init");

    writeFileSync(join(dir, "tracked.txt"), "modified");
    writeFileSync(join(dir, "untracked.txt"), "new");
    mkdirSync(join(dir, "sub"), { recursive: true });
    writeFileSync(join(dir, "sub", "staged.txt"), "staged");
    await git(dir, "add", "sub/staged.txt");

    const changes = await uncommittedChanges(dir);
    const paths = changes.map((c) => c.path).sort();
    expect(paths).toEqual(["sub/staged.txt", "tracked.txt", "untracked.txt"]);
    const modified = changes.find((c) => c.path === "tracked.txt");
    const created = changes.find((c) => c.path === "untracked.txt");
    expect(modified?.action).toBe("modified");
    expect(created?.action).toBe("created");
  });

  it("returns empty array on clean repo", async () => {
    dir = await makeGitRepo();
    writeFileSync(join(dir, "a.txt"), "a");
    await git(dir, "add", "a.txt");
    await git(dir, "commit", "-q", "-m", "init");
    const changes = await uncommittedChanges(dir);
    expect(changes).toEqual([]);
  });

  it("returns empty array in non-git dir", async () => {
    dir = mkdtempSync(join(tmpdir(), "cairn-nogit-"));
    const changes = await uncommittedChanges(dir);
    expect(changes).toEqual([]);
  });
});
