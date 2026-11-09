package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/compforge/repokit/toolkit/go/worktree"
	"github.com/spf13/cobra"
	"os"
	"path/filepath"
)

func newWorktreeCommand(opts *options) *cobra.Command {
	root := &cobra.Command{Use: "worktree", Short: "Maintain linked worktrees"}
	var dryRun bool
	var count, prepared int
	var connection forgeOptions
	clean := &cobra.Command{Use: "clean", Short: "Remove old worktrees and release old dependency environments", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if err := connection.validate(opts); err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
		defer cancel()
		policy, err := worktree.LoadWorktreePolicy(ctx, opts.repository)
		if err != nil {
			return executionError{err}
		}
		if cmd.Flags().Changed("max-count") {
			policy.MaxCount = count
		}
		if cmd.Flags().Changed("max-prepared") {
			policy.MaxPrepared = prepared
		}
		if err = policy.Validate(); err != nil {
			return err
		}
		stateDir, err := repositoryStateDir(opts.stateDir)
		if err != nil {
			return executionError{err}
		}
		request := worktree.WorktreeCleanOptions{Policy: &policy, DryRun: dryRun, StateDir: stateDir}
		prContext, connectionErr := connection.context(ctx, opts.repository, opts.stateDir)
		if connectionErr == nil {
			request.PullRequests = prContext
		}
		report, err := worktree.CleanWorktrees(ctx, opts.repository, request)
		if err != nil {
			return executionError{err}
		}
		if connectionErr != nil && !errors.Is(connectionErr, errNoForgeOrigin) {
			report.Errors = append(report.Errors, "PR status unavailable: "+connectionErr.Error())
		}
		if opts.json {
			if err = json.NewEncoder(cmd.OutOrStdout()).Encode(report); err != nil {
				return executionError{err}
			}
		} else {
			for _, action := range report.Actions {
				fmt.Fprintf(cmd.OutOrStdout(), "%s: %s %s (%s)\n", action.Status, action.Action, action.Path, action.Reason)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Remaining: %d worktrees\n", report.RemainingCount)
			for _, issue := range report.Errors {
				fmt.Fprintln(cmd.ErrOrStderr(), "warning:", issue)
			}
		}
		if len(report.Errors) > 0 {
			return executionError{errors.New("worktree maintenance incomplete")}
		}
		return nil
	}}
	clean.Flags().BoolVar(&dryRun, "dry-run", false, "show decisions without deleting resources")
	clean.Flags().IntVar(&count, "max-count", 10, "maximum linked worktrees (repository config by default)")
	clean.Flags().IntVar(&prepared, "max-prepared", 3, "maximum prepared worktrees (repository config by default)")
	connection.flags(clean)
	var reconcile bool
	observe := &cobra.Command{Use: "observe", Short: "Record local branches and checkout locations", Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
		if opts.timeout <= 0 {
			return errors.New("--timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.timeout)
		defer cancel()
		stateDir, err := repositoryStateDir(opts.stateDir)
		if err != nil {
			return executionError{err}
		}
		directory, err := worktree.Observe(ctx, opts.repository, stateDir, reconcile)
		if err != nil {
			return executionError{err}
		}
		if opts.json {
			return json.NewEncoder(cmd.OutOrStdout()).Encode(map[string]string{"directory": directory})
		}
		fmt.Fprintln(cmd.OutOrStdout(), directory)
		return nil
	}}
	observe.Flags().BoolVar(&reconcile, "reconcile", false, "retire absent checkout observations without deleting source directories")
	root.AddCommand(observe)
	root.AddCommand(clean)
	return root
}

func repositoryStateDir(explicit string) (string, error) {
	if explicit != "" {
		return explicit, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".repocli"), nil
}
