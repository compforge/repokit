package repokit

import (
	"context"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
	"github.com/compforge/repokit/toolkit/go/internal/units"
)

// Change retains a patch, captured before/after content and path tags.
type Change = units.Change
type Fragment = units.Fragment
type ElementCounts = units.ElementCounts
type Unit = units.Unit
type ElementKind = units.Kind
type SourceElement = units.Element
type SourceSpan = units.Span
type FragmentRelation = units.Relation
type SourceConnection = units.Connection
type UnitOptions = units.Options
type UnitResult = units.Result
type UnitStep = units.Step
type UnitMerge = units.Merge
type UnitDiagnostic = units.Diagnostic
type UnitNamespaceMerge = units.NamespaceMerge
type UnitReport = analysis.UnitReport

// FormUnits forms Fragments and Units from an existing, optionally filtered Diff.
// Exclude applies caller policy before graph construction and splitting.
// Captured source is reused; no Git or workspace reads occur here.
func FormUnits(ctx context.Context, input DiffReport, opts UnitOptions) (UnitReport, error) {
	result, err := analysis.FormUnits(ctx, input, opts)
	return result, errs.Wrap(err, "units.failed", "units.form", "")
}

// SplitChange partitions captured edits using syntax, without constructing a graph.
func SplitChange(ctx context.Context, ch Change) ([]Fragment, error) {
	result, err := units.Split(ctx, ch)
	return result, errs.Wrap(err, "units.split_failed", "units.split", "")
}

// GroupFragments reuses an existing split while retaining shared Fragment identity.
func GroupFragments(ctx context.Context, fragments []Fragment, before, after *cg.Graph, opts UnitOptions) (UnitResult, error) {
	result, err := units.Group(ctx, fragments, before, after, opts)
	return result, errs.Wrap(err, "units.group_failed", "units.group", "")
}

// FragmentID is stable for the same patch and versioned source owners.
func FragmentID(f Fragment) string { return units.FragmentID(f) }

// ValidateFragmentEdits checks exact edit coverage before grouping can share references.
func ValidateFragmentEdits(ch Change, fs []Fragment) error {
	return errs.Wrap(units.ValidateEdits(ch, fs), "units.invalid_edits", "units.validate_edits", "")
}

const (
	ElementUnknown     ElementKind = units.Unknown
	ElementFunction    ElementKind = units.Function
	ElementMethod      ElementKind = units.Method
	ElementClass       ElementKind = units.Class
	ElementStruct      ElementKind = units.Struct
	ElementInterface   ElementKind = units.Interface
	ElementImport      ElementKind = units.Import
	ElementExport      ElementKind = units.Export
	ElementType        ElementKind = units.Type
	ElementEnum        ElementKind = units.Enum
	ElementField       ElementKind = units.Field
	ElementProperty    ElementKind = units.Property
	ElementVariable    ElementKind = units.Variable
	ElementConstant    ElementKind = units.Constant
	ElementModule      ElementKind = units.Module
	ElementNamespace   ElementKind = units.Namespace
	ElementConstructor ElementKind = units.Constructor
	ElementRecord      ElementKind = units.Record
	ElementWhitespace  ElementKind = units.Whitespace
)
