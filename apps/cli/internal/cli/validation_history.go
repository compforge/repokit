package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"slices"
	"time"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newValidationHistoryCommand(opts *cmdutil.Options) *cobra.Command {
	var directory string
	root := &cobra.Command{Use: "history", Short: "Query retained validation executions and evidence", Annotations: map[string]string{"history-query": "true"}}
	root.PersistentFlags().StringVar(&directory, "history-dir", "", "validation history root (default ~/.repokit/repositories)")
	prepare := func(cmd *cobra.Command, _ []string) error {
		if opts.Timeout <= 0 {
			return errors.New("--timeout must be positive")
		}
		return nil
	}
	historyDir := func() (string, error) {
		if directory != "" {
			return directory, nil
		}
		home, err := repositoryHome(opts.Home)
		if err != nil {
			return "", err
		}
		return filepath.Join(home, "repositories"), nil
	}
	var q repokit.ValidationRunQuery
	list := &cobra.Command{Use: "list", Short: "List actual runs, newest first; reuse does not add a run", Args: cobra.NoArgs, PreRunE: prepare,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			var err error
			q.HistoryDir, err = historyDir()
			if err != nil {
				return executionFailure(err)
			}
			q.Repository = opts.Repository
			finish := measurePhase(cmd.OutOrStdout(), "history.query")
			result, err := repokit.ListValidationRuns(ctx, q)
			finish()
			if err != nil {
				return executionFailure(err)
			}
			if opts.JSON {
				return cmdutil.JSON(cmd.OutOrStdout(), result)
			}
			cmdutil.Record(cmd.OutOrStdout(), result)
			fmt.Fprintf(cmd.OutOrStdout(), "%s: %d of %d retained runs (offset=%d hasMore=%t)\n", result.Repository, len(result.Runs), result.Total, result.Offset, result.HasMore)
			for _, run := range result.Runs {
				writeRunText(cmd.OutOrStdout(), run)
			}
			fmt.Fprintln(cmd.OutOrStdout(), "Historical results only; use history show <runID> for evidence. No checks executed.")
			return nil
		}}
	list.PreRunE = func(cmd *cobra.Command, args []string) error {
		if err := prepare(cmd, args); err != nil {
			return err
		}
		if q.Limit < 1 || q.Limit > 100 || q.Offset < 0 {
			return errors.New("limit must be 1–100 and offset must be nonnegative")
		}
		if q.Check != "" && q.Check != "lint" && q.Check != "test" {
			return errors.New("check must be lint or test")
		}
		if q.Status != "" && !slices.Contains([]string{"passed", "failed", "cancelled", "uncertain", "unfinished"}, q.Status) {
			return errors.New("invalid run status")
		}
		return nil
	}
	list.Flags().StringVar(&q.Checkout, "checkout", "", "exact checkout path; default all repository checkouts, including removed paths")
	list.Flags().StringVar(&q.Component, "component", "", "exact repository-relative Component root")
	list.Flags().StringVar(&q.Check, "check", "", "lint or test")
	list.Flags().StringVar(&q.Status, "status", "", "passed, failed, cancelled, uncertain or unfinished")
	list.Flags().IntVar(&q.Limit, "limit", 20, "maximum returned runs (1–100)")
	list.Flags().IntVar(&q.Offset, "offset", 0, "skip this many matching runs")
	show := &cobra.Command{Use: "show <runID>", Short: "Read a retained run, captured output and input evidence", Args: cobra.ExactArgs(1), PreRunE: prepare,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			directory, err := historyDir()
			if err != nil {
				return executionFailure(err)
			}
			finish := measurePhase(cmd.OutOrStdout(), "history.query")
			result, err := repokit.ReadValidationRun(ctx, directory, opts.Repository, args[0])
			finish()
			if err != nil {
				return executionFailure(err)
			}
			if opts.JSON {
				err = cmdutil.JSON(cmd.OutOrStdout(), result)
			} else {
				cmdutil.Record(cmd.OutOrStdout(), result)
				writeRunDetailText(cmd.OutOrStdout(), result)
			}
			if err != nil {
				return err
			}
			if !result.Complete {
				return executionFailure(&resultFailure{"historical run evidence is incomplete"})
			}
			return nil
		}}
	list.Flags().String("output", "", "atomically save the complete command result")
	show.Flags().String("output", "", "atomically save the complete command result")
	root.AddCommand(list, show)
	return root
}

func writeRunText(w io.Writer, run repokit.ValidationRun) {
	consistent := "unknown"
	if run.InputsConsistent != nil {
		consistent = fmt.Sprint(*run.InputsConsistent)
	}
	fmt.Fprintf(w, "%s %s %s %s: %s scope=%s historicalExecution=%.3fs inputsConsistent=%s reuses=%d\n  checkout=%s\n", run.RunID, run.StartedAt.Format(time.RFC3339Nano), run.Component, run.Check, run.Status, run.Scope, run.DurationSeconds, consistent, run.ReuseCount, run.Checkout)
}

func writeRunDetailText(w io.Writer, detail repokit.ValidationRunDetail) {
	writeRunText(w, detail.Run)
	fmt.Fprintf(w, "Evidence complete=%t inputID=%s\nCommand: %q\nSelected files: %q\n", detail.Complete, detail.Run.InputID, detail.Result.Command, detail.Result.Files)
	if detail.Result.Diagnostic != "" {
		fmt.Fprintln(w, "Check diagnostic:", detail.Result.Diagnostic)
	}
	if detail.Inputs != nil {
		fmt.Fprintf(w, "Input snapshot: %s (%d file digests; --json for manifest)\n", detail.Inputs.Snapshot, len(detail.Inputs.Files))
	}
	for _, issue := range detail.Diagnostics {
		fmt.Fprintf(w, "[warning] %s: %s\n", issue.Artifact, issue.Message)
	}
	writeTestTimings(w, detail.Result)
	for _, stream := range []struct{ name, value string }{{"stdout", detail.Result.Stdout}, {"stderr", detail.Result.Stderr}} {
		if stream.value == "" {
			continue
		}
		excerpt, truncated := logExcerpt(stream.value)
		fmt.Fprintf(w, "%s:\n%s\n", stream.name, excerpt)
		if truncated {
			fmt.Fprintln(w, "[display truncated; --json includes captured output]")
		}
	}
	fmt.Fprintln(w, "Historical evidence does not establish a pass for current contents. No checks executed.")
}

func validationHistoryQuery(cmd *cobra.Command) bool {
	for ; cmd != nil; cmd = cmd.Parent() {
		if cmd.Annotations["history-query"] == "true" {
			return true
		}
	}
	return false
}
