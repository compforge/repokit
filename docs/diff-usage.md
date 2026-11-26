# Diff and impact

[Project overview](../README.md) · [中文项目介绍](../README.zh-CN.md)

`repocli diff --base main --json` captures what changed: paths, patch, before/after source,
content identities and capture diagnostics. It does not parse code or component configuration.
Use `--head`, `--staged` or `--file` to choose a commit, index or patch postimage.
`--changed-file` selects paths while retaining dependency source in the captured snapshot.

`repocli diff --base main --units --max-units 8` additionally forms related Fragments and Units.
See [Unit formation](units.md) for grouping and size limits. The Go API exposes these as two
composable operations, `Diff` and `FormUnits`; callers may inspect or filter between them.

Since 0.19.0, static impact analysis uses `repocli impact`. Migrate callers that previously
expected affected files, components or test candidates from `repocli diff` to this command.

`repocli impact` describes what changed and which files may be affected. It
reports source files, changed declarations, and import dependency paths. It does
not execute project commands or decide what a caller should do with the result.

## What it reports

- `affectedFiles`: reached existing files in the analyzed workset, plus changed existing
  files. Each entry includes `path`, snapshot `version`, `seed`, path `confidence`,
  dependency `distance`, `dependencyPath` and raw `relations`.
- `seeds`: automatic before/after query entries, with `id`, `path`, `version`,
  `granularity` (symbol, module, file or package) and the selection `basis`.

- `testSelection`: recommended test files, their direct or project-declared matches, and changed source paths with no match.
- `sourceFiles`: changed Go, Python, JavaScript, and TypeScript source paths.
  This includes changed test sources and deleted sources. A rename uses its new
  path; the old path and status remain in `changes`.
- `testFiles`: existing test files under the requested directories that may be
  affected through a known-target dependency path, including inferred edges, or
  are themselves changed. Unknown-target relationships and deleted tests are omitted.
- `changes`: all changed files, their status, changed line ranges, and declarations
  intersecting those ranges in the before/after snapshots. Symbols include
  `qualifiedName` to distinguish owners such as `A.work` and `B.work`.
- `reasons`: dependency paths explaining the affected test files, with typed
  `relations` and source locations where available. `version` identifies the
  before/after snapshot supplying the evidence.
- `fallbackReasons`: retained wire name for reasons the analysis is incomplete;
  RepoKit does not fill the test list or choose an execution fallback.
- `observations`: non-blocking gaps and metadata-only changes. These retain reporting context without creating dependency edges.
- `repository` and `components`: repository identity, component roots and
  languages, declared product memberships, and per-component source/test lists.

For example, if `source.ts` exports `a` and `b`, changing the body of `a` makes the following test reachable in `testFiles`:
a test importing `{ a as alias }`, even if it never calls `alias`. An unused import alone does not qualify it for `testSelection.files`. A test importing
only `{ b }` is not selected by that direct dependency. Namespace, default and
side-effect imports are treated at module granularity. Go imports are treated at
package granularity. Transitive propagation is deliberately broader: once an
importing file is affected, its downstream importers may also be affected.

This is a **static import-based estimate**, not proof that other tests are
unaffected. It follows the static calls supplied by CodeGraph, preserving cross-file
targets and candidate evidence, so a helper change can reach tests importing its caller. It does not
check actual use of imports in tests or infer dynamic method dispatch, general value
references, runtime side effects, reflection, arbitrary resource access, or framework-specific
implicit setup dependencies beyond recognized configuration files (such as
`conftest.py`).

## Quick start

Requires Go 1.26+ to build and Git to read repositories. No Node or Python runtime
is needed for analysis.

```sh
make -C apps/cli build
./bin/repocli impact --repo /path/to/repo --base main --test-dir tests --json
```

Repeat `--test-dir` to select candidate directories explicitly; this replaces default
discovery. `--test-dir .` searches supported test filenames throughout the repository.
Without it, discovery includes selected Components, colocated root tests and shared
`tests/`, `test/`, and `__tests__/` directories. Helpers are expanded on demand within
test directories; searching `.` never admits all production sources as helpers.

Production Components are always selected by diff ownership. The complete catalog
supports reference/configuration lookup, while source admission limits actual parsing.
General impact uses selected Component sources and tests as roots; explicit test directories
use changed files and candidates as roots. `affectedFiles` describes the reached workset,
and `testFiles` its test projection. An empty Testset is valid. Capture and expansion budgets
still apply independently of whether any tests were discovered.

`--max-files` also bounds repository snapshot capture, including non-source files.
Capture stops with an error rather than publishing a truncated content identity.
If source exploration reaches its file budget, known evidence is retained with
`complete: false` and `file_limit` diagnostics. `--max-nodes` (default 250,000)
and `--max-relations` (default 1,000,000) independently bound each source graph;
exhausted graph builds return an
error, not a partially bound graph. Both options accept 0 for their defaults and
reject negative values. `--max-snapshot-bytes` independently controls captured
in-memory source bytes per snapshot (default 128 MiB); it also accepts 0 for the
default and rejects negative values. Go callers use `DiffRequest.MaxFiles`,
`MaxNodes`, `MaxRelations`, and `MaxSnapshotBytes`. Patch postimages obey the same byte limit.

Budget messages include available file counts, configured limits and the current
process-wide Go heap allocation in MiB. This sample includes allocations not yet
garbage-collected; it is not RSS, peak memory, or memory attributable only to this
analysis, and excludes child processes. No forced GC or automatic budget increase
occurs. The caller may retry explicitly, for example:

```sh
repocli impact --base main --max-files 20000 --max-relations 2000000 --json
```

Source-byte, node, evidence and timeout budgets remain independent. Raising file
or relation limits does not disable these other safeguards.

`--base` defaults to `HEAD` and must resolve to a commit. The comparison is that
exact commit versus the current working tree, including staged, unstaged, and
non-ignored untracked files. It does **not** implicitly find a merge base.

You can also supply a Git patch, including via stdin:

```sh
git diff --binary main > /tmp/change.patch
./bin/repocli impact --base main --file /tmp/change.patch --test-dir tests --json

git diff --binary HEAD | ./bin/repocli impact --file - --test-dir tests --json
```

Patch mode reconstructs the postimage from `--base` in memory, independently of
the working tree. The base must match the patch preimage. Use `--binary` when a
patch includes binary changes. This mode does not include untracked files unless
the patch itself contains them.

## Output

The default output is readable text; `--json` writes one JSON object to stdout.
Text groups local gaps by reason and subject, showing counts and up to three examples;
JSON retains every recorded location.
Execution errors go to stderr; analysis diagnostics are included in the result. An abbreviated result looks like:

```json
{
  "schemaVersion": 4,
  "complete": true,
  "diagnostics": [],
  "sourceFiles": ["src/source.ts"],
  "testFiles": ["tests/source.test.ts"],
  "scope": "focused",
  "reasons": [
    {
      "testFile": "tests/source.test.ts",
      "kind": "import",
      "dependencyPath": ["tests/source.test.ts", "symbol:src/source.ts#a"]
    }
  ],
  "fallbackReasons": []
}
```

`scope` describes the requested impact analysis:

| Value | Meaning |
| --- | --- |
| `focused` | Known static paths determined the returned files. |
| `partial` | Known paths are returned; relevant input, workset or configuration gaps remain. |

An empty `testFiles` is only meaningful together with `scope` and
`complete` and `diagnostics`. A partial empty list does not mean no tests were
affected. A successful exit means analysis ran, possibly with gaps; it says
nothing about project correctness. The caller owns any execution fallback. Exit code `1`
indicates analysis/input failure and `2` indicates command-line usage errors.

## Coverage and limits

Test discovery recognizes `_test.go`, `test_*.py`, `*_test.py`, JS/TS
`*.test.*` / `*.spec.*`, and JS/TS sources inside `__tests__`. Custom test filename
conventions are not discovered. Dependencies outside test directories are still
read so indirect imports can be followed.

- JS/TS: relative imports, named imports and aliases, re-exports, plain-string
  `require` and `import()`, and declared external package dependencies. Local JSON
  `tsconfig extends` (including arrays) and explicit workspace `exports`, `main`,
  and `module` source entrypoints are resolved. References across gitlinks remain outside the captured boundary. Captured conditional export alternatives become weak edges. Custom TS aliases,
  package-based `extends`, JSONC config syntax,
  export patterns/arrays and missing generated entrypoints have no resolved target.
  Configuration parsing failures remain diagnostics.
- Python: relative imports, named imports and package initialization. Absolute
  imports resolve against proven search-path prefixes; catalog-only source-root
  matches remain weak inference, even when unique. File-local variables, import
  aliases, small literal loops, and roots built from `Path(__file__)`, `resolve()`,
  `parent` / `parents[n]`, and `/` literal suffixes are recognized. Context follows
  statement order and respects binding shadowing. `sys.path.insert` can establish
  a known prefix; append, conditional roots and deferred function bodies do not
  establish precedence over unknown paths. Known-string `importlib.import_module()`
  / `__import__()` targets are supported. Cwd-relative strings, external roots,
  unsupported path mutations invalidate known precedence. Unknown dynamic targets are
  omitted; unavailable captured roots and evaluation budget exhaustion remain diagnostics.
  Absolute imports with no repository match are treated as external. Import hooks,
  module caches, arbitrary function execution and cross-module side effects are
  outside this static model.
- Go: repository module imports and implicit same-package dependencies, across
  all source files regardless of build tags. Local `replace` directives cause
  diagnostics. Results remain file paths, including for Go tests.

Configuration changes and unsupported resources retain scoped diagnostics. README and `docs/`
Markdown/reStructuredText changes and changes limited to descriptive package fields
are observations; package versions, names, exports, scripts and unknown fields are
not exempt. Local parse, declaration and call-resolution gaps remain observations,
including their snapshot, subject, location and outline counters where available.
Overlapping declaration gaps broaden changed seeds to files; they do not make all
candidates partial. Unresolved targets stay unknown.
These gaps never add tests. Both old and new import graphs participate, preserving resolved
dependencies removed by the diff.

Untracked embedded repositories (including linked worktrees) are outside the parent
repository input; ordinary untracked files remain included. Git-ignored untracked
files are excluded. Symlink, submodule and large-file content
identity follows [snapshot](snapshot.md). A submodule remains one gitlink entry in
`changes`; its sources, components and tests are not recursively included. Parent
imports into a gitlink root depend on that entry as an external package. A gitlink
change can select parent consumers, but never dependency-owned tests. Unchanged
non-source links/assets do not independently create analysis gaps; imports of
unavailable targets, source symlinks and changed resources remain diagnosed.
Files over 2 MiB are hashed without syntax parsing. Capture limits are 10,000 files
or 128 MiB of regular-file contents per repository by default, configurable with
`--max-files` and `--max-snapshot-bytes`; exceeding them fails rather
than silently truncating. The default deadline is
two minutes, configurable with `--timeout`. Syntax facts for identical content
are reused within a run; there is no persistent cache.

See [the analysis design](diff.md) for implementation boundaries.

## Repository structure

`diff` uses the shared [repository preparation](repository.md) for each captured
version, including patch postimages. It reports the entire current component
catalog, even components with empty change lists. Removed components that own
changed source files retain `snapshot: "before"` metadata.

Each entry includes `component` (shared identity and language), `root`, `products`,
and detected `tools`, plus analysis-specific source/test lists, `scope`,
`complete`, and optional `fallbackReasons`. Component roots determine ownership and graph scope. Only Components owning diff paths supply
production source roots. Independent test candidates and their helpers can cross ownership
boundaries without selecting the surrounding production Component. The catalog
still includes unselected Components, without claiming their runtime independence.

## Comparison and automation contract (schema 4)

`--base REF` is an exact commit, never an implicit merge base. The target is the
working tree by default; `--staged` selects the index and `--head REF` selects a
commit. These target modes and `--file` are mutually exclusive. Working-tree
analysis includes non-ignored untracked files. No mode writes to the index.

Repeat `--changed-file PATH` to restrict changed seeds (matching either rename
side). The complete before/after snapshots and dependency graph remain available,
so unchanged consumers in another component can still appear in `testFiles`.

Granularity is automatic; there is no `--impact` option. Edits within known declarations
use symbol seeds plus a whole-module-import seed. Module-level edits, changed tests,
added/renamed files, and changes overlapping declaration gaps use file seeds. Go uses
package propagation. `seed.basis` explains the choice; seed precision is separate from
path confidence.

Path confidence follows the weakest edge: exact, strong, then weak. Native CodeGraph
candidate edges rank as weak while retaining their original value in `relations`.
Stronger paths win before shorter paths. Distance counts imports, calls and config
inheritance; ownership, package membership and export aliases count as zero. Distance
and confidence are ranking signals, not probabilities. Before/after edges are never
combined into one path. Changed existing files have an exact, zero-distance direct
change explanation; deleted files remain seeds, but are absent from `affectedFiles`.

JSON includes `schemaVersion: 4`, resolved `base`/`head`, `input`,
`snapshot` (SHA-256 of observed file contents), `complete`, and `diagnostics`.
Diagnostic codes are `snapshot_incomplete`, `snapshot_changed`, and
`impact_uncertain`; `message` describes the gap and `path` is optional. Impact
diagnostics additionally expose `reason` (such as `missing_config`,
`boundary_unavailable`, `expansion_limit` or `configuration_change`), `relation`,
`version` and `line` where available. Relation `confidence` is omitted for definite
static evidence, `strong` for strong inference, or `weak` for weak inference; `basis`
explains the inference. Native CodeGraph calls retain `exact` / `candidate`, relation ID and full location.
All known-target tiers participate in recommendations; unknown targets create no edge.

`complete` means no blocking input, workset or repository-resolution gaps in the requested
best-effort analysis. Local extraction gaps remain in `observations` without changing it.
It does not guarantee dependency coverage: even a focused, complete report with an
empty test list cannot prove that no tests are affected. Snapshot gaps are reported
even without test directories or changed files. Callers decide whether to run more tests.

Working tree/index contents are compared before and after analysis to detect
concurrent changes. The digest is content identity, **not an atomic snapshot**.
Callers must ensure the checked tree still matches the analyzed input.

Use `repocli --version` for build identity. Tagged releases publish fixed-version
macOS/Linux amd64/arm64 archives and SHA-256 checksums. Consumers should install
once outside validation, keep a pinned version, and check the JSON schema before
using a result. A missing CLI or unsupported schema is an analysis failure, never
evidence that no files were affected.

Digest v2 starts with `repocli-snapshot-v2\0`; for regular files it then hashes paths in lexicographic order.
Each entry contributes UTF-8 byte length, `:`, path bytes, decimal content byte length,
`:`, then content bytes. Prefix the SHA-256 hex digest with `sha256:`. This framing
lets a consumer compare execution input without rerunning dependency analysis.
Incomplete snapshots additionally hash their sorted issue strings and must never
be treated as a complete regular-file identity.

Execution logging is shared by all commands; see [execution logs](logging.md).

Snapshot completeness and dependency-analysis completeness are separate: an internal
symlink or gitlink reference can have a reliable content identity while
parent-repository expansion failures can still yield `impact_uncertain`, and parse failures
remain local observations. Patch reconstruction with symlinks,
submodules or streamed large files in the base is unsupported; use working-tree,
index or commit comparison instead. Imports without a captured target, including runtime built-ins and unknown package
specifiers, create neither edges nor unresolved-package diagnostics.
