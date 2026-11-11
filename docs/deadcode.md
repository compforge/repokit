# Deadcode candidates

`repocli deadcode` analyzes usage evidence in complete captured repository graphs.
It can scan the repository, inspect selected documents, or follow a diff's graph
nodes to declarations whose usefulness may have changed. It does not delete code.

## Repository and document scope

```sh
repocli deadcode --head HEAD --exclude-tests --exclude-entrypoints --json
repocli deadcode --file internal/service.go --file internal/worker.go --json
```

`--file` selects exact snapshot-relative candidate paths. Each occurrence is one
literal path, including commas; repeat the flag to select multiple files. An empty
argument is invalid. A caller outside those files still counts. A caller's local graph can supply this file set through the Go
API's `DeadcodeRequest.Files`; the analysis still builds the complete snapshot
graph. Nil `Files` selects the repository; an explicitly empty slice selects no
candidates. Missing or invalid paths fail instead of silently widening scope.

All captured text documents participate as graph material, with the normal source
and tooling admission policy. Tests remain available as usage evidence. There is
no viewer document-count truncation or impact workset selection. Capture and graph
budgets still apply; a failed build produces no candidate report. For larger graphs:

```sh
repocli deadcode --max-nodes 250000 --max-relations 1000000 --json
```

## Review a change

```sh
# HEAD versus working tree, including untracked files
repocli deadcode --base HEAD --exclude-tests --exclude-entrypoints --json
# HEAD versus index
repocli deadcode --base HEAD --staged --json
# Two exact revisions; nothing from the current working tree is mixed in
repocli deadcode --base main --head HEAD --mode unreachable --json
```

`--base` captures both versions once through the same comparison mechanism as
`diff`. Nodes located in changed documents seed dependency traversal in **both**
graphs. Before edges preserve removed calls; after edges preserve newly introduced
dependencies. Candidate declarations can therefore be in unchanged files far from
the edited caller. Their incoming-use evidence is checked against the complete
graph, not just this traversal's selected nodes.

The report includes affected historical candidates for opportunistic cleanup,
but separates them from consequences of this change:

| `change` | Observation |
| --- | --- |
| `became_dead` | An existing declaration newly meets the selected deadcode rule, or loses its remaining test-only reachability. |
| `became_test_only` | In unreachable mode, an existing declaration loses production reachability but retains test reachability. |
| `introduced` | A candidate declaration identity is new in the after graph. |
| `pre_existing` | The affected declaration was already a candidate before this change. |
| `unmatched` | Declaration identity is ambiguous, or missing baseline evidence prevents claiming an introduction. |

Unrelated historical candidates outside the diff dependency traversal do not
appear. `--file` can additionally narrow the returned candidates without narrowing
either graph. An empty diff returns no candidates, even if the repository has
historical deadcode. Deleted documents remain before-graph entrypoints; deleted
declarations are not after-snapshot cleanup candidates.

Matching uses path, language, kind and qualified name rather than byte offsets;
inserting lines does not make old declarations appear new. Captured Git renames
map old paths to new paths. Overloads or other duplicate identities stay unmatched;
arbitrary moves or semantic renames are not guessed.

Each matched candidate carries its `previous` declaration. `lostUses` records
direct before-only dependencies, their caller, relation kind, confidence and
before-snapshot source location. A transitively unreachable declaration may retain
all its immediate callers, so an empty `lostUses` does not contradict
`became_dead`. This is graph-state evidence, not proof of runtime causality.

## Analysis rules

### Unreferenced (default)

Declared code nodes with no incoming usage relation become candidates. Structural
edges (`declares`, `contains`, `encloses`, `in_namespace`, `in_directory`,
`occurs_in`) do not count as uses. Every other incoming relation counts, including
weak evidence and self-references. Documents, directories, packages, modules and
namespaces are not candidates. This mode does not find unused recursive cycles or
whole unreachable call chains.

### Unreachable

```sh
repocli deadcode --mode unreachable --json
repocli deadcode --mode unreachable --root 'qualified.symbol' --json
```

`--root` is repeatable and accepts an exact declaration ID or a unique qualified
name. Unknown and ambiguous roots fail. The graph must identify native entrypoints
or explicit export items, or the caller must supply roots. A Go library with no
native entrypoint requires explicit public API roots; its boundary is not inferred
from capitalization. In diff mode the chosen boundary must be valid in each
version, so qualified names are preferable to snapshot-specific IDs.

The reported root set includes:

- CodeGraph native entrypoints, currently Go `main` and `init`, and explicit export
  items exposed by the language adapter;
- explicitly selected declarations;
- documents' top-level execution and top-level variable initialization, retained
  conservatively because an unread variable can still initialize a registration;
- test documents and their declarations, used for a separate test-reachability walk.

Dependency traversal keeps all evidence strengths. An occurrence's dependency is
reachable through its owning declaration; source-use nodes do not independently
make their targets live. A live type conservatively retains its members, protecting
unmodeled dynamic dispatch and serialization. Package/module membership does not
make every function live. Cycles without a path from a root remain unreachable.

Candidates have `reason=unreachable`, or `reason=test_only` when only the test roots
can reach them. Test-file roots are not production roots unless explicitly selected.
Framework callbacks, public boundaries not represented by explicit export items,
reflection, build configurations and dynamic execution remain limitations. Supply
all relevant roots and review diagnostics before drawing conclusions.

## Filters and risks

`--exclude-tests` omits candidates in test files; `--exclude-entrypoints` omits
CodeGraph native entrypoints. Both default to false and filter the report only.
Test and entrypoint callers still participate in graph construction. In unreachable
mode production declarations used only by tests remain `test_only` candidates even
with `--exclude-tests`.

Each candidate has `risks`, including applicable capture/comparison gaps, graph
binding gaps, unknown external callers, dynamic dispatch and possible initialization
effects. Missing callers may be in other documents, so graph gaps are not assumed
to affect only candidates at the diagnostic's location. `external_callers` means
external callers have not been ruled out; it is not an exported/private language
fact. CodeGraph owns language facts; RepoKit does not add a second parser.

An empty risks list, an empty candidate list, or complete capture does not prove
runtime completeness or safe deletion. Initializer effects, API compatibility and
framework conventions still need review. Before and after diagnostics remain
separate, and a failed baseline build cannot be treated as an empty baseline.

## Report and API

The Go toolkit exposes `AnalyzeDeadcode(ctx, DeadcodeRequest)`. `Base`, `Files`,
`Mode`, `Roots`, `ExcludeTests` and `ExcludeEntrypoints` correspond to CLI flags.

Domain schema 2 contains `mode`, `scope`, `snapshot`, `documents`, `nodes`, `roots`
and `diagnostics`, plus `baseline` for diff analysis. Each `nodes` element retains
CodeGraph declaration fields and adds `reason`, `risks`, `change`, `previous` and
`lostUses`. Empty collections are arrays. Scope completeness describes comparison;
snapshot completeness describes capture; graph diagnostics describe analysis gaps.
The CLI wraps this report in its standard command-result envelope.

The command is read-only and does not execute target code or tests. Candidates do
not cause a nonzero exit status: a returned report exits 0, execution failures exit
1, and invalid arguments exit 2. Inspect coverage and diagnostics independently of
command success.

## Implementation boundary

`toolkit/go/internal/analysis/deadcode` owns the complete deadcode flow: request
validation, capture and graph-build orchestration, candidate rules, reachability,
before/after comparison and report assembly. Graph policy consumes CodeGraph facts;
it does not parse language syntax or execute target code.

The parent `internal/analysis` package supplies shared capture and graph primitives.
`CaptureDiffSnapshots` retains the comparison's exact source versions, and
`CapturedSnapshot.Graph` builds from retained contents. The dependency points from
`analysis/deadcode` to `analysis`; the parent does not import feature packages.
The public Go API remains in `toolkit/go/deadcode.go`, with CLI presentation in
`apps/cli`.
