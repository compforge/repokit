import { RepoKitError } from "./errors.js";
import { isIP } from "node:net";
import { open } from "node:fs/promises";
import { join } from "node:path";
import { ForgeConnection } from "./forge/connection.js";
import { ForgeAuthError } from "./forge/model.js";
import { resolveHome } from "./state/home.js";

type Entry = { provider?: string; apiHost?: string; token?: string };

export class ConfigError extends RepoKitError {
  constructor(message: string = "", options?: ErrorOptions) { super("config.invalid", message, options); this.name = "ConfigError"; }
}

// Use the shared host grammar, independent of WHATWG URL normalization.
function normalizeHost(value: string): string {
  const name = value.toLowerCase();
  let host = name;
  if (name === "github") host = "github.com";
  else if (name === "gitlab") host = "gitlab.com";
  const invalid = () => new ConfigError("Invalid Forge host");
  let port = "", hasPort = false;
  if (host.startsWith("[")) {
    const end = host.indexOf("]"), name = host.slice(1,end);
    if (end < 0 || name.includes("%") || isIP(name) !== 6) throw invalid();
    const suffix = host.slice(end+1);
    if (suffix && !suffix.startsWith(":")) throw invalid();
    port = suffix.slice(1); hasPort = !!suffix;
  } else {
    const colon = host.indexOf(":"), name = colon < 0 ? host : host.slice(0,colon);
    hasPort = colon >= 0; port = hasPort ? host.slice(colon+1) : "";
    if (name.length > 253 || name.split(".").some(label => !/^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$/.test(label))) throw invalid();
    if (/^[0-9.]+$/.test(name) && isIP(name) !== 4) throw invalid();
  }
  if (hasPort && (!/^[0-9]{1,5}$/.test(port) || Number(port) < 1 || Number(port) > 65535)) throw invalid();
  return host;
}

/** Read-only settings, separate from repository state and workflow policy. */
export class RepoKitConfig {
  readonly #forges: ReadonlyMap<string, Entry>;
  constructor(readonly home: string, entries: ReadonlyMap<string, Entry> = new Map()) {
    this.#forges = entries;
  }
  /**
   * Resolve access without network I/O.
   * @spec Credentials come only from the selected host in config.json. Environment variables
   * and caller configuration never supply or override credentials.
   * @see {@link component://docs/configuration.md}
   */
  forge(name: string, options: { readonly timeoutMs?: number } = {}): ForgeConnection {
    const host = normalizeHost(name);
    const entry = this.#forges.get(host) ?? {};
    const provider = entry.provider || ({"github.com":"github", "gitlab.com":"gitlab"} as Record<string,string>)[host];
    if (provider !== "github" && provider !== "gitlab") throw new ConfigError(`Configure a provider for Forge ${host} in ${join(this.home,"config.json")}`);
    const token = (entry.token ?? "").trim();
    if (!token) throw new ForgeAuthError(`no ${provider} credential for ${host}; configure ${join(this.home,"config.json")}`);
    return new ForgeConnection({host, provider, token, apiHost: entry.apiHost || undefined, home: this.home, timeoutMs: options.timeoutMs});
  }
}

function object(value: unknown): value is Record<string,unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

/**
 * Load once per operation; no file creation or application-owned config lookup.
 * @spec Missing config is empty; invalid or non-private config fails without exposing contents.
 * @see {@link component://docs/configuration.md}
 */
export async function loadConfig(home?: string): Promise<RepoKitConfig> {
  const root = resolveHome(home), path = join(root,"config.json");
  let file;
  try { file = await open(path,"r"); }
  catch (error) {
    if ((error as NodeJS.ErrnoException).code === "ENOENT") return new RepoKitConfig(root);
    throw error;
  }
  let document: unknown;
  try {
    const stat = await file.stat();
    if (!stat.isFile() || (stat.mode & 0o077)) throw new ConfigError(`RepoKit config must be a private regular file (chmod 600): ${path}`);
    const contents = await file.readFile("utf8");
    try { document = JSON.parse(contents); }
    catch { throw new ConfigError(`Invalid RepoKit JSON configuration: ${path}`); }
  } finally { await file.close(); }
  if (!object(document) || Object.keys(document).some(k => k !== "forges")) throw new ConfigError(`Expected a JSON object with a forges map: ${path}`);
  const raw = document.forges ?? {};
  if (!object(raw)) throw new ConfigError(`Expected a forges map: ${path}`);
  const entries = new Map<string,Entry>();
  for (const [key, rawEntry] of Object.entries(raw)) {
    const host = normalizeHost(key), value = rawEntry ?? {};
    if (entries.has(host)) throw new ConfigError(`Duplicate normalized Forge host: ${path}`);
    if (!object(value) || Object.keys(value).some(k => !["provider","apiHost","token"].includes(k))) throw new ConfigError(`Invalid Forge configuration fields: ${path}`);
    if (Object.values(value).some(v => v !== null && typeof v !== "string")) throw new ConfigError(`Forge configuration fields must be strings: ${path}`);
    const entry: Entry = Object.fromEntries(Object.entries(value).map(([k,v]) => [k,v ?? ""]));
    if (entry.provider && !["github","gitlab"].includes(entry.provider)) throw new ConfigError(`Provider must be github or gitlab: ${path}`);
    if (entry.apiHost) entry.apiHost = normalizeHost(entry.apiHost);
    entries.set(host,entry);
  }
  return new RepoKitConfig(root,entries);
}
