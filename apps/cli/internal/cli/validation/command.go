package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

// +spec=Validation completion publishes check evidence without refreshing repository topology.
// +why=The CLI records checkout access separately; topology consumers own observation freshness.
// +link=component://docs/home.md
func New(opts *cmdutil.Options, name string) *cobra.Command {
	var components, files []string
	var base string
	var planOnly, force, showHistory, full bool
	var concurrency int
	var formatWorkers, lintWorkers, testWorkers, buildWorkers int
	workers := []struct {
		check string
		value *int
	}{{"format", &formatWorkers}, {"lint", &lintWorkers}, {"test", &testWorkers}, {"build", &buildWorkers}}
	var inputsFile, historyDir string
	var changed, checks []string
	command := &cobra.Command{
		Use:   name,
		Short: "Run Makefile-defined Component " + name + " checks",
		Long:  "Run explicit Makefile checks. Validate automatically selects checks from the branch diff and falls back to full checks when scope is uncertain. Use validate --full for unconditional scope.\nStandalone lint, test and format use full scope unless a selection is supplied. File and worker parameters follow the documented\nMakefile contract. Each Component selects its first supported local target; missing targets are skipped without root fallback.\nUse validate --check format,lint,test to format first and validate the resulting contents. Add build to execute each selected Component's literal build target alongside lint and test within the concurrency limit. Target output is kept in the report.\nRead the text summary by default; use --json to parse fields.\nTo retain full evidence with a text summary: repocli validate --output result.json.\nDo not also redirect stdout to save the same JSON result again.",
		Args:  cobra.NoArgs,
		PreRunE: func(cmd *cobra.Command, _ []string) error {
			for _, worker := range workers {
				flag := worker.check + "-workers"
				if cmd.Flags().Changed(flag) && *worker.value <= 0 {
					return fmt.Errorf("--%s must be positive", flag)
				}
				if showHistory && cmd.Flags().Changed(flag) {
					return errors.New("--history cannot be combined with check execution options")
				}
			}
			if full && (base != "" || len(files) > 0 || len(changed) > 0) {
				return errors.New("--full cannot be combined with --base, --file or --changed-file")
			}
			if showHistory && (full || planOnly || force || len(files) > 0 || base != "" || inputsFile != "" || len(checks) > 0 || len(components) > 0) {
				return errors.New("--history cannot be combined with check execution options")
			}
			if opts.Timeout <= 0 {
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
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			// +spec=Optional history must not prevent planning or executing checks
			if historyDir == "" && !planOnly {
				home, err := state.ResolveHome(opts.Home)
				if err != nil {
					if showHistory {
						return cmdutil.ExecutionFailure(fmt.Errorf("resolve validation history directory: %w; specify --history-dir", err))
					}
					cmdutil.AddDiagnostic(cmd.OutOrStdout(), cmdutil.Diagnostic{Code: "validation.history_unavailable", Severity: "warning", Phase: "history", Message: err.Error()})
					if !opts.JSON {
						fmt.Fprintf(cmd.ErrOrStderr(), "repocli: validation history disabled: %v\n", err)
					}
				} else {
					historyDir = filepath.Join(home, "repositories")
				}
			}
			if showHistory {
				checkout, err := repokit.FindCheckout(ctx, opts.Repository)
				if err != nil {
					return cmdutil.ExecutionFailure(err)
				}
				if checkout == "" {
					return cmdutil.ExecutionFailure(errors.New("repository checkout not found"))
				}
				history, err := repokit.ReadValidationHistory(ctx, historyDir, checkout)
				if err != nil {
					return cmdutil.ExecutionFailure(err)
				}
				if opts.JSON {
					return cmdutil.JSON(cmd.OutOrStdout(), history)
				}
				cmdutil.Record(cmd.OutOrStdout(), history)
				return writeHistorySummaryText(cmd.OutOrStdout(), history)
			}
			request := repokit.ValidationRequest{Automatic: name == "validate" && !full && base == "" && len(files) == 0, Home: opts.Home, HistoryDir: historyDir, Force: force, Repository: opts.Repository, Components: components, Files: files, Concurrency: concurrency, FormatWorkers: formatWorkers, LintWorkers: lintWorkers, TestWorkers: testWorkers, BuildWorkers: buildWorkers, Checks: checks, Base: base, ChangedFiles: changed}
			if inputsFile != "" {
				data, err := os.ReadFile(inputsFile)
				if err != nil {
					return cmdutil.ExecutionFailure(err)
				}
				decoder := json.NewDecoder(bytes.NewReader(data))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&request.Inputs); err != nil {
					return cmdutil.ExecutionFailure(err)
				}
			}
			if name != "validate" {
				request.Checks = []string{name}
			}
			if planOnly {
				finishPhase := cmdutil.MeasurePhase(cmd.OutOrStdout(), "planning")
				plan, err := repokit.PlanValidation(ctx, request)
				finishPhase()
				recordPhases(cmd.OutOrStdout(), plan.Phases)
				if err != nil {
					return cmdutil.ExecutionFailure(err)
				}
				if opts.JSON {
					return cmdutil.JSON(cmd.OutOrStdout(), plan)
				}
				cmdutil.Record(cmd.OutOrStdout(), plan)
				return writePlanText(cmd.OutOrStdout(), plan)
			}
			finishPhase := cmdutil.MeasurePhase(cmd.OutOrStdout(), "validation")
			report, err := repokit.Validate(ctx, request)
			finishPhase()
			recordPhases(cmd.OutOrStdout(), report.Phases)
			// Preserve available check evidence even if final capture or bookkeeping fails.
			if report.SchemaVersion != 0 {
				cmdutil.Record(cmd.OutOrStdout(), report)
			}
			if err != nil {
				return cmdutil.ExecutionFailure(err)
			}
			if !opts.JSON {
				if err := WriteText(cmd.OutOrStdout(), report); err != nil {
					return cmdutil.ExecutionFailure(err)
				}
			}

			if (!report.InputsConsistent && len(report.Components) > 0) || !report.Complete || !report.Passed {
				return cmdutil.ExecutionFailure(&cmdutil.ResultFailure{Message: "validation did not establish a complete pass"})
			}
			return nil
		},
	}
	command.Flags().StringSliceVar(&components, "component", nil, "Component boundary; default: file owners with --file, otherwise all Components")
	command.Flags().StringArrayVar(&files, "file", nil, "repository-relative file selecting its owning Component (repeatable)")
	command.Flags().StringVar(&base, "base", "", "analyze working-tree impact against this revision")
	command.Flags().BoolVar(&planOnly, "plan", false, "preview the plan without running checks (text by default; --json for parsing)")
	command.Flags().IntVar(&concurrency, "concurrency", 3, "maximum concurrent checks (1–64)")
	for _, worker := range workers {
		if name == "validate" || name == worker.check {
			command.Flags().IntVar(worker.value, worker.check+"-workers", 0, "positive "+strings.ToUpper(worker.check)+"_WORKERS override; omitted uses project default")
		}
	}
	command.Flags().StringVar(&inputsFile, "inputs", "", "JSON Component/check input declarations and receipt paths")
	command.Flags().StringVar(&historyDir, "history-dir", "", "validation history root (default ~/.repokit/repositories)")
	command.Flags().BoolVar(&force, "force", false, "execute checks even when successful history matches")
	command.Flags().BoolVar(&showHistory, "history", false, "show retained execution and reuse statistics without running checks")
	command.Flags().StringArrayVar(&changed, "changed-file", nil, "restrict impact seeds to a changed repository-relative path")
	command.Flags().String("output", "", "save the complete result as JSON; stdout format is unchanged")
	if name == "validate" {
		command.AddCommand(newValidationHistoryCommand(opts))
		command.Flags().BoolVar(&full, "full", false, "check the selected Components in full without impact analysis")
		command.Flags().StringSliceVar(&checks, "check", nil, "steps to run (format,lint,test,build); format first, then lint/test/build share the concurrency limit; default lint,test")
	}
	return command
}
