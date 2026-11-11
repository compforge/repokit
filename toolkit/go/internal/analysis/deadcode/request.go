package deadcode

import (
	"fmt"

	"github.com/compforge/repokit/toolkit/go/internal/analysis"
	"github.com/compforge/repokit/toolkit/go/internal/diff"
)

// Request selects repository contents and finite graph build budgets.
// Zero budgets retain CodeGraph defaults.
type Request struct {
	analysis.InputRequest
	MaxNodes     int
	MaxRelations int
	// Base compares before/after graphs and reports candidates reached from changed documents.
	Base string
	// Files limits returned candidates, never graph inputs. Nil means all files;
	// an explicitly empty slice selects no files.
	Files []string
	// Mode is unreferenced (default) or unreachable. Roots selects additional
	// graph roots by exact node ID or unique qualified name in unreachable mode.
	Mode  string
	Roots []string
	// Filters affect returned candidates only; all captured documents still enter the graph.
	ExcludeTests       bool
	ExcludeEntrypoints bool
}

func (req Request) Validate() error {
	if req.MaxNodes < 0 || req.MaxRelations < 0 {
		return fmt.Errorf("MaxNodes and MaxRelations must not be negative")
	}
	if req.Head != "" && req.Staged {
		return fmt.Errorf("head and staged are mutually exclusive")
	}
	if req.Mode != "" && req.Mode != "unreferenced" && req.Mode != "unreachable" {
		return fmt.Errorf("mode must be unreferenced or unreachable")
	}
	if len(req.Roots) > 0 && req.Mode != "unreachable" {
		return fmt.Errorf("roots require unreachable mode")
	}
	for _, file := range req.Files {
		if !diff.ValidPath(file) {
			return fmt.Errorf("invalid candidate file %q", file)
		}
	}
	for _, root := range req.Roots {
		if root == "" {
			return fmt.Errorf("root must not be empty")
		}
	}
	return nil
}
