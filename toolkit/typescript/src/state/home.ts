import { homedir } from "node:os";
import { join, resolve } from "node:path";

/** Resolve the shared user directory without creating or migrating files.
 * @spec Explicit home wins over REPOKIT_HOME, then ~/.repokit; omission never disables persistence.
 * @see {@link component://docs/state.md}
 */
export function resolveHome(home?: string): string {
  const value=home || process.env.REPOKIT_HOME || join(homedir(),".repokit");
  if(!value.trim())throw new Error("RepoKit home must be nonempty");
  return resolve(value);
}
