package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/worktree"
	"os"
	"path/filepath"

	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newValidationCommand(opts *options, name string) *cobra.Command {
	var components, files []string
	var base string
	var planOnly, force, showHistory bool
	var concurrency int
	var inputsFile, output, historyDir string
	var changed, checks []string
	command := &cobra.Command{
		Use:   name,
		Short: "Run Makefile-defined Component " + name + " checks",
		Long:  "Run explicit Makefile checks. Without --base, check selected Components in full.\nWith --base, plan from impact evidence. LINT_FILES/TEST_FILES require the documented\nMakefile contract. Missing lint/test targets fall back to the root Component; if absent there too, checks are unavailable.\nUse validate --check format,lint,test to format first and validate the resulting contents. Target output is kept in the report.",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if showHistory && (planOnly || force || len(files) > 0 || base != "" || inputsFile != "" || output != "" || len(checks) > 0 || len(components) > 0) {
				return errors.New("--history cannot be combined with check execution options")
			}
			if opts.timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			if len(changed) > 0 && base == "" {
				return errors.New("--changed-file requires --base")
			}
			if len(files) > 0 && base != "" {
				return errors.New("--file and --base are mutually exclusive")
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
			defer cancel()
			// +spec=Optional history must not prevent planning or executing checks
			if historyDir == "" && !planOnly {
				stateDir, err := repositoryStateDir(opts.stateDir)
				if err != nil {
					if showHistory {
						return executionError{fmt.Errorf("resolve validation history directory: %w; specify --history-dir", err)}
					}
					fmt.Fprintf(cmd.ErrOrStderr(), "repocli: validation history disabled: %v\n", err)
				} else {
					historyDir = filepath.Join(stateDir, "repositories")
				}
			}
			if showHistory {
				checkout, err := repokit.FindCheckout(ctx, opts.repository)
				if err != nil {
					return executionError{err}
				}
				if checkout == "" {
					return executionError{errors.New("repository checkout not found")}
				}
				history, err := repokit.ReadValidationHistory(ctx, historyDir, checkout)
				if err != nil {
					return executionError{err}
				}
				return json.NewEncoder(cmd.OutOrStdout()).Encode(history)
			}
			request := repokit.ValidationRequest{HistoryDir: historyDir, Force: force, Repository: opts.repository, Components: components, Files: files, Concurrency: concurrency, Checks: checks, Base: base, ChangedFiles: changed}
			if inputsFile != "" {
				data, err := os.ReadFile(inputsFile)
				if err != nil {
					return executionError{err}
				}
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&request.Inputs); err != nil {
					return executionError{err}
				}
			}
			if name != "validate" {
				request.Checks = []string{name}
			}
			encoder := json.NewEncoder(cmd.OutOrStdout())
			encoder.SetIndent("", "  ")
			if planOnly {
				plan, err := repokit.PlanValidation(ctx, request)
				if err != nil {
					return executionError{err}
				}
				return encoder.Encode(plan)
			}
			report, err := repokit.Validate(ctx, request)
			if err != nil {
				return executionError{err}
			}
			if stateDir, stateErr := repositoryStateDir(opts.stateDir); stateErr == nil {
				if _, stateErr = worktree.Observe(ctx, opts.repository, stateDir, false); stateErr != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "repocli: repository observation unavailable: %v\n", stateErr)
				}
			}
			if output != "" {
				if err := writeValidationReport(output, report); err != nil {
					return executionError{err}
				}
			}
			if opts.json {
				if err := encoder.Encode(report); err != nil {
					return executionError{err}
				}
			} else {
				for _, group := range append(report.Preparation, report.Components...) {
					for _, check := range group.Checks {
						status := check.Status
						elapsed := check.DurationSeconds
						if check.Reused {
							status = "reused"
							elapsed = check.LookupSeconds
						}
						fmt.Fprintf(cmd.OutOrStdout(), "%s %s: %s (%s, %.2fs) — %s; %s\n", group.Component.Root, check.Check, status, check.Scope, elapsed, check.Reason, check.ReuseReason)
						if check.Stdout != "" {
							fmt.Fprint(cmd.OutOrStdout(), check.Stdout)
						}
						if check.Stderr != "" {
							fmt.Fprint(cmd.ErrOrStderr(), check.Stderr)
						}
						if check.Diagnostic != "" {
							fmt.Fprintln(cmd.ErrOrStderr(), check.Diagnostic)
						}
					}
				}
				for _, diagnostic := range report.Diagnostics {
					fmt.Fprintln(cmd.ErrOrStderr(), diagnostic)
				}
			}
			if (!report.InputsConsistent && len(report.Components) > 0) || !report.Complete || !report.Passed {
				return executionError{errors.New("validation did not establish a complete pass")}
			}
			return nil
		},
	}
	command.Flags().StringSliceVar(&components, "component", nil, "Component root(s); default: all discovered Components")
	command.Flags().StringArrayVar(&files, "file", nil, "repository-relative file candidate (repeatable)")
	command.Flags().StringVar(&base, "base", "", "analyze working-tree impact against this revision")
	command.Flags().BoolVar(&planOnly, "plan", false, "print the plan as JSON without running checks")
	command.Flags().IntVar(&concurrency, "concurrency", 2, "maximum concurrent checks (1–64)")
	command.Flags().StringVar(&inputsFile, "inputs", "", "JSON Component/check input declarations and receipt paths")
	command.Flags().StringVar(&historyDir, "history-dir", "", "validation history root (default ~/.repocli/repositories)")
	command.Flags().BoolVar(&force, "force", false, "execute checks even when successful history matches")
	command.Flags().BoolVar(&showHistory, "history", false, "show retained execution and reuse statistics without running checks")
	command.Flags().StringArrayVar(&changed, "changed-file", nil, "restrict impact seeds to a changed repository-relative path")
	command.Flags().StringVar(&output, "output", "", "atomically save the ValidationReport to this file")
	if name == "validate" {
		command.Flags().StringSliceVar(&checks, "check", nil, "steps to run (format,lint,test); format runs first, default lint,test")
	}
	return command
}

func writeValidationReport(path string, report repokit.ValidationReport) error {
	data, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".validation-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(data); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}
