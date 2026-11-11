package repokit_test

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go"
)

func TestDeadcodeFullGraphAndSelectedSnapshot(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", "package main\nfunc main() { used() }\nfunc used() {}\nfunc orphan() {}\n")
	git(t, root, "add", "main.go")
	git(t, root, "commit", "-qm", "declarations")
	put(t, root, "caller.go", "package main\nfunc stagedCaller() { orphan() }\n")
	git(t, root, "add", "caller.go")
	put(t, root, "caller.go", "package main\nfunc workingCaller() { orphan() }\n")
	for _, tc := range []struct {
		input string
		req   repokit.InputRequest
		want  []string
	}{
		{"commit", repokit.InputRequest{Repository: root, Head: "HEAD"}, []string{"main", "orphan"}},
		{"index", repokit.InputRequest{Repository: root, Staged: true}, []string{"stagedCaller", "main"}},
		{"working_tree", repokit.InputRequest{Repository: root}, []string{"workingCaller", "main"}},
	} {
		report, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: tc.req})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, node := range report.Nodes {
			names = append(names, node.Name)
		}
		if !reflect.DeepEqual(names, tc.want) {
			t.Fatalf("%s: %v", tc.input, names)
		}
		if report.SchemaVersion != 2 || report.Snapshot.Input != tc.input || !report.Snapshot.Complete || report.Diagnostics == nil {
			t.Fatalf("%+v", report)
		}
	}
	// A caller beyond the viewer's default 2,000-document window must count.
	for i := 0; i < 2001; i++ {
		put(t, root, fmt.Sprintf("padding/%04d.txt", i), "")
	}
	put(t, root, "zz.go", "package main\nvar callback = workingCaller\n")
	report, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}})
	if err != nil {
		t.Fatal(err)
	}
	if report.Documents <= 2000 {
		t.Fatal(report.Documents)
	}
	for _, node := range report.Nodes {
		if node.Name == "workingCaller" {
			t.Fatal("omitted late caller")
		}
	}
}

func TestDeadcodeRetainsGraphDiagnostics(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", "package main\nfunc main() { missing() }\n")
	report, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}})
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Diagnostics) == 0 || !report.Snapshot.Complete {
		t.Fatalf("%+v", report)
	}
	if _, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root, Head: "HEAD", Staged: true}}); err == nil {
		t.Fatal("accepted conflicting inputs")
	}
}

func TestDeadcodeBuildBudgets(t *testing.T) {
	root := fixture(t)
	for _, req := range []repokit.DeadcodeRequest{
		{MaxNodes: -1}, {MaxRelations: -1},
		{InputRequest: repokit.InputRequest{Repository: root}, MaxNodes: 1},
		{InputRequest: repokit.InputRequest{Repository: root}, MaxRelations: 1},
	} {
		if _, err := repokit.AnalyzeDeadcode(context.Background(), req); err == nil {
			t.Fatalf("accepted exhausted/invalid budget: %+v", req)
		}
	}
}

// +spec=Deadcode retains uses of individual names in a shared Go declaration.
func TestDeadcodeMultiNameVariableUses(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", `package main
func main() { _, _ = pair() }
func pair() (string, string) {
 var first, second string
 return first, second
}
func orphan() {}
`)
	report, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}})
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, node := range report.Nodes {
		names = append(names, node.Name)
	}
	if !reflect.DeepEqual(names, []string{"main", "orphan"}) {
		t.Fatalf("unexpected candidates: %v", names)
	}
}

// +spec=Deadcode preserves structured CodeGraph budget errors through its public API.
func TestDeadcodeStructuredBuildBudget(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", "package main\nfunc main() { target(); missing() }\nfunc target() {}\n")
	input := repokit.InputRequest{Repository: root}
	full, err := repokit.Graph(context.Background(), input, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		resource string
		limit    int
		request  repokit.DeadcodeRequest
	}{
		{"MaxNodes", len(full.Nodes) - 1, repokit.DeadcodeRequest{InputRequest: input, MaxNodes: len(full.Nodes) - 1}},
		{"MaxRelations", len(full.Relations) - 1, repokit.DeadcodeRequest{InputRequest: input, MaxRelations: len(full.Relations) - 1}},
	} {
		t.Run(tc.resource, func(t *testing.T) {
			report, err := repokit.AnalyzeDeadcode(context.Background(), tc.request)
			var budget *codegraph.BuildBudgetError
			if !errors.Is(err, codegraph.ErrBuildBudget) || !errors.As(err, &budget) {
				t.Fatalf("lost budget error: %v", err)
			}
			if budget.Stage != "source-use" || budget.Resource != tc.resource || budget.Used != tc.limit || budget.Adding != 1 || budget.Limit != tc.limit {
				t.Fatalf("wrong budget details: %+v", budget)
			}
			if len(report.Nodes) != 0 {
				t.Fatal("published candidates from failed graph")
			}
		})
	}
}

// +spec=Deadcode output filters retain test callers and Go entrypoint uses in the full graph.
func TestDeadcodeCandidateFilters(t *testing.T) {
	root := fixture(t)
	put(t, root, "main.go", `package main
 func main() { fromMain() }
 func init() { fromInit() }
 func fromMain() {}
 func fromInit() {}
 func testedOnly() {}
 func orphan() {}
 `)
	put(t, root, "main_test.go", `package main
 func TestOnly() { testedOnly() }
 func helper() {}
 `)
	put(t, root, "library/library.go", `package library
 func main() {}
 func init() {}
 `)
	documents := 0
	for _, tc := range []struct {
		tests, entrypoints bool
		want               []string
	}{
		{false, false, []string{"main", "init", "main", "init", "orphan", "TestOnly", "helper"}},
		{true, false, []string{"main", "init", "main", "init", "orphan"}},
		{false, true, []string{"main", "orphan", "TestOnly", "helper"}},
		{true, true, []string{"main", "orphan"}},
	} {
		report, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, ExcludeTests: tc.tests, ExcludeEntrypoints: tc.entrypoints})
		if err != nil {
			t.Fatal(err)
		}
		var names []string
		for _, node := range report.Nodes {
			names = append(names, node.Name)
		}
		if !reflect.DeepEqual(names, tc.want) {
			t.Fatalf("tests=%v entrypoints=%v got=%v want=%v", tc.tests, tc.entrypoints, names, tc.want)
		}
		if documents == 0 {
			documents = report.Documents
		}
		if report.Documents != documents {
			t.Fatalf("filters dropped captured documents: %d", report.Documents)
		}
	}
}

func TestDeadcodeExcludesScriptTestAliases(t *testing.T) {
	root := t.TempDir()
	git(t, root, "init", "-q")
	put(t, root, "package.json", "{}")
	for _, ext := range []string{"mts", "cts", "es6", "sjs", "njs"} {
		put(t, root, "tests/value.test."+ext, "function testOnly() {}")
	}
	for _, excluded := range []bool{false, true} {
		report, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: repokit.InputRequest{Repository: root}, ExcludeTests: excluded})
		if err != nil {
			t.Fatal(err)
		}
		want := 5
		if excluded {
			want = 0
		}
		if len(report.Nodes) != want {
			t.Fatalf("excludeTests=%v: %+v", excluded, report.Nodes)
		}
	}
}
