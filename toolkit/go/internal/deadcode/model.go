// Package deadcode applies unused-code policy to supplied CodeGraph facts.
// It does not capture repositories, build graphs, or execute target code.
package deadcode

import shared "github.com/compforge/codegraph"

// Graph borrows one version's facts for the duration of analysis. CaptureComplete
// describes source capture; Diagnostics separately retain graph-building gaps.
type Graph struct {
	Nodes           []shared.Node
	Relations       []shared.Relation
	Diagnostics     []shared.Diagnostic
	CaptureComplete bool
}

// Options selects analysis rules and candidate filters, never graph material.
type Options struct {
	Mode               string
	Roots              []string
	Files              []string
	ExcludeTests       bool
	ExcludeEntrypoints bool
}

// Comparison supplies baseline facts and diff entrypoints from the same capture.
type Comparison struct {
	Before       Graph
	ChangedFiles []string
	Renames      map[string]string
	Complete     bool
}

type Result struct {
	Candidates  []Candidate
	Roots       []Root
	BeforeRoots []Root
}

// Candidate records an observation, never a safe-deletion verdict.
type Candidate struct {
	shared.Node
	Reason string   `json:"reason"`
	Risks  []string `json:"risks"`
	// Change distinguishes introduced, became_dead, became_test_only, pre_existing,
	// and unmatched candidates in diff reports.
	Change   string       `json:"change,omitempty"`
	Previous *shared.Node `json:"previous,omitempty"`
	// LostUses are direct incoming dependencies present only in the before graph.
	// Transitive loss of reachability need not remove a direct incoming edge.
	LostUses []Use `json:"lostUses"`
}

type Use struct {
	Source     shared.Node         `json:"source"`
	Kind       shared.RelationKind `json:"kind"`
	Location   *shared.Location    `json:"location,omitempty"`
	Confidence shared.Confidence   `json:"confidence"`
}

type Root struct {
	shared.Node
	Reason string `json:"reason"`
}
