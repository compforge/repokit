import { checkoutEnvironment } from "./environment.js";
import { spawnSync } from "node:child_process";

export interface GitResult {
  readonly code: number;
  readonly stdout: string;
  readonly stderr: string;
  readonly ok: boolean;
  readonly uncertain: boolean;
}

/** A timed-out write may have taken effect; inspect before retrying it. */
export function runGit(repo: string, args: readonly string[], timeoutMs = 5_000, raw = false): GitResult {
  const result = spawnSync("git", ["-C", repo, ...args], {
    env: checkoutEnvironment(), encoding: "utf8", timeout: timeoutMs, maxBuffer: 16 << 20,
  });
  const code = result.status ?? -1;
  return { code, stdout: (raw || result.stdout?.includes("\0")) ? (result.stdout ?? "") : (result.stdout ?? "").trim(),
    stderr: (result.stderr || result.error?.message || "").trim(), ok: code === 0,
    uncertain: (result.error as NodeJS.ErrnoException | undefined)?.code === "ETIMEDOUT" };
}

export function changedPaths(repo: string): readonly string[] {
  const tracked = runGit(repo, ["diff", "--no-renames", "--name-only", "-z", "HEAD", "--"]);
  const untracked = runGit(repo, ["ls-files", "--others", "--exclude-standard", "-z"]);
  if (!tracked.ok || !untracked.ok) throw new Error(tracked.stderr || untracked.stderr);
  return [...new Set((tracked.stdout + untracked.stdout).split("\0").filter(p => p && !p.endsWith("/")))];
}

function resolveCommit(repo: string, ref: string): string {
  const result = runGit(repo, ["rev-parse", "--verify", "--end-of-options", `${ref}^{commit}`]);
  if (!result.ok) throw new Error(result.stderr);
  return result.stdout;
}

function paths(repo: string, args: readonly string[]): readonly string[] {
  const result = runGit(repo, args);
  if (!result.ok) throw new Error(result.stderr);
  return [...new Set(result.stdout.split("\0").filter(Boolean))];
}

/** One commit relative to its first parent, or the empty tree for a root commit. */
export function committedPaths(repo: string, rev = "HEAD"): readonly string[] {
  return paths(repo, ["diff-tree", "--root", "--diff-merges=first-parent", "--no-commit-id", "-r",
    "--no-renames", "--name-only", "-z", resolveCommit(repo, rev), "--"]);
}

/** Branch changes from the merge base to head, including both rename sides. */
export function rangePaths(repo: string, base: string, head = "HEAD"): readonly string[] {
  return paths(repo, ["diff", "--no-renames", "--name-only", "-z",
    `${resolveCommit(repo, base)}...${resolveCommit(repo, head)}`, "--"]);
}
