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
	if len(paths) == 0 {
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
		for i := range report.Bins {
			bin := &report.Bins[i]
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
					report.Bins[j].Action = "not_run"
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
		bin.Error = &errs.Error{Code: "depot." + code, Message: message, Uncertain: uncertain, Subject: bin.Path}
	}
	defer func() {
		if bin.Error != nil {
			slog.WarnContext(ctx, "Depot preparation incomplete", "checkout", report.Checkout, "bin", bin.Path, "manager", bin.Manager, "code", bin.Error.Code, "executed", bin.Executed)
		}
	}()
	directory, e := checkedPath(report.Checkout, bin.Path)
	if e != nil {
		fail("unsafe_path", e.Error(), false)
		return
	}
	tracked := command.Run(ctx, report.Checkout, 5*time.Second, "ls-files", "-z", "--", bin.Path)
	if tracked.Err != nil {
		fail("inspection_failed", tracked.Err.Error(), false)
		return
	}
	if tracked.Stdout != "" {
		fail("tracked_resource", "dependency Bin contains tracked files", false)
		return
	}
	// Retire old success evidence before invoking an installer that can partially mutate its directory.
	receiptPath := filepath.Join(directory, receiptName)
	if e = os.Remove(receiptPath); e != nil && !errors.Is(e, os.ErrNotExist) {
		fail("receipt_failed", e.Error(), false)
		return
	}
	start := time.Now()
	slog.InfoContext(ctx, "Depot installation started", "checkout", report.Checkout, "bin", bin.Path, "manager", bin.Manager)
	result := run(ctx, filepath.Join(report.Checkout, filepath.FromSlash(bin.Root)), bin.Command, bin.Manager)
	measure(report, "install", bin.Path, start)
	bin.Executed, bin.ExitCode, bin.Stdout, bin.Stderr = result.started, result.code, result.stdout, result.stderr
	if result.err != nil {
		fail("install_failed", result.err.Error(), result.uncertain)
		return
	}
	start = time.Now()
	fresh, e := Inspect(ctx, report.Checkout, options)
	measure(report, "verify", bin.Path, start)
	if e != nil {
		fail("verification_failed", e.Error(), true)
		return
	}
	if !samePlan(*report, fresh) {
		fail("inputs_changed", "dependency inputs changed during installation; no receipt written", false)
		return
	}
	if _, e = checkedPath(report.Checkout, bin.Path); e != nil {
		fail("unsafe_path", e.Error(), false)
		return
	}
	info, e := os.Lstat(directory)
	if e != nil || !info.IsDir() {
		fail("missing_output", "installer did not create the dependency directory", false)
		return
	}
	if e = ctx.Err(); e != nil {
		fail("cancelled", e.Error(), true)
		return
	}
	start = time.Now()
	generation := make([]byte, 16)
	if _, e = rand.Read(generation); e != nil {
		fail("receipt_failed", e.Error(), false)
		return
	}
	data, e := json.Marshal(receipt{Manager: bin.Manager, Fingerprint: bin.Fingerprint, Generation: hex.EncodeToString(generation)})
	if e == nil {
		e = writeReceipt(ctx, receiptPath, data)
	}
	measure(report, "receipt", bin.Path, start)
	if e != nil {
		fail("receipt_failed", e.Error(), ctx.Err() != nil)
		return
	}
	bin.Status, bin.Action, bin.Reason = "ready", "prepared", ""
	slog.InfoContext(ctx, "Depot installation ready", "checkout", report.Checkout, "bin", bin.Path, "manager", bin.Manager)
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
