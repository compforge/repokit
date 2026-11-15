import type { Comment, CommentResolution } from "../comment.js";
import type { PullRequestKey } from "../model.js";
import { records } from "../http.js";
type Row = Record<string, unknown>;
const object = (v: unknown): Row => v && typeof v === "object" && !Array.isArray(v) ? v as Row : {};
const text = (v: unknown): string | undefined => typeof v === "string" ? v : undefined;
const line = (v: unknown): number | undefined => Number.isSafeInteger(v) ? v as number : undefined;
export async function readComments(key: PullRequestKey, page: (page: number) => Promise<{
  readonly data: unknown;
  readonly headers: Headers;
}>): Promise<readonly Comment[]> {
  const result: Comment[] = [];
  let number = 1;
  for (let count = 0; count < 100; count++) {
    const { data, headers } = await page(number);
    const discussions = records("GitLab discussions", data);
    for (const d of discussions) {
      if (!d.id || typeof d.individual_note !== "boolean")
        throw new Error("Invalid GitLab discussion");
      const notes = records("GitLab notes", d.notes).filter(n => n.system !== true);
      const relevant = notes.filter(n => n.resolvable !== false);
      const resolution: CommentResolution = d.individual_note || !relevant.length ? "unsupported" : relevant.some(n => n.resolved === false) ? "unresolved" : relevant.every(n => n.resolved === true) ? "resolved" : "unknown";
      for (const n of notes) {
        if (!n.id || typeof n.body !== "string")
          throw new Error("Invalid GitLab note");
        const p = object(n.position), start = object(object(p.line_range).start);
        const side = p.new_line != null ? "RIGHT" : p.old_line != null ? "LEFT" : undefined;
        result.push({
          id: `note:${n.id}`, pr: key, body: n.body, author: text(object(n.author).username), createdAt: text(n.created_at) ?? "", updatedAt: text(n.updated_at), url: text(n.url), resolution, resolvedBy: text(object(n.resolved_by).username), resolvedAt: text(n.resolved_at), anchor: n.position ? {
            path: text(p.new_path) || text(p.old_path) || "", oldPath: text(p.old_path), revision: text(p.head_sha) ?? "", originalRevision: text(p.base_sha), side, line: line(side === "RIGHT" ? p.new_line : p.old_line), startLine: line(start.type === "new" ? start.new_line : start.old_line), startSide: start.type === "new" ? "RIGHT" : start.type === "old" ? "LEFT" : undefined
          } : undefined
        });
      }
    }
    const next = headers.get("x-next-page");
    if (!next && discussions.length < 100 || next === "") {
      if (new Set(result.map(c => c.id)).size !== result.length)
        throw new Error("Duplicate GitLab comment identity");
      return result.sort((a, b) => a.createdAt.localeCompare(b.createdAt));
    }
    const parsed = next ? Number(next) : number + 1;
    if (!Number.isSafeInteger(parsed) || parsed <= number)
      throw new Error("Invalid GitLab comment pagination");
    number = parsed;
  }
  throw new Error("GitLab comments exceed pagination limit");
}
