package git

import (
	"context"
	"errors"
	"fmt"

	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/git/branch"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func newBranchCommand(opts *command.Options) *cobra.Command {
	root := &cobra.Command{Use: "branch", Short: "Create branches while preserving local changes"}
	for _, action := range []string{"create", "status"} {
		var input branch.Options
		cmd := &cobra.Command{Use: action + " <name>", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			input.Name = args[0]
			var report branch.Report
			var err error
			if action == "create" {
				report, err = state.CreateBranch(ctx, opts.Repository, input, opts.Home)
			} else {
				report, err = branch.Status(ctx, opts.Repository, input.Name)
			}
			if opts.JSON {
				if e := command.JSON(cmd.OutOrStdout(), report); e != nil {
					return e
				}
			} else {
				command.Record(cmd.OutOrStdout(), report)
				fmt.Fprintf(cmd.OutOrStdout(), "Branch %s: %s; branchConfirmed=%t changesRestored=%t confirmed=%t\n", report.Branch, report.Phase, report.BranchConfirmed, report.ChangesRestored, report.Confirmed)
				if report.StashOID != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Recovery stash: %s\n", report.StashOID)
				}
				if report.RecoveryPath != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Recovery record: %s\n", report.RecoveryPath)
				}
				for _, p := range report.Conflicts {
					fmt.Fprintf(cmd.OutOrStdout(), "conflict: %s\n", p)
				}
				for _, d := range report.Diagnostics {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", d.Code, d.Message)
				}
				for _, p := range report.Phases {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %.3fs\n", p.Name, p.DurationSeconds)
				}
			}
			return err
		}}
		if action == "create" {
			cmd.Flags().StringVar(&input.Base, "base", "", "explicit base ref or remote branch")
			cmd.Flags().StringVar(&input.Remote, "remote", "", "configured remote whose base branch must be refreshed")
			cmd.Flags().BoolVar(&input.CarryChanges, "carry-changes", false, "preserve staged, unstaged and untracked changes on the new branch")
			_ = cmd.MarkFlagRequired("base")
		}
		root.AddCommand(cmd)
	}
	root.AddCommand(&cobra.Command{Use: "show <name>", Short: "Read current branch facts and clone-scoped creation provenance", Args: cobra.ExactArgs(1), RunE: func(cmd *cobra.Command, args []string) error {
		if opts.Timeout <= 0 {
			return errors.New("--timeout must be positive")
		}
		ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
		defer cancel()
		observed, err := state.LoadBranch(ctx, opts.Repository, args[0], opts.Home, 0)
		if err != nil {
			return err
		}
		creation, err := state.LoadBranchCreation(ctx, opts.Repository, args[0], opts.Home)
		if err != nil {
			return err
		}
		report := struct {
			Observation *state.BranchObservation `json:"observation"`
			Creation    *state.BranchCreation    `json:"creation"`
		}{observed, creation}
		if opts.JSON {
			return command.JSON(cmd.OutOrStdout(), report)
		}
		command.Record(cmd.OutOrStdout(), report)
		if observed == nil {
			fmt.Fprintln(cmd.OutOrStdout(), "Branch not found")
		} else {
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s complete=%t\n", observed.Branch, observed.Head, observed.Complete)
		}
		if creation != nil {
			fmt.Fprintf(cmd.OutOrStdout(), "Created from %s at %s\n", creation.BaseRef, creation.BaseCommit)
		}
		return nil
	}})
	return root
}
