package repokit

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/code/navigation"
)

type SymbolPosition = navigation.Position
type CalleeSite = navigation.CalleeSite
type CalleesReport = navigation.CalleesReport
type HierarchyDirection = navigation.Direction
type HierarchyRequest = navigation.HierarchyRequest
type HierarchyEntry = navigation.HierarchyEntry
type HierarchyReport = navigation.HierarchyReport

const (
	HierarchyParents  = navigation.Parents
	HierarchyChildren = navigation.Children
	HierarchyBoth     = navigation.Both
)

// FindCallees returns direct outgoing call sites for a callable declaration.
func FindCallees(ctx context.Context, req SymbolPosition) (CalleesReport, error) {
	report, err := navigation.Callees(ctx, req)
	return report, errs.Wrap(err, "code.callees_failed", "code.callees", req.Path)
}

// TypeHierarchy returns the selected type's ancestors, descendants, or both (default).
func TypeHierarchy(ctx context.Context, req HierarchyRequest) (HierarchyReport, error) {
	report, err := navigation.Hierarchy(ctx, req)
	return report, errs.Wrap(err, "code.hierarchy_failed", "code.hierarchy", req.Path)
}
