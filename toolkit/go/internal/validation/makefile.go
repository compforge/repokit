package validation

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	gts "github.com/odvcencio/gotreesitter"
	"github.com/odvcencio/gotreesitter/grammars"
)

type makeContract struct {
	targets   map[string]bool
	variables map[string]bool
	problem   string
}

// Reuse the existing syntax runtime. Parsing never evaluates Make functions,
// includes or conditional expressions, and never launches make for discovery.
// +spec=Make targets come from literal top-level rule syntax, not recipes, comments or define bodies
// +link=component://docs/validation.md
func readMakeContract(ctx context.Context, source []byte) makeContract {
	contract := makeContract{targets: map[string]bool{}, variables: map[string]bool{}}
	if len(source) == 0 {
		return contract
	}
	if len(source) > 2<<20 {
		contract.problem = "Makefile exceeds 2 MiB syntax budget"
		return contract
	}
	language := grammars.MakeLanguage()
	parser := gts.NewParser(language)
	parser.SetTimeoutMicros(2_000_000)
	var cancelled uint32
	parser.SetCancellationFlag(&cancelled)
	stop := context.AfterFunc(ctx, func() { atomic.StoreUint32(&cancelled, 1) })
	defer stop()
	tree, err := parser.ParseStrict(source)
	if err != nil {
		contract.problem = fmt.Sprintf("Makefile syntax unavailable: %v", err)
		return contract
	}
	defer tree.Release()
	root := tree.RootNode()
	if root.HasError() {
		contract.problem = "Makefile contains unsupported or invalid syntax"
		return contract
	}
	for i := 0; i < root.NamedChildCount(); i++ {
		rule := root.NamedChild(i)
		// Bare expression fragments are not Make statements. Some parser recovery
		// paths retain them without setting HasError; they cannot authorize execution.
		// +tmp:text=`Reject recovered top-level fragments`,until=`The parser reports these recovery paths as syntax errors`
		if rule.Type(language) == "word" || rule.Type(language) == "list" {
			return makeContract{targets: map[string]bool{}, variables: map[string]bool{}, problem: "Makefile contains unsupported or invalid syntax"}
		}
		if rule.Type(language) != "rule" {
			continue
		}
		for j := 0; j < rule.NamedChildCount(); j++ {
			node := rule.NamedChild(j)
			if node.Type(language) != "targets" {
				continue
			}
			for _, target := range strings.Fields(strings.ReplaceAll(node.Text(source), "\\\n", " ")) {
				if !strings.ContainsAny(target, "$%\\") {
					contract.targets[target] = true
				}
			}
		}
	}
	var visit func(*gts.Node)
	visit = func(node *gts.Node) {
		if node.Type(language) == "variable_reference" {
			for i := 0; i < node.NamedChildCount(); i++ {
				word := node.NamedChild(i)
				if word.Type(language) == "word" {
					contract.variables[word.Text(source)] = true
				}
			}
		}
		for i := 0; i < node.NamedChildCount(); i++ {
			visit(node.NamedChild(i))
		}
	}
	visit(root)
	return contract
}

// +spec=Format selects the first declared target in format, fmt, fix order; execution failure never selects another alias.
// +link=component://docs/validation.md
func (contract makeContract) target(check string) string {
	candidates := []string{"lint-ci", "lint"}
	if check == "test" {
		candidates = []string{"test", "test-ci", "test-local"}
	}
	if check == "format" {
		candidates = []string{"format", "fmt", "fix"}
	}
	for _, target := range candidates {
		if contract.targets[target] {
			return target
		}
	}
	return ""
}
