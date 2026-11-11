# Recoverable rebase

RepoKit owns explicit Git rebase execution, recovery evidence and exact-lease publication.
The caller chooses the source/target, decides whether rewriting is permitted, resolves conflicts,
validates the resulting tree and authorizes publication. RepoKit does not query PRs, acquire
application session ownership or run tests inside this operation.

## Commands and API

```sh
repocli git rebase start --branch feature --target main --remote origin --repo . --json
repocli git rebase status --repo . --json
# Resolve and stage conflicts, then continue as needed.
repocli git rebase continue --repo . --json
repocli validate --repo . --base origin/main --json
# Explicitly publish the completed rewrite after inspecting validation.
repocli git rebase finish --repo . --json
# Only an in-progress rebase can restore its original checkout.
repocli git rebase abort --repo . --json
```

Go callers use `git/rebase.Run(ctx, repo, action, Options{Branch, Target, Remote})`.
`start` requires the exact checked-out source branch and an explicit remote target branch;
`remote` defaults to `origin`. Other actions use the saved transaction. There is no implicit
stash, conflict resolution, skipped commit, hard reset, merge, PR update or automatic push.

`start` requires a clean checkout and no active transaction or unmanaged Git rebase. It captures
the source SHA at the configured remote's single push destination, fetches its objects and
requires the local source to contain that commit. The target is refreshed from the fetch remote
and fixed to a commit before rewriting; moving tracking refs cannot change the replay target.
Automatic stashing and updating other local branch refs are disabled. A paused Git rebase is
recognized by its saved source ref, original HEAD and onto commit, not just directory existence.

## Recovery ownership

The `git/rebase` package owns a private `repokit-rebase.json` in the checkout's Git directory.
It records source/target, original HEAD, target commit, expected remote SHA, destination,
publication commit and phase. Linked worktrees have independent records. It is operation
recovery evidence, not an expiring observation in RepoKit home. Records are atomically replaced
with private permissions and synchronized before a rewrite or push. Transport destinations
stay private and are never included in public reports or error messages.

Calls use the same repository lifecycle file lock as worktree creation/cleanup and Depot
preparation. Lock waiting respects the caller's context; no lease expires mid-operation.
The lock coordinates RepoKit callers, not arbitrary Git invocations. Git's native locks and
identity checks remain necessary; callers must avoid simultaneous manual checkout mutations.
State supplies no rebase rules and does not read or maintain the recovery record.

Terminal records (`published`, `aborted`, `failed`) remain available for status and repeated
confirmation; the next explicit `start` replaces them. Active or corrupt records are never
silently discarded. A completed rebase is not automatically reset by `abort`. An interrupted
start observed back at its original HEAD with no native rebase may be explicitly abandoned
with `abort`; an ambiguous changed checkout requires manual inspection.

## Results and publication

Reports contain `checkout`, `action`, `phase`, `transaction`, `headSha`, `conflicts`,
`writeAttempted`, `pushAttempted`, `confirmed`, `diagnostics` and measured `phases`.
`writeAttempted` describes history/index-changing Git execution, not fetch or record I/O.

| Phase | Meaning and next action |
|---|---|
| `none` | No RepoKit or Git rebase is present. |
| `unmanaged` | Git has a rebase not owned by RepoKit; do not adopt it implicitly. |
| `prepared` | Rewrite intent was saved; execution has not yet been confirmed. |
| `paused` | Matching native Git rebase remains; resolve/stage and continue, or abort. |
| `ready` | Local HEAD contains the fixed target and native rebase is finished; validate before finish. |
| `publishing` | Publication commit is frozen; finish first reconciles the remote. |
| `published` | Finish confirmed the destination branch equals the publication commit. |
| `aborted` | Original checkout was restored, or an unchanged interrupted start was abandoned. |
| `failed` | Rewrite failed without changing the starting HEAD or leaving a native rebase. |
| `unknown` | Available facts cannot confirm completion; inspect before any further write. |

A confirmed `paused` result is successful command execution, not a completed rebase. `status`
observes local facts without fetching or pushing; its `published` phase is a retained prior
confirmation, not a fresh remote read. `finish` always observes the remote again. Failed reads
are errors, never an absent rebase or an absent remote branch. Interrupted writes return
`git.rebase_unknown` with `uncertain=true` and keep the record.

Before the first push, `finish` saves the intended commit. It observes the same push destination
used at start: equality with the intended commit means publication is already complete,
including recovery after a lost response. Otherwise only the originally captured remote SHA
permits a push with explicit `--force-with-lease=<ref>:<sha>`. A changed destination, moved/missing
remote source, changed HEAD after publication intent, or incomplete local rewrite is rejected.
Target movement does not silently change the captured replay baseline.

Push sends only the frozen commit to the source branch, without following tags or recursively
pushing submodules. Both successful and unsuccessful pushes are followed by remote observation.
A porcelain rejection is a definite failure; missing confirmation remains unknown. There is no
automatic retry. Confirmed Git effects survive a record-publication failure as warnings, with
recovery guidance; they are never turned into a request to replay the completed write.

Local queries are bounded to 5 seconds, remote observations to 30 seconds, fetch/rewrite/push
to 2 minutes, always within the caller context. Push reserves part of the remaining budget for
verification. CLI `cost.phases` maps lock wait, inspection, remote preparation, rewrite/continue/
abort, verification and push phases; unexecuted phases are absent.
