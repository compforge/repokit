import { posix } from "node:path";
import { parse as toml } from "smol-toml";
import { parse as yaml } from "yaml";
import type { Bin, Depot } from "@compforge/harness-common";
import type { Catalog } from "./layout.js";

function object(value: unknown): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) throw new Error("expected an object");
  return value as Record<string, unknown>;
}
function strings(value: unknown): string[] {
  if (!Array.isArray(value) || !value.every(v => typeof v === "string")) throw new Error("expected workspace patterns");
  return value;
}
const field = (v: Record<string, unknown>, key: string): unknown => Object.entries(v).filter(([k]) => k.toLowerCase() === key.toLowerCase()).at(-1)?.[1];

function patterns(files: Catalog, root: string, python: boolean): string[] {
  const data = (name: string) => files.get(posix.join(root, name))?.toString() ?? "";
  const exists = (name: string) => files.has(posix.join(root, name));
  let values: string[] = [];
  if (python) {
    if (exists("pyproject.toml")) {
      const metadata = toml(data("pyproject.toml"));
      const workspace = object(object(object(metadata.tool ?? {}).uv ?? {}).workspace ?? {});
      values = [...strings(workspace.members ?? []), ...strings(workspace.exclude ?? []).map(p => "!" + p)];
    }
  } else if (exists("pnpm-workspace.yaml")) {
    values = strings(object(yaml(data("pnpm-workspace.yaml")) ?? {}).packages ?? []);
  } else if (exists("package.json")) {
    const metadata = object(JSON.parse(data("package.json")) ?? {});
    let workspaces = field(metadata, "workspaces") ?? [];
    if (!Array.isArray(workspaces) && typeof workspaces === "object") workspaces = field(object(workspaces), "packages") ?? [];
    values = strings(workspaces);
  }
  for (const value of values) {
    const pattern = value.replace(/^!/, "");
    if (/[\\\x00?\[\]{}()]/.test(pattern) || pattern.split("/").some(p => !p || p === "." || p === "..")) throw new Error("unsupported workspace pattern");
  }
  return values;
}
function match(pattern: string, name: string): boolean {
  const parts = name.split("/");
  let states = [true, ...parts.map(() => false)];
  for (const segment of pattern.split("/")) {
    const next = states.map(() => false);
    const expression = new RegExp("^" + segment.split("*").map(p => p.replace(/[.*+?^${}()|[\]\\]/g, "\\$&")).join(".*") + "$");
    for (let i = 0; i < states.length; i++) {
      if (segment === "**") next[i] = states[i] || (i > 0 && next[i - 1]);
      else if (i > 0 && states[i - 1]) next[i] = expression.test(parts[i - 1]);
    }
    states = next;
  }
  return states.at(-1)!;
}
function installationRoot(files: Catalog, component: string, python: boolean): string {
  let selected = component;
  for (let ancestor = component; ; ancestor = posix.dirname(ancestor)) {
    const globs = patterns(files, ancestor, python);
    const relative = posix.relative(ancestor, selected);
    if (ancestor !== selected && globs.some(p => !p.startsWith("!") && match(p, relative)) && !globs.some(p => p.startsWith("!") && match(p.slice(1), relative))) selected = ancestor;
    if (ancestor === ".") return selected;
  }
}
/** @spec Standalone Node packages without dependency, lockfile or workspace evidence need no Bin. */
function nodeInstallationRequired(files: Catalog, root: string): boolean {
  if (["npm-shrinkwrap.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb"].some(name => files.has(posix.join(root, name)))) return true;
  if (patterns(files, root, false).length) return true;
  const metadata = object(JSON.parse(files.get(posix.join(root, "package.json"))!.toString()) ?? {});
  return ["dependencies", "devDependencies", "optionalDependencies", "peerDependencies", "bundledDependencies", "bundleDependencies"].some(name => {
    const value: unknown = metadata[name];
    return value != null && (typeof value !== "object" || Object.keys(value).length > 0);
  });
}

/**
 * @spec Each Component has at most one logical Depot; workspace members may share Bin paths.
 * @rule Discovery never inspects generated directories or authorizes their deletion.
 * @see {@link component://docs/depot.md}
 */
export function discoverDepot(files: Catalog, component: string): Depot | undefined {
  const bins: Bin[] = [];
  for (const [python, manifest, directory] of [[false, "package.json", "node_modules"], [true, "pyproject.toml", ".venv"]] as const) {
    if (!files.has(posix.join(component, manifest))) continue;
    try {
      const root = installationRoot(files, component, python);
      if (python) {
        if (!files.has(posix.join(root, "uv.lock"))) continue;
      } else {
        if (!nodeInstallationRequired(files, root)) continue;
        if (files.has(posix.join(root, ".pnp.cjs"))) continue;
        if (files.has(posix.join(root, ".yarnrc.yml"))) {
          const config = object(yaml(files.get(posix.join(root, ".yarnrc.yml"))?.toString() ?? "") ?? {});
          if (config.nodeLinker === "pnp") continue;
        }
      }
      bins.push({path: posix.join(root, directory), kind: "dependencies"});
    } catch { /* Unsupported/malformed metadata cannot justify an installation root. */ }
  }
  return bins.length ? { bins: bins.sort((a,b) => Buffer.compare(Buffer.from(a.path),Buffer.from(b.path))) } : undefined;
}
