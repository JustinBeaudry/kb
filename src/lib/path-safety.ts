import { existsSync, lstatSync, realpathSync } from "node:fs";
import { isAbsolute, relative, resolve } from "node:path";

export class PathUnsafeError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "PathUnsafeError";
  }
}

export function isWithin(child: string, parent: string): boolean {
  const childAbs = resolve(child);
  const parentAbs = resolve(parent);
  if (childAbs === parentAbs) return true;
  const rel = relative(parentAbs, childAbs);
  return rel !== "" && !rel.startsWith("..") && !isAbsolute(rel);
}

export function assertSafeFilename(name: string): void {
  if (isAbsolute(name)) {
    throw new PathUnsafeError(`invalid filename (absolute path): ${name}`);
  }
  const segments = name.split(/[\\/]/);
  if (segments.includes("..")) {
    throw new PathUnsafeError(`invalid filename (traversal): ${name}`);
  }
  if (name.includes("\0")) {
    throw new PathUnsafeError("invalid filename (null byte)");
  }
}

/**
 * Verify that `dir` is a real directory inside `vaultPath` and not a symlink
 * pointing somewhere else. Protects against scope hijacks where raw/, sessions/,
 * or wiki/ has been swapped for a symlink.
 */
export function assertGenuineScopeDir(dir: string, vaultPath: string): void {
  if (!existsSync(dir)) {
    throw new PathUnsafeError(`scope directory missing: ${dir}`);
  }
  const lst = lstatSync(dir);
  if (lst.isSymbolicLink()) {
    throw new PathUnsafeError(`scope directory is a symlink: ${dir}`);
  }
  if (!lst.isDirectory()) {
    throw new PathUnsafeError(`scope is not a directory: ${dir}`);
  }
  const realDir = realpathSync(dir);
  const realVault = realpathSync(vaultPath);
  if (!isWithin(realDir, realVault)) {
    throw new PathUnsafeError(`scope directory escapes vault: ${dir}`);
  }
}
