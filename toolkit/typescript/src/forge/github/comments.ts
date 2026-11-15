import type { Comment, CommentAnchor } from "../comment.js";
import type { PullRequestKey } from "../model.js";
import { records } from "../http.js";
type Row = Record<string, unknown>;
const object = (v: unknown): Row => v && typeof v === "object" && !Array.isArray(v) ? v as Row : {};
const text = (v: unknown): string | undefined => typeof v === "string" ? v : undefined;
const line = (v: unknown): number | undefined => Number.isSafeInteger(v) ? v as number : undefined;
export async function readComments(key: PullRequestKey, rows: (path: string) => Promise<readonly Row[]>, graphql: (query: string, variables: Row) => Promise<unknown>): Promise<readonly Comment[]> {
  const [owner, name] = key.repository.path.split("/");
  const threads = new Map<string, Row>();
  const cursors = new Set<string>();
  let cursor: unknown = null;
  let complete = false;
  const query = `query CommentThreads($owner:String!,$name:String!,$number:Int!,$cursor:String){repository(owner:$owner,name:$name){pullRequest(number:$number){reviewThreads(first:100,after:$cursor){nodes{id isResolved isOutdated resolvedBy{login} comments(first:1){nodes{fullDatabaseId}}} pageInfo{hasNextPage endCursor}}}}}`;
  for (let page = 0; page < 100; page++) {
    const response = object(await graphql(query, {
      owner, name, number: key.number, cursor
    }));
    if (response.errors)
      throw new Error("GitHub comment GraphQL observation failed");
    const connection = object(object(object(object(response.data).repository).pullRequest).reviewThreads);
    for (const thread of records("review threads", connection.nodes)) {
      const roots = records("thread root", object(thread.comments).nodes);
      const id = roots[0]?.fullDatabaseId;
      if (roots.length !== 1 || !id || typeof thread.id !== "string" || typeof thread.isResolved !== "boolean")
        throw new Error("Invalid GitHub comment thread");
      if (threads.has(String(id))) throw new Error("Duplicate GitHub thread root");
      threads.set(String(id), thread);
    }
    const info = object(connection.pageInfo);
    if (info.hasNextPage === false) {
      complete = true;
      break;
    }
    cursor = info.endCursor;
    if (typeof cursor !== "string" || !cursor || cursors.has(cursor))
      throw new Error("Invalid GitHub comment cursor");
    cursors.add(cursor);
  }
  if (!complete)
    throw new Error("GitHub comments exceed pagination limit");
  const conversation = await rows(`issues/${key.number}/comments`);
  const review = await rows(`pulls/${key.number}/comments`);
  const summaries = await rows(`pulls/${key.number}/reviews`);
  const roots = new Set(review.filter(r => !r.in_reply_to_id).map(r => String(r.id)));
  if (roots.size !== threads.size || [...roots].some(id => !threads.has(id)))
    throw new Error("GitHub comment/thread mismatch; refresh required");
  function convert(row: Row, kind: string): Comment {
    if (!row.id || typeof row.body !== "string")
      throw new Error("Invalid GitHub comment");
    const parent = row.in_reply_to_id;
    const thread = kind === "review" ? threads.get(String(parent ?? row.id)) : undefined;
    if (kind === "review" && !thread)
      throw new Error("GitHub reply has no observed parent");
    let anchor: CommentAnchor | undefined;
    if (kind === "review" && typeof row.path === "string")
      anchor = {
        path: row.path, revision: text(row.commit_id) ?? "", side: row.side === "LEFT" ? "LEFT" : row.side === "RIGHT" ? "RIGHT" : undefined, line: line(row.line), startLine: line(row.start_line), startSide: row.start_side === "LEFT" ? "LEFT" : row.start_side === "RIGHT" ? "RIGHT" : undefined, originalRevision: text(row.original_commit_id), originalLine: line(row.original_line), originalStartLine: line(row.original_start_line), outdated: typeof thread?.isOutdated === "boolean" ? thread.isOutdated : undefined
      };
    return {
      id: `${kind}:${row.id}`, pr: key, body: row.body, author: text(object(row.user).login), createdAt: text(row.created_at) ?? text(row.submitted_at) ?? "", updatedAt: text(row.updated_at), url: text(row.html_url), replyToId: parent ? `review:${parent}` : undefined, anchor, resolution: thread ? (thread.isResolved ? "resolved" : "unresolved") : "unsupported", resolvedBy: text(object(thread?.resolvedBy).login)
    };
  }
  const result = [...conversation.map(r => convert(r, "conversation")), ...review.map(r => convert(r, "review")), ...summaries.filter(r => r.state !== "PENDING" && r.submitted_at && r.body).map(r => convert(r, "summary"))];
  if (new Set(result.map(c => c.id)).size !== result.length)
    throw new Error("Duplicate GitHub comment identity");
  return result.sort((a, b) => a.createdAt.localeCompare(b.createdAt));
}
