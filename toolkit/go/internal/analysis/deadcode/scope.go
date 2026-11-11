package deadcode

import (
	"context"
	"fmt"
	"slices"

	"github.com/compforge/repokit/toolkit/go/internal/analysis"
)

// +spec=`Diff entry nodes and both graphs use the exact retained comparison snapshots`
func captureDeadcode(ctx context.Context, req Request) (before, after *analysis.CapturedSnapshot, scope Scope, renames map[string]string, err error) {
	scope = Scope{Kind: "repository", Files: slices.Clone(req.Files), ChangedFiles: []string{}, Complete: true, Diagnostics: []analysis.Diagnostic{}}
	if scope.Files == nil {
		scope.Files = []string{}
	}
	if req.Files != nil {
		scope.Kind = "files"
	}
	if req.Base == "" {
		after, err = analysis.Capture(ctx, req.InputRequest)
	} else {
		var comparison analysis.DiffReport
		comparison, before, after, err = analysis.CaptureDiffSnapshots(ctx, analysis.Request{Repository: req.Repository, Base: req.Base, Head: req.Head, Staged: req.Staged})
		if err != nil {
			return
		}
		scope.Kind = "diff"
		scope.Complete = comparison.Complete
		for _, d := range comparison.Diagnostics {
			scope.Diagnostics = append(scope.Diagnostics, analysis.Diagnostic{Code: d.Code, Message: d.Message, Path: d.Path})
		}
		renames = map[string]string{}
		for _, change := range comparison.Changes {
			if change.IsRenamed {
				renames[change.OldPath] = change.NewPath
			}
			// Removed documents are still seeds in the before graph.
			if !change.IsNew {
				scope.ChangedFiles = append(scope.ChangedFiles, change.OldPath)
			}
			if !change.IsDeleted {
				scope.ChangedFiles = append(scope.ChangedFiles, change.NewPath)
			}
		}
		slices.Sort(scope.ChangedFiles)
		scope.ChangedFiles = slices.Compact(scope.ChangedFiles)
	}
	if err != nil {
		return
	}
	for _, file := range req.Files {
		if _, ok := after.Contents.Files[file]; !ok {
			err = fmt.Errorf("candidate file %q is not a captured text document", file)
			return
		}
	}
	return
}
