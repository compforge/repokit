package git

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	commitgit "github.com/compforge/repokit/toolkit/go/git/commit"
	"github.com/spf13/cobra"
)

func newCommitCommand(opts *command.Options) *cobra.Command {
	root := &cobra.Command{Use: "commit", Short: "Plan explicit content, create and verify a native Git commit"}
	for _, action := range []string{"plan", "create", "check"} {
		var input commitgit.Options
		var messageFile string
		cmd := &cobra.Command{Use: action, Args: cobra.NoArgs, RunE: func(cmd *cobra.Command, _ []string) error {
			if opts.Timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			if messageFile != "" {
				var b []byte
				var e error
				if messageFile == "-" {
					b, e = io.ReadAll(cmd.InOrStdin())
				} else {
					b, e = os.ReadFile(messageFile)
				}
				if e != nil {
					return e
				}
				input.Message = string(b)
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			var r commitgit.Report
			var e error
			switch action {
			case "plan":
				r, e = commitgit.Plan(ctx, opts.Repository, input.Selection)
			case "create":
				r, e = commitgit.Create(ctx, opts.Repository, input)
			case "check":
				r, e = commitgit.Check(ctx, opts.Repository, input.Intent)
			}
			if opts.JSON {
				if err := command.JSON(cmd.OutOrStdout(), r); err != nil {
					return err
				}
			} else {
				command.Record(cmd.OutOrStdout(), r)
				fmt.Fprintf(cmd.OutOrStdout(), "Commit %s: empty=%t attempted=%t confirmed=%t indexInstalled=%t\n", action, r.Empty, r.Attempted, r.Confirmed, r.IndexInstalled)
				fmt.Fprintf(cmd.OutOrStdout(), "Intent: branch=%s head=%s tree=%s indexFingerprint=%s\n", r.Branch, r.Head, r.Tree, r.IndexFingerprint)
				if r.Commit != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Observed: commit=%s actualTree=%s parents=%v\n", r.Commit, r.ActualTree, r.Parents)
				}
				for _, c := range r.Changes {
					fmt.Fprintf(cmd.OutOrStdout(), "%s -> %s %q\n", c.OldMode, c.NewMode, c.Path)
				}
				if r.RecoveryPath != "" {
					fmt.Fprintf(cmd.OutOrStdout(), "Recovery: %s\n", r.RecoveryPath)
				}
				for _, d := range r.Diagnostics {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %s\n", d.Code, d.Message)
				}
				for _, p := range r.Phases {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %.3fs\n", p.Name, p.DurationSeconds)
				}
			}
			return e
		}}
		if action != "check" {
			cmd.Flags().StringVar(&input.Mode, "mode", "staged", "selection: staged (preserve partial staging), tracked, or files")
			cmd.Flags().StringArrayVar(&input.Paths, "file", nil, "literal checkout-root-relative file/directory (requires --mode files)")
		}
		if action != "plan" {
			cmd.Flags().StringVar(&input.Branch, "branch", "", "planned local branch")
			cmd.Flags().StringVar(&input.Head, "head", "", "planned parent commit, or unborn")
			cmd.Flags().StringVar(&input.Tree, "tree", "", "planned full tree ID")
			for _, flag := range []string{"branch", "head", "tree"} {
				_ = cmd.MarkFlagRequired(flag)
			}
		}
		if action == "create" {
			cmd.Flags().StringVar(&input.IndexFingerprint, "index-fingerprint", "", "planned index fingerprint")
			cmd.Flags().StringVarP(&input.Message, "message", "m", "", "commit message")
			cmd.Flags().StringVarP(&messageFile, "message-file", "F", "", "message file, or - for stdin")
			cmd.MarkFlagsMutuallyExclusive("message", "message-file")
			cmd.MarkFlagsOneRequired("message", "message-file")
			_ = cmd.MarkFlagRequired("index-fingerprint")
		}
		root.AddCommand(cmd)
	}
	return root
}
