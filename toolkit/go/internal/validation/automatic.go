package validation

import (
	"context"
	"errors"
	"slices"
	"strings"

	cg "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/git/process"
	"github.com/compforge/repokit/toolkit/go/internal/git/worktree"
	"github.com/compforge/repokit/toolkit/go/internal/project"
	"github.com/compforge/repokit/toolkit/go/state"
)

// +spec=Automatic validation includes branch commits and working-tree edits against a locally known target.
func automaticBase(ctx context.Context, repo, home string) (string, error) {
	info, err := worktree.Local(ctx, repo)
	if err != nil {
		return "", err
	}
	ref, _, err := git.NewReader(ctx, info).AttachedHead(ctx)
	if err != nil {
		return "", err
	}
	branch, attached := strings.CutPrefix(ref, "refs/heads/")
	if !attached {
		return "", errors.New("HEAD is not attached to a local branch")
	}
	creation, err := state.LoadBranchCreation(ctx, info.Root, branch, home)
	if err != nil {
		return "", err
	}
	target := "refs/remotes/origin/HEAD"
	if creation != nil {
		target = creation.BaseCommit
		if creation.Remote != "" {
			target = "refs/remotes/" + creation.Remote + "/" + creation.BaseRef
		}
	}
	return process.MergeBase(ctx, info.Root, target)
}

// Ordinary documentation gaps do not invalidate source/test selection. A resource
// with another role (e.g. a test fixture or configuration file) still needs coverage.
func documentationGaps(report *analysis.Report) map[string]bool {
	gaps := make(map[string]bool)
	for _, d := range report.Diagnostics {
		if d.Code == "impact_uncertain" && d.Reason == "unsupported_resource_change" &&
			slices.Equal(project.FileTags(cg.Document{Path: d.Path}), []cg.Tag{cg.DocumentationTag}) {
			gaps[d.Path+": "+d.Message] = true
		}
	}
	return gaps
}

func firstValidationGap(documentation map[string]bool, groups ...[]string) string {
	for _, reasons := range groups {
		for _, reason := range reasons {
			if !documentation[reason] {
				return reason
			}
		}
	}
	return ""
}
