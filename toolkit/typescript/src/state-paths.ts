import { createHash } from "node:crypto";
import { basename, dirname } from "node:path";
export const pathDigest = (value: string): string => createHash("sha256").update(value).digest("hex").slice(0, 32);
export function namePart(value: string): string {
  let name = [...Buffer.from(value)].map(c => c >= 97 && c <= 122 || c >= 48 && c <= 57 || [95,46,126].includes(c) ? String.fromCharCode(c) : "%" + c.toString(16).toUpperCase().padStart(2,"0")).join("");
  if (["", ".", ".."].includes(name)) name = name.replaceAll(".", "%2E") || "%00";
  return name.length <= 180 ? name : name.slice(0,120) + "--" + pathDigest(value);
}
export function repositoryKey(forge: string, path: string): string {
  const name = [forge, ...path.split("/")].map(namePart).join("--");
  return name.length <= 240 ? name : name.slice(0,160) + "--" + pathDigest(forge + "/" + path);
}
export function localKey(common: string): string {
  const label = basename(common) === ".git" ? basename(dirname(common)) : basename(common);
  return `local--${namePart(label)}--${createHash("sha256").update(common).digest("hex").slice(0,16)}`;
}
