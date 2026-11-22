package repokit_test

import (
	"context"
	"testing"

	"github.com/compforge/repokit/toolkit/go"
)

// +case=Repeated calls keep their source locations. Other receivers and uses as a function value do not become calls.
func TestCallersByDeclaration(t *testing.T) {
	root := fixture(t)
	put(t, root, "service.go", "package main\ntype Store struct{}\nfunc (Store) Close() {}\ntype Other struct{}\nfunc (Other) Close() {}\ntype Closer interface { Close() }\nfunc callback() {}\nvar registered = callback\n")
	put(t, root, "use.go", "package main\nfunc use(s Store, o Other, c Closer) {\n s.Close(); s.Close()\n o.Close()\n c.Close()\n}\n")
	req := repokit.CallersRequest{InputRequest: repokit.InputRequest{Repository: root}, Path: "service.go", Line: 3, Column: 14}
	run := func(line, col int) repokit.CallersReport {
		t.Helper()
		req.Line, req.Column = line, col
		report, err := repokit.FindCallers(context.Background(), req)
		if err != nil {
			t.Fatal(err)
		}
		return report
	}
	r := run(3, 14)
	if r.Target.QualifiedName != "Store.Close" || len(r.Callers) != 2 {
		t.Fatalf("target=%s callers=%+v", r.Target.QualifiedName, r.Callers)
	}
	for i, s := range r.Callers {
		if s.Caller.Name != "use" || s.Call.Location.Path != "use.go" || s.Call.Location.Line != 3 || !s.Call.Confidence.Valid() || len(s.Call.Evidence) == 0 {
			t.Fatalf("unexpected site %d: %+v", i, s)
		}
	}
	if r.Callers[0].Call.Location.Column >= r.Callers[1].Call.Location.Column {
		t.Fatal("lost distinct ordered sites")
	}
	if r := run(6, 25); len(r.Callers) != 1 || r.Callers[0].Call.Location.Line != 5 {
		t.Fatalf("interface callers: %+v", r.Callers)
	}
	if r := run(7, 6); len(r.Callers) != 0 {
		t.Fatal("callback registration counted as a call")
	}
	dead, err := repokit.AnalyzeDeadcode(context.Background(), repokit.DeadcodeRequest{InputRequest: req.InputRequest})
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range dead.Nodes {
		if n.Name == "callback" {
			t.Fatal("callback use lost")
		}
	}
	req.Line, req.Column = 3, 19
	if _, err := repokit.FindCallers(context.Background(), req); err == nil {
		t.Fatal("selected punctuation after the name")
	}
}
