package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"text/tabwriter"
	"time"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/apps/cli/internal/stats"
	"github.com/spf13/cobra"
)

func newStatsCommand(opts *cmdutil.Options) *cobra.Command {
	var top int
	command := &cobra.Command{
		Use:   "stats",
		Short: "Summarize retained validation, command timings and local storage across repositories",
		Long:  "Summarize existing local records across repositories without running checks or cleaning history.\nCounts cover retained records, not lifetime totals. Storage reports logical file bytes.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.Timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			if top < 1 || top > 100 {
				return errors.New("--top must be between 1 and 100")
			}
			if cmd.Flags().Changed("repo") {
				return errors.New("stats is global; use validate --history --repo <path> for one repository")
			}
			path, err := repositoryHome(opts.Home)
			if err != nil {
				return executionFailure(err)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			report, err := stats.Read(ctx, path, top)
			if err != nil {
				return executionFailure(err)
			}
			if !opts.JSON {
				cmdutil.Record(cmd.OutOrStdout(), report)
			}
			if opts.JSON {
				err = cmdutil.JSON(cmd.OutOrStdout(), report)
			} else {
				err = writeStats(cmd.OutOrStdout(), report)
			}
			if err != nil {
				return executionFailure(err)
			}
			if !report.Complete {
				return executionFailure(&resultFailure{"statistics are incomplete; see diagnostics in the report"})
			}
			return nil
		},
	}
	command.Flags().IntVar(&top, "top", 5, "number of slowest command runs (1–100)")
	return command
}

func writeStats(output io.Writer, report stats.Report) error {
	w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
	fmt.Fprintf(w, "State: %s\nRetained local records; collected %s; complete=%t\n", report.Directory, report.CollectedAt.Format(time.RFC3339), report.Complete)
	fmt.Fprintln(w, "\nStorage (logical file bytes; symlinks are not followed)")
	fmt.Fprintln(w, "DIRECTORY\tFILES\tBYTES\tSYMLINKS\tOLDEST MTIME\tNEWEST MTIME")
	var files int
	var bytes int64
	for _, group := range report.Storage {
		fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%s\t%s\n", group.Name, group.Files, group.Bytes, group.Symlinks, statsTime(group.OldestModifiedAt), statsTime(group.NewestModifiedAt))
		files += group.Files
		bytes += group.Bytes
	}
	fmt.Fprintf(w, "TOTAL\t%d\t%d\n", files, bytes)
	c := report.Validation.Total
	fmt.Fprintf(w, "\nValidation: %d executions, %d passed, %d failed, %d unfinished, %d other, %d reuses\n", c.Executions, c.Passed, c.Failed, c.Unfinished, c.Other, c.Reuses)
	fmt.Fprintf(w, "Pass rate %.1f%% (passed / passed+failed); reuse rate %.1f%% (reuses / executions+reuses)\n", report.Validation.PassRate*100, report.Validation.ReuseRate*100)
	fmt.Fprintf(w, "Execution %.3fs; lookup %.3fs (summed check time, not wall time)\n", c.ExecutionSeconds, c.LookupSeconds)
	fmt.Fprintln(w, "REPOSITORY / COMPONENT CHECK\tEXEC\tPASS\tFAIL\tOPEN\tOTHER\tREUSE\tEXEC(s)\tLOOKUP(s)")
	for _, repo := range report.Validation.Repositories {
		fmt.Fprintf(w, "%s [%s .. %s]\n", repo.Repository, statsTime(repo.From), statsTime(repo.To))
		keys := make([]string, 0, len(repo.Checks))
		for key := range repo.Checks {
			keys = append(keys, key)
		}
		slices.Sort(keys)
		for _, key := range keys {
			c := repo.Checks[key]
			fmt.Fprintf(w, "  %s\t%d\t%d\t%d\t%d\t%d\t%d\t%.3f\t%.3f\n", key, c.Executions, c.Passed, c.Failed, c.Unfinished, c.Other, c.Reuses, c.ExecutionSeconds, c.LookupSeconds)
		}
	}
	fmt.Fprintf(w, "\nCommands [%s .. %s]; %d starts without a retained completion (possibly still running)\n", statsTime(report.Commands.From), statsTime(report.Commands.To), report.Commands.Unfinished)
	fmt.Fprintln(w, "COMMAND\tOK\tFAIL\tTOTAL(s)\tP50(s)\tP95(s)\tMAX(s)")
	for _, command := range report.Commands.ByCommand {
		d := command.Duration
		fmt.Fprintf(w, "%s\t%d\t%d\t%.3f\t%.3f\t%.3f\t%.3f\n", command.Name, command.Succeeded, command.Failed, d.TotalSeconds, d.P50Seconds, d.P95Seconds, d.MaxSeconds)
	}
	fmt.Fprintln(w, "\nSlowest commands\nCOMMAND\tSECONDS\tEXIT\tRUN ID\tTIME")
	for _, run := range report.Commands.Slowest {
		fmt.Fprintf(w, "%s\t%.3f\t%d\t%s\t%s\n", run.Command, run.Seconds, run.ExitCode, run.RunID, statsTime(run.Time))
	}
	a := report.Analysis
	fmt.Fprintf(w, "\nDiff/impact [%s .. %s]: completed=%d failed=%d canceled=%d deadline_exceeded=%d untimed=%d\n", statsTime(a.From), statsTime(a.To), a.Statuses["completed"], a.Statuses["failed"], a.Statuses["canceled"], a.Statuses["deadline_exceeded"], a.Untimed)
	fmt.Fprintln(w, "STAGE / OPERATION\tSAMPLES\tTOTAL(s)\tP50(s)\tP95(s)\tMAX(s)")
	writeDuration(w, "analysis operation", a.Duration)
	for _, stage := range a.Stages {
		writeDuration(w, stage.Name, stage.Duration)
	}
	fmt.Fprintln(w, "Stage intervals may overlap or nest; do not add their totals as wall time.")
	if len(report.Diagnostics) > 0 {
		fmt.Fprintln(w, "\nDiagnostics")
		for _, diagnostic := range report.Diagnostics {
			fmt.Fprintln(w, diagnostic)
		}
	}
	return w.Flush()
}

func writeDuration(w io.Writer, name string, d stats.Duration) {
	fmt.Fprintf(w, "%s\t%d\t%.3f\t%.3f\t%.3f\t%.3f\n", name, d.Samples, d.TotalSeconds, d.P50Seconds, d.P95Seconds, d.MaxSeconds)
}

func statsTime(t time.Time) string {
	if t.IsZero() {
		return "-"
	}
	return t.Format(time.RFC3339)
}
