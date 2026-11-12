// Package cli adapts the repository toolkit to repocli's Cobra commands and command-line output.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	depotcmd "github.com/compforge/repokit/apps/cli/internal/cli/depot"
	gitcmd "github.com/compforge/repokit/apps/cli/internal/cli/git"
	"github.com/compforge/repokit/apps/cli/internal/cli/pr"
	releasecmd "github.com/compforge/repokit/apps/cli/internal/cli/release"
	"github.com/compforge/repokit/apps/cli/internal/upgrade"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

// Version is supplied by the executable through build flags or module build information.
var Version = "dev"

// Execute runs a fresh command tree so flags and streams never leak between calls.
func Execute(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return execute(ctx, newRootCommand(&cmdutil.Options{}), args, stdin, stdout, stderr)
}

// Keep the execution boundary independent of subcommand handlers: new commands,
// actual command handlers and errors raised before RunE share a log lifecycle.
func execute(ctx context.Context, root *cobra.Command, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	// Cobra interprets nil args as os.Args; embedding callers own their input.
	if args == nil {
		args = []string{}
	}
	run := &commandLog{}
	start := func() {
		if run.logger == nil {
			home, _ := root.PersistentFlags().GetString("home")
			*run = *startCommandLog(stderr, args, home)
		}
	}
	// Let Cobra resolve help/version normally. Opening the log only when work starts
	// keeps informational commands usable without a writable home directory.
	logCommandHandlers(root, start)
	defer run.close()
	root.SetArgs(args)
	root.SetIn(stdin)
	output := &resultOutput{Writer: stdout, started: time.Now()}
	jsonRequested := requestedJSON(root, args)
	root.SetOut(output)
	root.SetErr(stderr)
	command, err := root.ExecuteContextC(context.WithValue(ctx, commandLogKey{}, run))
	if output.writeErr != nil {
		err = executionFailure(output.writeErr)
	}
	code := 0
	if err != nil {
		if (command == nil || command.Name() != "stats") && !validationHistoryQuery(command) {
			start()
		}
		code = 2
		var failure *errs.Error
		var reported *resultFailure
		switch {
		case errors.As(err, &failure):
			code = 1
			if failure.Code == "usage.invalid_argument" {
				code = 2
			}
		case errors.As(err, &reported):
			code = 1
		default:
			err = usageFailure(err)
		}
	}
	name := root.CommandPath()
	if command != nil {
		name = command.CommandPath()
	}
	parsedJSON, _ := root.PersistentFlags().GetBool("json")
	if (jsonRequested || parsedJSON) && (output.recorded || err != nil) {
		output.structured = true
	}
	if command != nil && (output.recorded || err != nil) && command.Flags().Lookup("output") != nil {
		output.exportPath, _ = command.Flags().GetString("output")
	}
	code, err = output.finish(name, err, code, stderr)
	if run.logger != nil {
		run.finish(name, code, err)
	}
	return code
}

func newRootCommand(opts *cmdutil.Options) *cobra.Command {
	root := &cobra.Command{
		Use:           "repocli",
		Version:       Version,
		Short:         "Tools for Git repositories",
		SilenceErrors: true,
		SilenceUsage:  true,
	}
	flags := root.PersistentFlags()
	flags.StringVar(&opts.Home, "home", "", "RepoKit user home (REPOKIT_HOME or ~/.repokit)")
	flags.StringVar(&opts.Repository, "repo", ".", "repository directory")
	flags.BoolVar(&opts.JSON, "json", false, "write structured JSON to stdout")
	flags.DurationVar(&opts.Timeout, "timeout", 2*time.Minute, "command deadline")
	_ = root.MarkPersistentFlagDirname("repo")
	root.AddCommand(newStatsCommand(opts), newCleanCommand(opts))
	root.AddCommand(newWorktreeCommand(opts), pr.NewCommand(opts), gitcmd.NewCommand(opts), releasecmd.NewCommand(opts), depotcmd.NewCommand(opts))
	root.AddCommand(newValidationCommand(opts, "format"), newValidationCommand(opts, "validate"), newValidationCommand(opts, "lint"), newValidationCommand(opts, "test"), newDeadcodeCommand(opts), newInspectCommand(opts), newTreeCommand(opts), newViewCommand(opts), newDiffCommand(opts), newImpactCommand(opts), newSnapshotCommand(opts), newVersionCommand(opts, root.Version), newUpgradeCommand(opts, root.Version, upgrade.NewClient()))
	trackRepositoryCommands(root, opts)
	return root
}

func logCommandHandlers(command *cobra.Command, start func()) {
	if command.Annotations["history-query"] == "true" {
		return
	}
	// +why=Statistics must not pollute their input or trigger retention cleanup while observing it
	if (command.Name() == "version" || command.Name() == "stats") && command.Parent() != nil && command.Parent().Parent() == nil {
		return
	}
	if run := command.RunE; run != nil {
		command.RunE = func(cmd *cobra.Command, args []string) error {
			start()
			return run(cmd, args)
		}
	}
	if run := command.Run; run != nil {
		command.Run = func(cmd *cobra.Command, args []string) {
			start()
			run(cmd, args)
		}
	}
	for _, child := range command.Commands() {
		logCommandHandlers(child, start)
	}
}

// Repository commands renew only their selected checkout. Maintenance and statistics never do.
func trackRepositoryCommands(root *cobra.Command, opts *cmdutil.Options) {
	var visit func(*cobra.Command)
	visit = func(cmd *cobra.Command) {
		if cmd.Name() == "clean" || cmd.Annotations["history-query"] == "true" {
			return
		}
		if run := cmd.RunE; run != nil {
			cmd.RunE = func(cmd *cobra.Command, args []string) error {
				ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
				var accessErr error
				history, _ := cmd.Flags().GetBool("history")
				if !history {
					accessErr = state.RecordAccess(ctx, opts.Repository, opts.Home)
				}
				cancel()
				err := run(cmd, args)
				if accessErr != nil && err == nil {
					if output, ok := cmd.OutOrStdout().(*resultOutput); ok {
						output.result.Diagnostics = append(output.result.Diagnostics, diagnostic{Code: "state.access_unavailable", Severity: "warning", Phase: "observation", Subject: opts.Repository, Message: accessErr.Error()})
					}
					if !opts.JSON {
						fmt.Fprintln(cmd.ErrOrStderr(), "warning: access record unavailable:", accessErr)
					}
				}
				return err
			}
		}
		for _, child := range cmd.Commands() {
			visit(child)
		}
	}
	for _, cmd := range root.Commands() {
		switch cmd.Name() {
		case "version", "stats", "upgrade", "clean":
			continue
		}
		visit(cmd)
	}
}
