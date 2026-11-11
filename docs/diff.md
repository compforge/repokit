# Impact analysis

`impact` turns a comparison into changed source and best-effort affected files, with
optional test recommendations. Repository identity and ownership follow
[repository discovery](repository.md); content identity and capture gaps follow
[Snapshot](snapshot.md).

## Comparison and analysis scope

Comparison pairs before/after versions, file statuses, patches and captured source.
`Diff` provides these facts independently of layout or syntax analysis.
`DiffReport.Inspect` derives layout from those captured versions; `FormUnits` forms
[Fragments and Units](units.md), while `AnalyzeImpact` explicitly requests impact analysis.
An input patch reconstructs the target in memory from the matching base. Deleted
and renamed files retain their respective version's paths, contents and configuration.

- Changeset contains changed Documents present in a given version; changed Symbols
  are query seeds, not a second material collection.
- Testset is independently discovered test candidates. Only its members can appear
  in test recommendations; an empty Testset is valid.
- Workset contains the selected Component sources, tests and referenced test support
  actually admitted to the bounded graph. The complete captured catalog remains
  available for resolution without requiring every catalogued file to be parsed.

These sets belong to RepoKit's impact model, not to the shared CodeGraph model.

## Flow

```text
Git comparison / patch
  → changed files and hunks
  → Components owning diff paths in before / after layouts
  → changed definitions in their before / after source
  → changed-file/symbol query entries and test candidates
  → bounded CodeGraph for each version
  → independent reverse queries and merged evidence
  → affected files, test projection and explanations
  → retain evidenced associations and report missing knowledge separately
```

A dependency edge is an import/reference estimate, not proof of a runtime call.
The graph retains intermediate dependent source files in explanation paths;
`sourceFiles` remains the directly changed source list.

Git supplies the base snapshot. The target is the selected working tree, index or
commit, or the result of applying an input patch to the base in memory. Patch parsing
extracts actual changed line runs, excluding unchanged context. Syntax facts map
those ranges to declarations in each snapshot.

When a change is contained in named declarations, named imports can distinguish
the changed symbols from unrelated symbols in the same module. Importing a
changed symbol suffices; the command does not check whether it is called or read.
Module-level changes and declaration gaps overlapping a change use file granularity.
Whole-module imports retain a module seed alongside declaration seeds. Added, renamed,
or changed test files also use file seeds. Each seed reports its granularity and basis. Go uses
package granularity because its imports do not name individual declarations.

Reverse queries use each version's changed
symbols or files. Deleted imports retain their old evidence. Once a dependent file is reached,
transitive propagation uses file granularity. Queries prefer stronger evidence, then shorter dependency distance, and terminate
across cycles. Path confidence is the weakest edge tier: exact, strong, then weak
(scoped maps to strong; name_only and heuristic map to weak). Ownership, package membership and export alias
steps have zero distance; imports, calls and config inheritance count as one.
The relation count and stable node ordering break remaining ties. These are ranking
signals, not probabilities; raw relation confidence, repository basis and source Evidence remain unchanged.

`impact` selects production Components from diff ownership and discovers a Testset
independently. The complete captured catalog remains available for import, module and
configuration resolution. A source admission set controls which files can actually
be parsed: selected Component sources, candidate tests, and test support files.
A Workset is the admitted material actually reached by bounded expansion, not the catalog.

Without test directories, supported sources in the selected Components are roots,
along with discovered tests. Explicit test directories narrow roots to changed files
and candidate tests; intermediate production dependencies stay in the selected Components.
One shared [CodeGraph](codegraph.md) per version preserves relationships between all
selected Components and their tests. Before/after edges never mix.
Known references to excluded production sources remain visible as `source_scope`
observations with `disposition: out_of_scope`; their definitions are not parsed.

`affectedFiles` includes reached existing files plus directly changed existing files;
`testFiles` projects those routes onto the Testset, including default discovery.
A scoped graph does not promise repository-wide dependency closure.

## Test discovery

Test filenames use language conventions, including Go `_test.go`, Python `test_*.py`
and `*_test.py`, and supported JavaScript/TypeScript test/spec names. Directory
membership alone does not make every source file a test.

- Default discovery includes tests owned by selected Components, colocated root tests,
  and repository-level `tests/`, `test/`, and `__tests__/` directories.
- Repeated `--test-dir` arguments replace default discovery with candidates under those
  directories. They may select tests outside the changed Components.
- Helper sources inside declared or conventional test directories are admitted only
  when referenced. `--test-dir .` searches for tests across the repository; it does not
  classify every repository source as a helper or select more production Components.
- Unselected production Components are not expanded through test imports. A test's
  filesystem owner does not identify the Component it tests.
- No matching tests is a valid empty Testset, including an empty requested directory.
  Empty discovery and zero evidenced associations do not produce `no_candidates` gaps.

These are static filename/directory conventions, not execution of a project's test
runner. Unsupported custom locations can be supplied with `--test-dir`; dynamic
relationships remain subject to the normal evidence limits.

## Best-effort recommendations and execution gaps

Test selection traverses known-target edges, including inferred imports and calls.
Each explanation retains confidence and basis so callers can distinguish static evidence
from inference. Unknown dependency targets are omitted. The analyzer does not propagate
hypothetical missing relations or prove that unselected tests are independent.

Local CodeGraph extraction gaps remain observations with their snapshot, subject,
location and outline counters. They do not make every candidate partial. Declaration
gaps overlapping changed lines prevent narrow symbol seeds; unrelated gaps do not
change seed selection. Unavailable input, relevant unsupported configuration and
exhausted workset budgets can make `scope: partial` / `complete: false`.
A focused report does not promise dependency coverage. Known paths remain useful.
Configuration changes and resources without modeled references retain their component attribution and analysis gaps. Static resource references contribute evidenced dependency paths; unresolved paths remain explicit observations. Fatal input or snapshot failures return a nonzero exit.

Test discovery follows documented filename conventions, not the project's test
runner configuration. The report describes import-based associations, not a
complete runtime test inventory or coverage proof.

Document tags describe overlapping file classifications, not validation importance or exemptions.
Documentation and VERSION changes retain any unmodeled resource impact; descriptive
`package.json` edits retain configuration-change gaps. Known dependency paths are still queried
and reported alongside these limits. Repocli does not decide that a change is safe to skip.
Callers combine tags, evidence and specific diagnostics to choose validation: `complete: false`
is not an instruction to run a full suite, and `complete: true` is not permission to skip one.

## Repository boundary

Within the [snapshot repository boundary](snapshot.md), a submodule change remains one gitlink
file change containing a revision. Imports below that root are external dependencies,
so a pointer update does not imply source or module changes in parent consumers.
The parent project owns any integration checks required for a dependency update. Configuration references crossing the gitlink boundary remain explicit resolution gaps;
analysis does not read child source or configuration files.

Local configuration and language-specific resolution follow [CodeGraph](codegraph.md).
Resolution failures become dependency diagnostics; they do not independently
select tests. The supported syntax and configuration forms are listed in
[diff usage](diff-usage.md#coverage-and-limits).

## Component impact

`components` is a list of `ComponentImpact` entries, not a dictionary keyed by name or
root. Each entry combines identity/metadata with its `ImpactSummary`: changed, affected,
source and test paths, per-tag counts, scope, completeness and gap reasons. Entries are
unique by repository identity, component name and root. Current metadata wins for an
unchanged identity; `snapshot: before` retains identities no longer present after the
comparison. Output is sorted by root, snapshot and component name for reproducibility;
position carries no meaning. A renamed component at the same root can have two entries.
Language is metadata, not a new identity.

The report always includes an `unowned` ImpactSummary. It uses the same projection for
paths with no Component owner in the evidence's version. Empty results use `components: []`,
empty file arrays and `{}` counts; they do not invent a root Component. A real root Component
still owns its files normally. Component-local and unowned gaps remain separate; repository
capture gaps affect both.

Use component results for ownership-oriented queries. Top-level file routes and reasons
remain repository-wide because dependency paths can cross owners; the flat source/test
lists remain aggregate query views over the same analysis facts. Do not sum overlapping
tag counts to obtain a file total. Before-side file lists can include deleted paths or old
owners, so they are evidence, not a promise that every path is runnable in the current checkout.
Renamed source paths are attributed using each side's actual path. Test candidates use their
current owner. Grouping does not change graph admission, test discovery or validation policy.

The `components` report retains the component catalog and before-side identities needed for
moves and deletions. Production source scope uses the most specific owner of each
diff path in each snapshot. Multiple selected Components share the same version graph;
imports and test discovery do not add production Components. A selected root Component
excludes nested Components unless they also own changes. Unowned changes select unowned
sources without creating a synthetic root. Testset and support materials are admitted
independently of this production scope.

`affected` marks components with owned changes or dependency evidence within this scope;
`changedFiles` includes non-source changes, while `affectedFiles` includes reachable ordinary
source files as well as tests. Ownership follows the version that supplied the evidence.

`affectedFileCounts` counts distinct affected repository-relative paths per CodeGraph document tag:
owned direct changes plus dependency-reachable files. It combines format classification and
builtin path rules, including `unknown` when neither classifies a document. Counts deduplicate
paths per tag across both snapshots and all dependency routes. Multiple tags overlap, so their
sum is not a total file count. Renames count both old and new paths in their respective owners;
deletions retain before-side tags. A path changing from a gitlink to a regular file can contribute
different tags from each snapshot. Unaffected Components emit `{}`; absent keys mean zero.

Counts summarize known evidence and never remove diagnostics or change graph construction scope.
Consumers must also inspect `complete`; an empty object is not proof of no possible effect.
Older reports without `affectedFileCounts` do not establish zero counts. `DiffRequest.TagRules` customizes
Diff classification only; impact uses builtin classification independently of caller tag rules.
Impact schema version 4 names this field `affectedFileCounts`; schema 3 used `tagCounts`.

`complete` and diagnostic reasons describe analysis gaps independently of `affected`:
`affected: false, complete: false` is unknown, not evidence of no impact. Shared configuration
with no known owner or dependency retains its uncertainty; no synthetic root component is created.
Callers choose validation scope and commands; RepoKit does not run lint or tests.

General `impact` computes affected files and test recommendations in this declared scope.
`--test-dir` changes Testset discovery, not Component selection. Tests retain their actual
file ownership in the component report; a recommended shared test may therefore mark its
owner affected without making that owner's production sources graph roots. Selection for
construction always comes from diff ownership, never from the resulting `affected` flags.
Unselected Components remain in the catalog; completeness is relative to the declared
analysis scope and does not prove runtime independence.
