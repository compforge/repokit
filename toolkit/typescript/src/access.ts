import { loadConfig } from "./config.js";
import { remote } from "./git/remote.js";
import type { PullRequestContext } from "./forge/observation/context.js";
import type { ObservationReader } from "./forge/observation/model.js";

/**
 * Native checkout-to-Forge assembly; callers select targets and own the observer.
 * @spec Absent Forge origin is inapplicable; missing access remains an error.
 * @see {@link component://docs/configuration.md}
 */
export async function pullRequestContext(checkout: string, options: {
  readonly home?: string;
  readonly timeoutMs?: number;
  readonly targets?: readonly string[];
  readonly reader?: ObservationReader;
} = {}): Promise<PullRequestContext | undefined> {
  const origin = await remote(checkout);
  if (!origin) return undefined;
  const connection = (await loadConfig(options.home)).forge(origin.host, { timeoutMs: options.timeoutMs });
  return connection.pullRequestContext(checkout, { source: origin.path, targets: options.targets, reader: options.reader });
}
