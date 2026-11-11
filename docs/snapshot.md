# Snapshot

Snapshot identifies the contents observed from a working tree, index or commit and
retains capture gaps. Its digest is a comparison aid, not a backup or an atomic
filesystem snapshot. The command captures content without interpreting project
configuration or constructing a graph.
Use [inspect](repository.md) for repository organization and package-tool evidence.

```sh
repocli snapshot --repo /path/to/repo --json
repocli snapshot --staged --json
repocli snapshot --head HEAD --json
```

The default reads current bytes of tracked and non-ignored untracked files, including
staged and unstaged changes and excluding deleted files. It works before the first
commit and from linked worktrees or repository subdirectories. `--staged` reads
index blobs; `--head` resolves a commit once and reads its tree. These options are
mutually exclusive. The command inherits `--timeout`, text/JSON output and execution
logging from the CLI.

Untracked embedded Git repositories, including linked worktrees inside the checkout,
are separate repository boundaries and do not enter the parent snapshot or diff.
Run the command inside that checkout to inspect it. Ordinary untracked files in
sibling directories remain included unless Git ignore rules exclude them. Tracked submodules follow the gitlink rules below.

## Report and comparison

The command's JSON schema is version 2, independent of the diff report's schema:

- `checkout`: resolved working-tree root; `input`: `working_tree`, `index` or `commit`.
- `head`: resolved commit ID, only for commit input.
- `snapshot`: `sha256:` digest; `fileCount`: included regular files.
- `complete`: whether capture completed without known gaps or detected changes.
- `diagnostics`: codes, messages and file paths when available; an empty array on a complete capture.

Schema 2 removes `repository`, `components`, and manifest `observations`; query organization through
`inspect`. The current digest contract is v2, described below.

The digest is the same sorted, length-framed path/content hash used by `diff`.
Identical complete contents produce the same digest across working tree, index and
commit inputs. It includes all captured regular files, not just source files or
changed files. File modes, timestamps, ignored files and external dependencies are
outside this content identity. Incomplete captures also hash their issue messages.

Internal symlinks contribute their link text and refer to already captured files;
links are never followed outside the captured input. External, cyclic, missing or
ignored targets remain incomplete. Gitlinks contribute only their OID. Working-tree
capture reads an initialized child's HEAD, otherwise retaining the index OID;
index/commit capture uses its own gitlink OID without requiring child objects or a
checkout. Dirty, staged and untracked child files do not affect the parent digest.
No child content or inherited configuration is captured. Analyze that repository
explicitly when needed; a config reference across a gitlink remains a boundary gap.

Digest v2 starts with the bytes `repocli-snapshot-v2\0`. This domain separator identifies
the digest protocol, independently of the project or package name. It hashes sorted,
length-framed regular-file records, then typed symlink, gitlink and large_file records.
Files larger than 2 MiB are streamed into a size/content hash. `fileCount` counts
regular files in this repository, including streamed large files, and excludes links.
The domain prefix intentionally invalidates v1 validation stamps, including in
repositories without gitlinks. Keep Go CLI and native consumers on this same contract;
a digest has the same `sha256:` textual shape, but old values must not be reused.

Mutable inputs are read twice; differing observations produce `snapshot_changed`.
The shared reader limits each repository to 10,000 candidate files and 128 MiB of
in-memory regular-file content by default. Diff callers can explicitly adjust
these with `MaxFiles` and `MaxSnapshotBytes`; capture fails when a limit is exceeded
rather than publishing a truncated content identity. Streaming honors the command deadline. Known gaps
produce `snapshot_incomplete`; read/Git failures exit 1 without a report. A returned
report, including an incomplete one, exits 0. Project configuration validity does not affect capture; its bytes still contribute to the digest. Invalid CLI usage exits 2.

Compare only compatible digest contracts with `complete: true`; check the intended
checkout and input source separately. Capture completeness does not establish complete
dependencies or authorize reuse of execution results; [validation](validation.md)
owns the input evidence and reuse decision.

## Native workspace captures

Python `snapshot(repository)` and TypeScript `snapshot(repository, options)` return
workspace content identity, completeness and diagnostics using the Go digest contract
above, including Unicode paths, regular files, symlinks, gitlink references and large files.
Changes detected between the two captures produce an incomplete result. These native
APIs currently accept workspace inputs; Go also supports index and commit inputs.

Consumers own workflow gates and report association; RepoKit owns Makefile validation
selection, execution history and reuse decisions. The CLI supplies the default history
directory; library callers opt in explicitly. A whole-repository digest conservatively binds every
captured input: a change requires reassessing old evidence. An unchanged diff against
HEAD does not prove equality with the contents from the previous validation.

## Scoped native captures

The Python toolkit also accepts `snapshot(repo, scopes={name: SnapshotScope(...)})`.
Each scope contains literal repository-relative `paths` (files or directory subtrees)
and optional `excludes`. Paths must be normalized; glob expansion is not performed.
The returned `Snapshot.scopes` contains a named digest, file count, completeness and
diagnostics for each selection. All scopes share the same two repository captures;
they do not perform separate filesystem scans. The ordinary repository digest and
CLI schema are unchanged. This additive native API is currently Python-only.

`component_scopes(layout)` projects the existing deepest-owner rules into scopes;
it does not discover components or interpret dependencies. Pass a layout observed
for the same contents, and verify repository identity across layout/impact and capture.
An ancestor scope excludes nested Components. A caller can separately capture shared
files/directories and combine those identities with dependency Component identities.

Scoped digest v1 hashes the normalized selection definition and selected typed
path/content records, so missing paths, membership changes, renames and exclusions
remain distinguishable. Its digest is not interchangeable with repository snapshot v2.
Symlinks whose resolved captured targets fall outside a scope make that scope incomplete;
callers may widen the scope or conservatively use the repository identity. A detected
concurrent edit or repository capture gap makes all scoped observations incomplete.

Scope completeness describes captured bytes, not a command's complete dependencies.
Ignored inputs and external dependency environments require a separate caller-owned
contract; a directory scope alone cannot prove that a Make target reads only that directory.
