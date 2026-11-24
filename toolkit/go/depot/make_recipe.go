package depot

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
)

// +spec=A Component depot target replaces default installers for its Bins. Execute it once under the resource lease and verify all known Bins before issuing receipts.
// +link=component://docs/depot.md
func prepareRecipe(ctx context.Context, report *Report, recipe *MakeRecipe, options Options) error {
	bins := []*Bin{}
	for i := range report.Bins {
		bin := &report.Bins[i]
		if bin.RecipeRoot != recipe.Root {
			continue
		}
		bins = append(bins, bin)
		if bin.Status == "unsupported" {
			return &errs.Error{Code: "depot.unsupported", Message: bin.Reason, Subject: bin.Path}
		}
		if err := retireBinReceipt(ctx, report.Checkout, bin); err != nil {
			err.Subject = bin.Path
			return err
		}
		if bin.Status != "present" {
			bin.Status, bin.Action = "unknown", "not_run"
		}
	}

	start := time.Now()
	slog.InfoContext(ctx, "Component Depot preparation started", "checkout", report.Checkout, "component", recipe.Root, "target", "depot")
	result := run(ctx, filepath.Join(report.Checkout, filepath.FromSlash(recipe.Root)), recipe.Command, "make")
	measure(report, "install", "", start)
	recipe.Executed, recipe.ExitCode, recipe.Stdout, recipe.Stderr = result.started, result.code, result.stdout, result.stderr
	if result.err != nil {
		for _, bin := range bins {
			bin.Action = "failed"
		}
		slog.WarnContext(ctx, "Component Depot preparation failed", "checkout", report.Checkout, "component", recipe.Root, "target", "depot", "error", result.err)
		return &errs.Error{Code: "depot.install_failed", Message: result.err.Error(), Op: "make depot", Subject: recipe.Root, Uncertain: result.uncertain, Cause: result.err}
	}

	start = time.Now()
	fresh, err := Inspect(ctx, report.Checkout, options)
	measure(report, "verify", "", start)
	if err != nil {
		return err
	}
	if !samePlan(*report, fresh) {
		return problem("inputs_changed", "project dependency inputs changed during preparation")
	}
	for _, bin := range bins {
		if err := verifyBinOutput(report.Checkout, bin); err != nil {
			bin.Error, bin.Action = err, "failed"
			err.Subject = bin.Path
			return err
		}
	}
	for _, bin := range bins {
		if bin.Status == "present" {
			bin.Action = "preserved"
			continue
		}
		if err := publishBinReceipt(ctx, report, bin); err != nil {
			return errs.Wrap(err, "depot.receipt_failed", "depot.prepare", bin.Path)
		}
	}
	return nil
}
