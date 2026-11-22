package repokit_test

import (
	"context"
	"testing"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go"
)

// +case=Repeated direct calls remain separate. Transitive calls and calls owned by nested functions are excluded.
func TestFindCallees(t *testing.T) {
	root := fixture(t)
	put(t, root, "calls.py", "def leaf(): pass\ndef mid(): leaf()\ndef nested_only(): pass\ndef top():\n    mid()\n    mid()\n    def nested(): nested_only()\n")
	pos := repokit.SymbolPosition{InputRequest: repokit.InputRequest{Repository: root}, Path: "calls.py", Line: 4, Column: 5}
	report, err := repokit.FindCallees(context.Background(), pos)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Callees) != 2 {
		t.Fatalf("unexpected calls: %+v", report.Callees)
	}
	for i, site := range report.Callees {
		if site.Callee.Name != "mid" || site.Call.Location.Line != 5+i || site.Callee.NameLocation.Line != 2 || len(site.Call.Evidence) == 0 {
			t.Fatalf("unexpected site: %+v", site)
		}
	}
}

// +case=Shared ancestors keep both edges. Traversal from a type cannot move up to a parent and then down into a sibling.
func TestTypeHierarchyDirections(t *testing.T) {
	root := fixture(t)
	put(t, root, "types.ts", "interface Root {}\ninterface Left extends Root {}\ninterface Right extends Root {}\ninterface Join extends Left, Right {}\nclass Mid implements Join {}\nclass Leaf extends Mid {}\nclass Sibling implements Left {}\n")
	pos := repokit.SymbolPosition{InputRequest: repokit.InputRequest{Repository: root}, Path: "types.ts", Line: 4, Column: 11}
	for _, d := range []repokit.HierarchyDirection{"", repokit.HierarchyParents, repokit.HierarchyChildren} {
		r, err := repokit.TypeHierarchy(context.Background(), repokit.HierarchyRequest{Position: pos, Direction: d})
		if err != nil {
			t.Fatal(err)
		}
		if d == repokit.HierarchyChildren {
			if len(r.Parents) != 0 {
				t.Fatalf("unexpected parents: %+v", r.Parents)
			}
		} else {
			counts := map[string]int{}
			repeated := 0
			for _, entry := range r.Parents {
				counts[entry.Type.Name]++
				if entry.AlreadyShown {
					repeated++
				}
				if entry.Relation.Kind != cg.Extends || (entry.Type.Name == "Root") != (entry.Depth == 2) {
					t.Fatalf("bad ancestry: %+v", entry)
				}
			}
			if len(r.Parents) != 4 || counts["Left"] != 1 || counts["Right"] != 1 || counts["Root"] != 2 || repeated != 1 {
				t.Fatalf("bad ancestry: %+v", r.Parents)
			}
		}
		if d == repokit.HierarchyParents {
			if len(r.Children) != 0 {
				t.Fatalf("unexpected children: %+v", r.Children)
			}
		} else if len(r.Children) != 2 || r.Children[0].Type.Name != "Mid" || r.Children[0].Relation.Kind != cg.Implements || r.Children[1].Type.Name != "Leaf" || r.Children[1].Depth != 2 {
			t.Fatalf("bad descendants: %+v", r.Children)
		}
	}
}

// +case=A cycle is displayed once in each direction, including the closing edge.
func TestHierarchyCycle(t *testing.T) {
	root := fixture(t)
	put(t, root, "cycle.ts", "interface A extends B {}\ninterface B extends A {}\n")
	r, err := repokit.TypeHierarchy(context.Background(), repokit.HierarchyRequest{Position: repokit.SymbolPosition{InputRequest: repokit.InputRequest{Repository: root}, Path: "cycle.ts", Line: 1, Column: 11}})
	if err != nil {
		t.Fatal(err)
	}
	for _, entries := range [][]repokit.HierarchyEntry{r.Parents, r.Children} {
		if len(entries) != 2 || entries[0].Type.Name != "B" || entries[1].Type.Name != "A" || !entries[1].AlreadyShown {
			t.Fatalf("cycle: %+v", entries)
		}
	}
}
