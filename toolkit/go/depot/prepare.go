package depot

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/compforge/repokit/toolkit/go/internal/git/command"
	"log/slog"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/state"
)

// Prepare materializes supported locked dependency Bins while preserving user-managed installations.
// +spec=Shared Bins execute once; cleanup and preparation share a cross-process lease. Failed or interrupted installers cannot publish a ready receipt.
// +link=component://docs/depot.md
func Prepare(ctx context.Context, repo string, options Options) (report Report, err error) {
	report, err = Inspect(ctx, repo, options)
	if err != nil {
		return
	}
	paths := []string{}
	for _, bin := range report.Bins {
		paths = append(paths, bin.Path)
	}
	if len(paths) == 0 && len(report.Recipes) == 0 {
		return report, nil
	}
	start := time.Now()
	entered := false
	err = state.WithCheckoutResources(ctx, report.Checkout, paths, func(ctx context.Context) error {
		entered = true
		measure(&report, "lock_wait", "", start)
		fresh, e := Inspect(ctx, report.Checkout, options)
		report.Phases = append(report.Phases, fresh.Phases...)
		if e != nil {
			return e
		}
		if !samePlan(report, fresh) {
			return problem("inputs_changed", "dependency plan changed while waiting; inspect again")
		}
		report.Bins = fresh.Bins
		for i := range report.Recipes {
			recipe := &report.Recipes[i]
			if e := prepareRecipe(ctx, &report, recipe, options); e != nil {
				recipe.Error = errs.Wrap(e, "depot.prepare_failed", "make depot", recipe.Root).(*errs.Error)
				return recipe.Error
			}
		}
		for i := range report.Bins {
			bin := &report.Bins[i]
			if bin.RecipeRoot != "" {
				continue
			}
			if e := ctx.Err(); e != nil {
				bin.Action = "failed"
				bin.Error = errs.Wrap(e, "depot.cancelled", "depot.prepare", bin.Path).(*errs.Error)
				return e
			}
			switch bin.Status {
			case "ready":
				bin.Action = "reused"
				continue
			case "present":
				bin.Action = "preserved"
				continue
			case "unsupported":
				bin.Action = "unsupported"
				bin.Error = problem("unsupported", bin.Reason)
				continue
			}
			if len(bin.Command) == 0 {
				bin.Action = "unsupported"
				bin.Error = problem("unsupported", "no supported locked installer")
				continue
			}
			prepareBin(ctx, &report, i, options)
			if bin.Error != nil {
				if i+1 < len(report.Bins) {
					report.Complete = false
				}
				for j := i + 1; j < len(report.Bins); j++ {
					if report.Bins[j].RecipeRoot == "" {
						report.Bins[j].Action = "not_run"
					}
				}
				break
			}
		}
		return nil
	})
	if !entered {
		measure(&report, "lock_wait", "", start)
	}
	if err != nil {
		report.Complete = false
		return report, errs.Wrap(err, "depot.prepare_failed", "depot.prepare", repo)
	}
	var issues []error
	uncertain := false
	for _, bin := range report.Bins {
		if bin.Error != nil {
			issues = append(issues, bin.Error)
			uncertain = uncertain || bin.Error.Uncertain
		}
	}
	if len(issues) > 0 {
		return report, &errs.Error{Code: "depot.prepare_incomplete", Message: "one or more Bins could not be prepared", Uncertain: uncertain, Cause: errors.Join(issues...), Hint: "Inspect per-Bin status and installer output; preserved user environments are not verified ready."}
	}
	return report, nil
}
func samePlan(a, b Report) bool {
	if len(a.Recipes) != len(b.Recipes) {
		return false
	}
	for i := range a.Recipes {
		if a.Recipes[i].Root != b.Recipes[i].Root || a.Recipes[i].Fingerprint != b.Recipes[i].Fingerprint {
			return false
		}
	}
	if !reflect.DeepEqual(a.Components, b.Components) || len(a.Bins) != len(b.Bins) {
		return false
	}
	for i := range a.Bins {
		if a.Bins[i].Path != b.Bins[i].Path || a.Bins[i].Fingerprint != b.Bins[i].Fingerprint || !reflect.DeepEqual(a.Bins[i].Command, b.Bins[i].Command) {
			return false
		}
	}
	return true
}
func prepareBin(ctx context.Context, report *Report, index int, options Options) {
	bin := &report.Bins[index]
	fail := func(code, message string, uncertain bool) {
		bin.Action = "failed"
		if uncertain {
			bin.Action = "uncertain"
		}
		if bin.Executed {
			bin.Status = "unknown"
		}
		bin.Reason = message
		bin.Error = &errs.Error{Code: code, Message: message, Uncertain: uncertain, Subject: bin.Path}
	}
	defer func() {
		if bin.Error != nil {
			slog.WarnContext(ctx, "Depot preparation incomplete", "checkout", report.Checkout, "bin", bin.Path, "manager", bin.Manager, "code", bin.Error.Code, "executed", bin.Executed)
		}
	}()
	if err := retireBinReceipt(ctx, report.Checkout, bin); err != nil {
		fail(err.Code, err.Message, false)
		return
	}
	start := time.Now()
	slog.InfoContext(ctx, "Depot installation started", "checkout", report.Checkout, "bin", bin.Path, "manager", bin.Manager)
	result := run(ctx, filepath.Join(report.Checkout, filepath.FromSlash(bin.Root)), bin.Command, bin.Manager)
	measure(report, "install", bin.Path, start)
	bin.Executed, bin.ExitCode, bin.Stdout, bin.Stderr = result.started, result.code, result.stdout, result.stderr
	if result.err != nil {
		fail("depot.install_failed", result.err.Error(), result.uncertain)
		return
	}
	start = time.Now()
	fresh, e := Inspect(ctx, report.Checkout, options)
	measure(report, "verify", bin.Path, start)
	if e != nil {
		fail("depot.verification_failed", e.Error(), true)
		return
	}
	if !samePlan(*report, fresh) {
		fail("depot.inputs_changed", "dependency inputs changed during installation; no receipt written", false)
		return
	}
	if e := verifyBinOutput(report.Checkout, bin); e != nil {
		fail(e.Code, e.Message, false)
		return
	}
	if e := publishBinReceipt(ctx, report, bin); e != nil {
		fail("depot.receipt_failed", e.Error(), ctx.Err() != nil)
	}
}

func retireBinReceipt(ctx context.Context, checkout string, bin *Bin) *errs.Error {
	directory, err := checkedPath(checkout, bin.Path)
	if err != nil {
		return problem("unsafe_path", err.Error())
	}
	tracked := command.Run(ctx, checkout, 5*time.Second, "ls-files", "-z", "--", bin.Path)
	if tracked.Err != nil {
		return problem("inspection_failed", tracked.Err.Error())
	}
	if tracked.Stdout != "" {
		return problem("tracked_resource", "dependency Bin contains tracked files")
	}
	if err := os.Remove(filepath.Join(directory, receiptName)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return problem("receipt_failed", err.Error())
	}
	return nil
}

func verifyBinOutput(checkout string, bin *Bin) *errs.Error {
	directory, err := checkedPath(checkout, bin.Path)
	if err != nil {
		return problem("unsafe_path", err.Error())
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() {
		return problem("missing_output", "installer did not create the dependency directory")
	}
	return nil
}

func publishBinReceipt(ctx context.Context, report *Report, bin *Bin) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	start := time.Now()
	generation := make([]byte, 16)
	if _, err := rand.Read(generation); err != nil {
		return err
	}
	data, err := json.Marshal(receipt{Manager: bin.Manager, Fingerprint: bin.Fingerprint, Generation: hex.EncodeToString(generation)})
	if err == nil {
		err = writeReceipt(ctx, filepath.Join(report.Checkout, filepath.FromSlash(bin.Path), receiptName), data)
	}
	measure(report, "receipt", bin.Path, start)
	if err != nil {
		return err
	}
	bin.Status, bin.Action, bin.Reason = "ready", "prepared", ""
	slog.InfoContext(ctx, "Depot installation ready", "checkout", report.Checkout, "bin", bin.Path, "manager", bin.Manager)
	return nil
}
func writeReceipt(ctx context.Context, name string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(name), ".repokit-receipt-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return os.Rename(f.Name(), name)
}
