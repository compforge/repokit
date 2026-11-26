// Package project describes source ownership independently of any execution workflow.
// Identity semantics follow quality-harness's Product/Repository/Component model.
package project

import (
	cg "github.com/compforge/codegraph"
	"github.com/compforge/quality-harness/sdks/go/common"
)

// Binding separates a component's stable identity from directory layout and
// many-to-many product membership. Neither belongs inside Product's identity.
type Binding struct {
	common.Component
	Manifests    []string            `json:"manifests,omitempty"`
	Root         string              `json:"root"`
	Products     []common.Product    `json:"products"`
	ToolEvidence map[string][]string `json:"toolEvidence,omitempty"`
}

type Layout struct {
	Repository *common.Repository `json:"repository"`
	Components []Binding          `json:"components"`
}

// ImpactSummary describes owned or unowned files in a repository comparison.
// Counts deduplicate paths per tag across versions; tags can overlap.
// +spec=File lists, counts and gaps share the same version-aware ownership projection.
// +link=component://docs/diff.md
type ImpactSummary struct {
	ChangedSourceFiles []string       `json:"changedSourceFiles"`
	RecommendedTests   []string       `json:"recommendedTests"`
	AffectedFileCounts map[cg.Tag]int `json:"affectedFileCounts"`
	Affected           bool           `json:"affected"`
	ChangedFiles       []string       `json:"changedFiles"`
	AffectedFiles      []string       `json:"affectedFiles"`
	SourceFiles        []string       `json:"sourceFiles"`
	TestFiles          []string       `json:"testFiles"`
	Scope              string         `json:"scope"`
	Complete           bool           `json:"complete"`
	FallbackReasons    []string       `json:"fallbackReasons,omitempty"`
}

// ComponentImpact is one component's comparison result, not an execution instruction.
// Snapshot identifies the retained catalog entry: after wins for unchanged identity;
// before retains a removed or renamed component. File evidence may originate on either side.
// +spec=Components are unique by repository identity, component name and root; list order carries no meaning.
// +link=component://docs/diff.md
type ComponentImpact struct {
	Component    common.Component    `json:"component"`
	Root         string              `json:"root"`
	Snapshot     string              `json:"snapshot"`
	Products     []common.Product    `json:"products"`
	ToolEvidence map[string][]string `json:"toolEvidence,omitempty"`
	ImpactSummary
}
