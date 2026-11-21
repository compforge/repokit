package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"text/tabwriter"

	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/toolkit/go/state"
	"github.com/spf13/cobra"
)

func newRepoCommand(opts *cmdutil.Options) *cobra.Command {
	repo := &cobra.Command{Use: "repo", Short: "Query locally registered repositories"}
	repo.AddCommand(&cobra.Command{
		Use: "find <query>", Short: "Find local primary checkouts by remote URL, owner, repository name or path fragment",
		Example: "  repocli repo find example\n  repocli repo find https://github.com/owner/example --json",
		Args:    cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if opts.Timeout <= 0 {
				return errors.New("--timeout must be positive")
			}
			if strings.TrimSpace(args[0]) == "" {
				return errors.New("repository query must be nonempty")
			}
			if cmd.Flags().Changed("repo") {
				return errors.New("repo find searches all repositories registered in --home")
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			report, err := state.FindRepositories(ctx, opts.Home, args[0])
			if err != nil {
				return executionFailure(err)
			}
			output := cmd.OutOrStdout()
			if opts.JSON {
				err = cmdutil.JSON(output, report)
			} else {
				cmdutil.Record(output, report)
				w := tabwriter.NewWriter(output, 0, 4, 2, ' ', 0)
				if len(report.Matches) > 0 {
					fmt.Fprintln(w, "REPOSITORY\tLOCAL PATH\tORIGIN")
					for _, m := range report.Matches {
						name := "-"
						if m.Repository != nil {
							name = m.Repository.Forge.Name + "/" + m.Repository.Path
						}
						fmt.Fprintf(w, "%s\t%s\t%s\n", name, m.Path, m.Origin)
					}
				} else {
					fmt.Fprintln(w, "No matching registered primary checkouts.")
				}
				err = w.Flush()
				for _, message := range report.Diagnostics {
					fmt.Fprintln(cmd.ErrOrStderr(), message)
				}
			}
			if err != nil {
				return executionFailure(err)
			}
			if len(report.Diagnostics) > 0 {
				return &resultFailure{Message: "repository search incomplete"}
			}
			return nil
		},
	})
	return repo
}
