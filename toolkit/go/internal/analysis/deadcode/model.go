// Package deadcode owns repository deadcode analysis, including capture
// orchestration, candidate rules, reachability and before/after comparison.
package deadcode

import (
	shared "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
)

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

// Scope describes candidate selection independently of captured material.
type Scope struct {
	Kind         string                `json:"kind"`
	Files        []string              `json:"files"`
	ChangedFiles []string              `json:"changedFiles"`
	Complete     bool                  `json:"complete"`
	Diagnostics  []analysis.Diagnostic `json:"diagnostics"`
}

type Baseline struct {
	Snapshot    analysis.SnapshotReport `json:"snapshot"`
	Documents   int                     `json:"documents"`
	Diagnostics []shared.Diagnostic     `json:"diagnostics"`
	Roots       []Root                  `json:"roots"`
}

// Report retains independent capture, graph and comparison gaps.
type Report struct {
	SchemaVersion int                     `json:"schemaVersion"`
	Mode          string                  `json:"mode"`
	Scope         Scope                   `json:"scope"`
	Snapshot      analysis.SnapshotReport `json:"snapshot"`
	Documents     int                     `json:"documents"`
	Nodes         []Candidate             `json:"nodes"`
	Roots         []Root                  `json:"roots"`
	Diagnostics   []shared.Diagnostic     `json:"diagnostics"`
	Baseline      *Baseline               `json:"baseline,omitempty"`
}
