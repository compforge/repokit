import { createHash } from "node:crypto";
import { resolveHome } from "../home.js";
import type { RepositoryKey } from "@compforge/harness-common";
import { createForgeReader } from "./create.js";
import { ForgeAuthError, type ForgeReader, type ForgeReaderOptions } from "./model.js";
import { PullRequestContext } from "./observation/context.js";
import type { ObservationReader } from "./observation/model.js";

export function detectProvider(host: string, explicitType?: string): "github" | "gitlab" {
  if (explicitType === "github" || explicitType === "gitlab") return explicitType;
  const lower = host.toLowerCase();
  return lower === "github.com" || lower.startsWith("github.") ? "github" : "gitlab";
}

export interface ForgeConnectionConfig {
  readonly host: string;
  readonly token: string;
  readonly provider?: "github" | "gitlab";
  readonly apiHost?: string;
  readonly home?: string;
  readonly timeoutMs?: number;
}

/**
 * Explicit access configuration; never reads application config or persists credentials.
 * @spec Provider, Forge identity, endpoint and credential changes isolate observation caches.
 * @see {@link component://docs/pr.md}
 */
export class ForgeConnection {
  readonly host: string;
  readonly provider: "github" | "gitlab";
  readonly apiHost: string;
  readonly home: string;
  readonly timeoutMs: number;
  readonly #token: string;
  constructor(config: ForgeConnectionConfig) {
    this.host = config.host.toLowerCase();
    this.provider = detectProvider(this.host, config.provider);
    this.apiHost = (config.apiHost || this.host).toLowerCase();
    this.home = resolveHome(config.home);
    this.timeoutMs = config.timeoutMs ?? 10_000;
    if (!this.host || !Number.isFinite(this.timeoutMs) || this.timeoutMs <= 0) throw new Error("Forge host and positive timeout are required");
    if (!config.token.trim()) throw new ForgeAuthError(`PR query unavailable: no credential for ${this.host}`);
    this.#token = config.token;
  }
  get name(): string { return this.host === "github.com" ? "github" : this.host === "gitlab.com" ? "gitlab" : this.host; }
  get identity(): string {
    return createHash("sha256").update(JSON.stringify([this.provider, this.name, this.apiHost, this.#token])).digest("hex");
  }
  repository(path: string): RepositoryKey {
    if (!path || path.startsWith("/") || path.endsWith("/")) throw new Error("nonempty repository path is required");
    return { forge: { name: this.name }, path };
  }
  reader(options: ForgeReaderOptions = {}): ForgeReader {
    return createForgeReader({ forge: this.name, provider: this.provider, host: this.apiHost, token: this.#token }, { timeoutMs: this.timeoutMs, ...options });
  }
  pullRequestContext(checkout: string, options: { readonly source: string; readonly targets?: readonly string[]; readonly reader?: ObservationReader }): PullRequestContext {
    return new PullRequestContext(options.reader ?? this.reader(), {
      checkout, source: this.repository(options.source),
      targets: (options.targets ?? [options.source]).map(path => this.repository(path)),
      home: this.home, connection: this.identity, requestTimeoutMs: this.timeoutMs,
    });
  }
}
