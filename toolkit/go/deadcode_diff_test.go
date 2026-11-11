package repokit_test

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
)

// +case:id=deadcode_diff_dependencies,desc=`A changed caller stops using unchanged declarations`,expect=`Follow before/after graph dependencies and distinguish regressions from related historical debt`
func TestDeadcodeDiffDependencies(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", `package main
func main(){ target(); retained(); cycleA(); testedOnly() }
func alreadyLocal(){}
`)
	put(t, root, "helpers.go", `package main
func target(){ leaf() }
func leaf(){}
func cycleA(){cycleB()}
func cycleB(){cycleA()}
func retained(){}
func unrelated(){}
func testedOnly(){}
`)
	put(t, root, "helpers_test.go", "package main\nfunc TestUses(){ testedOnly() }\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "before")
	put(t, root, "main.go", `package main

func main(){retained()}
func alreadyLocal(){}
func introduced(){}
`)
	for _, mode := range []string{"unreferenced", "unreachable"} {
		t.Run(mode, func(t *testing.T) {
			report, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, Base: "HEAD", Mode: mode, ExcludeTests: true, ExcludeEntrypoints: true})
			if err != nil {
				t.Fatal(err)
			}
			got := map[string]string{}
			for _, c := range report.Nodes {
				got[c.Name] = c.Change + ":" + c.Reason
			}
			want := map[string]string{"target": "became_dead:" + mode, "alreadyLocal": "pre_existing:" + mode, "introduced": "introduced:" + mode}
			if mode == "unreachable" {
				want["leaf"] = "became_dead:unreachable"
				want["cycleA"] = "became_dead:unreachable"
				want["cycleB"] = "became_dead:unreachable"
				want["testedOnly"] = "became_test_only:test_only"
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("candidates=%v want=%v diagnostics=%v", got, want, report.Diagnostics)
			}
			if report.Baseline == nil || report.Scope.Kind != "diff" || !reflect.DeepEqual(report.Scope.ChangedFiles, []string{"main.go"}) {
				t.Fatalf("scope=%+v baseline=%v", report.Scope, report.Baseline)
			}
			for _, c := range report.Nodes {
				if c.Name == "target" {
					if c.Previous == nil || c.Location.Path != "helpers.go" || len(c.LostUses) == 0 {
						t.Fatalf("missing cross-file before evidence: %+v", c)
					}
					if c.LostUses[0].Location == nil || c.LostUses[0].Location.Path != "main.go" {
						t.Fatalf("lost caller=%+v", c.LostUses)
					}
				}
			}
		})
	}
}

// +case:id=deadcode_candidate_files,desc=`Selected declarations have callers outside the selected files`,expect=`Whole-graph callers remain evidence and an explicit empty selection stays empty`
func TestDeadcodeSelectedFilesRetainExternalCallers(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", "package main\nfunc main(){target()}\n")
	put(t, root, "helpers.go", "package main\nfunc target(){}\nfunc orphan(){}\n")
	for _, files := range [][]string{{"helpers.go"}, {}} {
		report, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, Files: files})
		if err != nil {
			t.Fatal(err)
		}
		want := 1
		if len(files) == 0 {
			want = 0
		}
		if len(report.Nodes) != want || (want == 1 && report.Nodes[0].Name != "orphan") {
			t.Fatalf("files=%v nodes=%v", files, report.Nodes)
		}
	}
	for _, file := range []string{"../outside.go", "/absolute.go", "missing.go"} {
		if _, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, Files: []string{file}}); err == nil {
			t.Fatalf("accepted %s", file)
		}
	}
}

func TestDeadcodeDiffUsesSelectedSnapshot(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", "package main\nfunc main(){target()}\n")
	put(t, root, "target.go", "package main\nfunc target(){}\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "before")
	put(t, root, "main.go", "package main\nfunc main(){}\n")
	git(t, root, "add", "main.go")
	put(t, root, "main.go", "package main\nfunc main(){target()}\n")
	for _, tc := range []struct {
		staged bool
		want   int
	}{{true, 1}, {false, 0}} {
		r, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root, Staged: tc.staged}, Base: "HEAD", ExcludeEntrypoints: true})
		if err != nil {
			t.Fatal(err)
		}
		if len(r.Nodes) != tc.want {
			t.Fatalf("staged=%t got=%v", tc.staged, r.Nodes)
		}
	}
	// Deleted callers still seed the before graph. No code from disk is mixed
	// into the committed after snapshot.
	git(t, root, "commit", "-qm", "after")
	r, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root, Head: "HEAD"}, Base: "HEAD^", ExcludeEntrypoints: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Nodes) != 1 || r.Nodes[0].Name != "target" {
		t.Fatalf("committed nodes=%v", r.Nodes)
	}
	if err := os.Remove(filepath.Join(root, "main.go")); err != nil {
		t.Fatal(err)
	}
	r, err = repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, Base: "HEAD^"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Nodes) != 1 || r.Nodes[0].Change != "became_dead" {
		t.Fatalf("deleted caller nodes=%v", r.Nodes)
	}
}

func TestDeadcodeReachabilityPreservesInitialization(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", `package main
var registration = register()
func register() int{return 1}
func main(){}
func recur(){recur()}
`)
	r, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, Mode: "unreachable"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Nodes) != 1 || r.Nodes[0].Name != "recur" {
		t.Fatalf("nodes=%v", r.Nodes)
	}
	r, err = repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, ExcludeEntrypoints: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Nodes) != 1 || r.Nodes[0].Name != "registration" || !slices.Contains(r.Nodes[0].Risks, "initialization_effects") {
		t.Fatalf("risks=%v", r.Nodes)
	}
}

func TestDeadcodeReachabilityRequiresBoundary(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", "package lib\nfunc API(){helper()}\nfunc helper(){}\nfunc orphan(){}\n")
	req := repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, Mode: "unreachable"}
	if _, err := repokit.AnalyzeDeadcode(context.Background(), req); err == nil {
		t.Fatal("inferred closed-world library boundary")
	}
	graph, err := repokit.Graph(context.Background(), req.InputRequest, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, node := range graph.Nodes {
		if node.Name == "API" {
			req.Roots = []string{node.ID}
		}
	}
	r, err := repokit.AnalyzeDeadcode(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Nodes) != 1 || r.Nodes[0].Name != "orphan" {
		t.Fatalf("nodes=%v", r.Nodes)
	}
}

func TestDeadcodeDiffRenameAndEmptyComparison(t *testing.T) {
	root := fixture(t)
	put(t, root, "old.go", "package main\nfunc historical(){}\n")
	git(t, root, "add", ".")
	git(t, root, "commit", "-qm", "before")
	git(t, root, "mv", "old.go", "renamed.go")
	r, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root, Staged: true}, Base: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Nodes) != 1 || r.Nodes[0].Name != "historical" || r.Nodes[0].Change != "pre_existing" || r.Nodes[0].Previous.Location.Path != "old.go" {
		t.Fatalf("rename=%+v", r.Nodes)
	}
	r, err = repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root, Head: "HEAD"}, Base: "HEAD"})
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Nodes) != 0 || len(r.Scope.ChangedFiles) != 0 {
		t.Fatalf("empty diff became repository scan: %+v", r)
	}
}

func TestDeadcodeScriptReachability(t *testing.T) {
	for _, tc := range []struct {
		name, path, source string
		explicit           bool
	}{
		{"typescript", "module.ts", "export function api(){ helper(); }\nfunction helper(){}\nfunction orphan(){}\n", false},
		{"python", "module.py", "def api():\n    helper()\ndef helper():\n    pass\ndef orphan():\n    pass\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			git(t, root, "init", "-q")
			put(t, root, tc.path, tc.source)
			req := repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, Mode: "unreachable"}
			if tc.explicit {
				graph, err := repokit.Graph(context.Background(), req.InputRequest, 100)
				if err != nil {
					t.Fatal(err)
				}
				for _, node := range graph.Nodes {
					if node.Name == "api" {
						req.Roots = []string{node.QualifiedName}
					}
				}
			}
			r, err := repokit.AnalyzeDeadcode(context.Background(), req)
			if err != nil {
				t.Fatal(err)
			}
			if len(r.Nodes) != 1 || r.Nodes[0].Name != "orphan" {
				t.Fatalf("nodes=%v diagnostics=%v", r.Nodes, r.Diagnostics)
			}
		})
	}
}

// +case:id=deadcode_explicit_initializer_root,desc=`A caller selects a top-level initializer in a source or test document as the API boundary`,expect=`Preserve its explicit reason and retain production reachability through the initializer`
func TestDeadcodeExplicitInitializationRoots(t *testing.T) {
	for _, file := range []string{"helpers.go", "helpers_test.go"} {
		t.Run(file, func(t *testing.T) {
			root := fixture(t)
			put(t, root, "main.go", "package lib\nfunc register() int{return 1}\nfunc orphan(){}\n")
			put(t, root, file, "package lib\nvar registration = register()\n")
			req := repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, Mode: "unreachable"}
			graph, err := repokit.Graph(context.Background(), req.InputRequest, 100)
			if err != nil {
				t.Fatal(err)
			}
			var id, qualifiedName string
			for _, node := range graph.Nodes {
				if node.Name == "registration" {
					id, qualifiedName = node.ID, node.QualifiedName
				}
			}
			if id == "" || qualifiedName == "" {
				t.Fatal("initializer declaration missing from graph")
			}
			for _, selected := range []string{id, qualifiedName} {
				req.Roots = []string{selected}
				report, err := repokit.AnalyzeDeadcode(context.Background(), req)
				if err != nil {
					t.Fatal(err)
				}
				if len(report.Nodes) != 1 || report.Nodes[0].Name != "orphan" || report.Nodes[0].Reason != "unreachable" {
					t.Fatalf("explicit initializer lost reachability: %+v", report.Nodes)
				}
				found := false
				for _, candidate := range report.Roots {
					if candidate.ID == id {
						found = true
						if candidate.Reason != "explicit" {
							t.Fatalf("root reason was overwritten: %+v", candidate)
						}
					}
				}
				if !found {
					t.Fatal("explicit initializer missing from roots")
				}
			}
		})
	}
}
