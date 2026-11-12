# Commit

`git/commit` owns explicit content selection, candidate index preparation and native Git commit
confirmation. It does not own sensitive-file policies, branch protection, sessions, validation,
review or publication. Go exposes `Plan`, `Create` and `Check`; CLI maps them to
`repocli git commit plan/create/check`. No repository state cache or workflow record is added.

## Selection and planning

Select exactly one mode: `staged` (default) preserves existing partial staging, `tracked` stages
tracked working changes plus existing staged additions, and `files` stages literal file/directory
paths including non-ignored untracked files. `--file` repeats one checkout-root-relative path;
there is no cwd guessing, glob expansion or implicit untracked inclusion in tracked mode.
The complete candidate index must be within files-mode scope; staged renames therefore require
both old and new paths. Index-only deletions are retained even when the local file remains.
Unmerged entries and active merge/rebase/cherry-pick/revert operations are rejected.
Initial commits use `head: unborn`; detached HEAD is unsupported.

Plan builds an isolated index under the checkout Git directory, borrowing the native index writer
lock. Like Python `git.stage(validate=...)`, rejection preserves the original index byte for byte,
including partial hunks. Planning can run clean filters and write Git objects; it does not change
the real index, HEAD or working files itself. Filter side effects are not rolled back.
Changed gitlinks must be registered in the candidate `.gitmodules`; changing that manifest checks
all candidate gitlinks. Deleting a gitlink is permitted.

The plan returns branch, parent (`head`), candidate `tree`, the original byte-level
`indexFingerprint`, normalized selection, and full candidate changes (path and old/new mode).
Callers inspect these facts to apply policy. `empty` means no difference from the parent tree.

## Create and confirmation

Create requires the planned branch/head/tree/index fingerprint, the same selection and a message.
It reacquires the common Repository operation lease and native index lock, rebuilds the candidate,
and rejects changed inputs before invoking Git. The native `git commit` runs once with that
candidate as `GIT_INDEX_FILE`; configured signing and Git hooks remain enabled. The native
index lock prevents cooperating index writers from replacing the real index during the operation.
Raw ref writers and hook side effects are not a repository-wide transaction: the observed result
must still be checked. Hooks see the candidate index and the actual checkout/branch.

Only an observed branch tip with exactly the planned tree and parent confirms the result.
A hook may edit the message normally. If a hook changes the committed tree, the actual SHA/tree/
parents are returned with a mismatch failure; no reset, retry or push is performed. Hook working-file
changes are retained. A hook rejection leaves the original index untouched and preserves the
candidate. Successful commit confirmation followed by failed index installation is reported as a
warning with `confirmed: true`, `indexInstalled: false`; callers stop for index repair without
repeating the commit. Post-commit index changes made by hooks are retained when installing it.
An empty candidate creates no commit and returns `empty: true`, `attempted: false`.

`recoveryPath` identifies an attempt's retained candidate index, intent and message inside the
checkout Git directory. It is saved before invoking commit; interrupted processes may also leave
`index.lock`. Inspect these artifacts and live Git processes before manually removing locks.
Successful confirmation and installation remove the temporary directory. Unknown writes never
trigger automatic retries. `check` observes the current checkout branch tip against the original
branch/head/tree without taking an index lock, staging, running filters/hooks or committing.
A known different tip yields successful observation with `confirmed: false`; a query failure stays
unknown to the caller. If the branch advanced again, inspect its history and the retained intent.

## Output and cost

Reports expose the intent, candidate changes, actual commit/tree/parents, `attempted`, `confirmed`,
`indexInstalled`, diagnostics and recovery path. Confirmation describes observed content, not
project validation. Phases are `lock_wait`, `prepare`, `commit`, `verify` and `install_index`;
CLI projects them under `cost.phases` as `git.commit.*`. A lost Git response can be reconciled by
matching commit evidence; cancellation that prevents verification remains uncertain.
