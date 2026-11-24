package depot

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	depotdata "github.com/compforge/repokit/toolkit/go/internal/depot"
)

type recipeReceipt struct {
	bin  *Bin
	path string
	data []byte
}

// +spec=Until the installer starts, a failed preparation preserves existing receipts including their generations.
func restoreRecipeReceipts(ctx context.Context, retired []recipeReceipt, cause error) error {
	for _, receipt := range retired {
		if err := writeReceipt(context.WithoutCancel(ctx), receipt.path, receipt.data); err != nil {
			receipt.bin.Status, receipt.bin.Action = "unknown", "failed"
			receipt.bin.Error = &errs.Error{Code: "depot.receipt_failed", Message: err.Error(), Subject: receipt.bin.Path, Uncertain: true}
			cause = errors.Join(receipt.bin.Error, cause)
		}
	}
	return cause
}

// +spec=A Component depot target replaces default installers for its Bins. Execute it once under the resource lease and verify all known Bins before issuing receipts.
// +link=component://docs/depot.md
func prepareRecipe(ctx context.Context, report *Report, recipe *MakeRecipe, options Options) error {
	bins := []*Bin{}
	var receipts []recipeReceipt
	for i := range report.Bins {
		bin := &report.Bins[i]
		if bin.RecipeRoot != recipe.Root {
			continue
		}
		bins = append(bins, bin)
		if bin.Status == "unsupported" {
			return &errs.Error{Code: "depot.unsupported", Message: bin.Reason, Subject: bin.Path}
		}
		name, err := binReceiptPath(ctx, report.Checkout, bin)
		if err != nil {
			err.Subject = bin.Path
			return err
		}
		if bin.Status == "ready" || bin.Status == "stale" {
			data, err := depotdata.ReadReceiptBytes(name)
			if err != nil {
				return errs.Wrap(err, "depot.receipt_failed", "read receipt", bin.Path)
			}
			receipts = append(receipts, recipeReceipt{bin: bin, path: name, data: data})
		}
	}
	for i, receipt := range receipts {
		if err := os.Remove(receipt.path); err != nil {
			cause := &errs.Error{Code: "depot.receipt_failed", Message: err.Error(), Op: "retire receipt", Subject: receipt.bin.Path, Cause: err}
			return restoreRecipeReceipts(ctx, receipts[:i], cause)
		}
	}

	start := time.Now()
	slog.InfoContext(ctx, "Component Depot preparation started", "checkout", report.Checkout, "component", recipe.Root, "target", "depot")
	result := run(ctx, filepath.Join(report.Checkout, filepath.FromSlash(recipe.Root)), recipe.Command, "make")
	measure(report, "install", "", start)
	recipe.Executed, recipe.ExitCode, recipe.Stdout, recipe.Stderr = result.started, result.code, result.stdout, result.stderr
	if result.started {
		for _, bin := range bins {
			if bin.Status != "present" {
				bin.Status = "unknown"
			}
		}
	}
	if result.err != nil {
		cause := &errs.Error{Code: "depot.install_failed", Message: result.err.Error(), Op: "make depot", Subject: recipe.Root, Uncertain: result.uncertain, Cause: result.err}
		slog.WarnContext(ctx, "Component Depot preparation failed", "checkout", report.Checkout, "component", recipe.Root, "target", "depot", "error", result.err)
		if !result.started {
			return restoreRecipeReceipts(ctx, receipts, cause)
		}
		for _, bin := range bins {
			bin.Action = "failed"
		}
		return cause
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
