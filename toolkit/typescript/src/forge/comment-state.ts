import { readdir, rm } from "node:fs/promises";
import type { Comment } from "./comment.js";
import type { ForgeReaderConfig, PullRequestKey } from "./model.js";
import { resolveHome } from "../state/home.js";
import { recordAccess } from "../state/activity.js";
import { namePart, repositoryKey } from "../state/identity.js";
import { ObservationStore, digest } from "../state/store.js";

/** Disposable observations; IDs, membership and reply relationships always come from Forge. */
export class CommentState {
  readonly #store: ObservationStore;
  readonly #connection: string;
  constructor(config: ForgeReaderConfig, home?: string) {
    this.#store = new ObservationStore(resolveHome(home));
    this.#connection = digest([config.forge, digest([config.provider, config.forge, config.host.toLowerCase(), config.token])]);
  }
  async capture(key: PullRequestKey, rows: readonly Comment[], signal: AbortSignal): Promise<void> {
    try {
      const store = this.#store, connection = this.#connection;
      const repository = repositoryKey(key.repository.forge.name, key.repository.path);
      const directory = store.path("repositories", repository);
      const root = store.path("repositories", repository, "prs", String(key.number), "comments");
      await recordAccess(store, repository, signal,undefined,["prs",String(key.number),"comments"]);
      await store.locked(root, signal, async owned => {
        await store.write(`${directory}/repository.json`, key.repository, owned);
        const entries = await readdir(root, { withFileTypes: true }).catch((error: NodeJS.ErrnoException) => {
          if (error.code === "ENOENT") return []; throw error;
        });
        for (const entry of entries) {
          if (entry.isDirectory()) { owned(); await rm(`${root}/${entry.name}/${connection}.json`, { force: true }); }
        }
        const observedAt = new Date().toISOString();
        for (const comment of rows) {
          await store.write(`${root}/${namePart(comment.id)}/${connection}.json`, { version: 1, connection, observedAt, comment }, owned);
        }
      });
    } catch (error) {
      process.emitWarning(`comment observation publication failed for ${key.repository.path}#${key.number}: ${String(error)}`);
    }
  }
}
