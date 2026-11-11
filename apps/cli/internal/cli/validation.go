package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/compforge/repokit/toolkit/go"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func newValidationCommand(opts *options, name string) *cobra.Command {
	var components, files []string
	var base string
	var planOnly, force, showHistory bool
	var concurrency int
	var inputsFile, historyDir string
	var changed, checks []string
	command := &cobra.Command{
		Use:         name,
		Annotations: map[string]string{"result-json-flags": "plan,history"},
		Short:       "Run Makefile-defined Component " + name + " checks",
		Long:        "Run explicit Makefile checks. Without --base, check selected Components in full.\nWith --base, plan from impact evidence. LINT_FILES/TEST_FILES require the documented\nMakefile contract. Missing lint/test targets fall back to the root Component; if absent there too, checks are unavailable.\nUse validate --check format,lint,test to format first and validate the resulting contents. Target output is kept in the report.",
		Args:        cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			if showHistory && (planOnly || force || len(files) > 0 || base != "" || inputsFile != "" || len(checks) > 0 || len(components) > 0) {
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
				home, err := repositoryHome(opts.home)
				if err != nil {
					if showHistory {
						return executionFailure(fmt.Errorf("resolve validation history directory: %w; specify --history-dir", err))
					}
					addDiagnostic(cmd.OutOrStdout(), diagnostic{Code: "validation.history_unavailable", Severity: "warning", Phase: "history", Message: err.Error()})
					if !opts.json {
						fmt.Fprintf(cmd.ErrOrStderr(), "repocli: validation history disabled: %v\n", err)
					}
				} else {
					historyDir = filepath.Join(home, "repositories")
				}
			}
			if showHistory {
				checkout, err := repokit.FindCheckout(ctx, opts.repository)
				if err != nil {
					return executionFailure(err)
				}
				if checkout == "" {
					return executionFailure(errors.New("repository checkout not found"))
				}
				history, err := repokit.ReadValidationHistory(ctx, historyDir, checkout)
				if err != nil {
					return executionFailure(err)
				}
				return writeJSON(cmd.OutOrStdout(), history)
			}
			request := repokit.ValidationRequest{HistoryDir: historyDir, Force: force, Repository: opts.repository, Components: components, Files: files, Concurrency: concurrency, Checks: checks, Base: base, ChangedFiles: changed}
			if inputsFile != "" {
				data, err := os.ReadFile(inputsFile)
				if err != nil {
					return executionFailure(err)
				}
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&request.Inputs); err != nil {
					return executionFailure(err)
				}
			}
			if name != "validate" {
				request.Checks = []string{name}
			}
			if planOnly {
				finishPhase := measurePhase(cmd.OutOrStdout(), "planning")
				plan, err := repokit.PlanValidation(ctx, request)
				finishPhase()
				if err != nil {
					return executionFailure(err)
				}
				return writeJSON(cmd.OutOrStdout(), plan)
			}
			finishPhase := measurePhase(cmd.OutOrStdout(), "validation")
			report, err := repokit.Validate(ctx, request)
			finishPhase()
			// Preserve available check evidence even if final capture or bookkeeping fails.
			if report.SchemaVersion != 0 {
				recordResult(cmd.OutOrStdout(), report)
			}
			if err != nil {
				return executionFailure(err)
			}
			finishPhase = measurePhase(cmd.OutOrStdout(), "observation")
			if home, stateErr := repositoryHome(opts.home); stateErr == nil {
				if _, stateErr = state.Observe(ctx, opts.repository, home, false); stateErr != nil {
					addDiagnostic(cmd.OutOrStdout(), diagnostic{Code: "state.observation_unavailable", Severity: "warning", Phase: "observation", Message: stateErr.Error()})
					if !opts.json {
						fmt.Fprintf(cmd.ErrOrStderr(), "repocli: repository observation unavailable: %v\n", stateErr)
					}
				}
			}
			finishPhase()
			if !opts.json {
				if err := writeValidationText(cmd.OutOrStdout(), report); err != nil {
					return executionFailure(err)
				}
			}

			if (!report.InputsConsistent && len(report.Components) > 0) || !report.Complete || !report.Passed {
				return executionFailure(&resultFailure{"validation did not establish a complete pass"})
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
	command.Flags().StringVar(&historyDir, "history-dir", "", "validation history root (default ~/.repokit/repositories)")
	command.Flags().BoolVar(&force, "force", false, "execute checks even when successful history matches")
	command.Flags().BoolVar(&showHistory, "history", false, "show retained execution and reuse statistics without running checks")
	command.Flags().StringArrayVar(&changed, "changed-file", nil, "restrict impact seeds to a changed repository-relative path")
	command.Flags().String("output", "", "atomically save the complete command result to this file")
	if name == "validate" {
		command.Flags().StringSliceVar(&checks, "check", nil, "steps to run (format,lint,test); format runs first, default lint,test")
	}
	return command
}
