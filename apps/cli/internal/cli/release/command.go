// Package release adapts Forge Release operations to CLI input and presentation.
package release

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/compforge/repokit/apps/cli/internal/cli/command"
	"github.com/compforge/repokit/apps/cli/internal/cli/forgeaccess"
	"github.com/compforge/repokit/toolkit/go/errs"
	releases "github.com/compforge/repokit/toolkit/go/forge/release"
	"github.com/spf13/cobra"
)

func NewCommand(opts *command.Options) *cobra.Command {
	root := &cobra.Command{Use: "release", Short: "Read and publish Forge Releases"}
	connection := &forgeaccess.Options{}
	connection.Flags(root)
	for _, operation := range []string{"show", "latest", "create"} {
		var target, title, notesFile string
		cmd := &cobra.Command{Use: operation + " <tag>", Args: cobra.ExactArgs(1)}
		if operation == "latest" {
			cmd.Use, cmd.Args = operation, cobra.NoArgs
		}
		if operation == "create" {
			cmd.Flags().StringVar(&target, "target", "", "explicit remote commit or ref to resolve once")
			cmd.Flags().StringVar(&title, "title", "", "release name (default: tag)")
			cmd.Flags().StringVar(&notesFile, "notes-file", "", "release notes file, or - for stdin")
		}
		cmd.RunE = func(cmd *cobra.Command, args []string) error {
			if err := connection.Validate(opts); err != nil {
				return err
			}
			if len(connection.Targets) > 1 {
				return errors.New("release operations require one --target-repository")
			}
			var notes []byte
			var err error
			if operation == "create" {
				if target == "" || notesFile == "" {
					return errors.New("--target and --notes-file are required")
				}
				if notesFile == "-" {
					notes, err = io.ReadAll(cmd.InOrStdin())
				} else {
					notes, err = os.ReadFile(notesFile)
				}
				if err != nil {
					return errs.Wrap(err, "execution.failed", "release.read_notes", notesFile)
				}
			}
			ctx, cancel := context.WithTimeout(cmd.Context(), opts.Timeout)
			defer cancel()
			access, err := connection.Context(ctx, opts.Repository, opts.Home)
			if err != nil {
				return errs.Wrap(err, "execution.failed", "release.connect", opts.Repository)
			}
			repository := access.Client.Repository(access.Targets[0])
			service := access.Client.Releases()
			var result any
			switch operation {
			case "show":
				result, err = service.Show(ctx, repository, args[0])
			case "latest":
				result, err = service.Latest(ctx, repository)
			case "create":
				result, err = service.Create(ctx, repository, releases.CreateRequest{Tag: args[0], Target: target, Name: title, Notes: string(notes)})
			}
			if opts.JSON {
				if outputErr := command.JSON(cmd.OutOrStdout(), result); outputErr != nil {
					return outputErr
				}
			} else {
				command.Record(cmd.OutOrStdout(), result)
				var value *releases.Release
				var phases []releases.Phase
				switch report := result.(type) {
				case releases.QueryResult:
					value, phases = report.Release, report.Phases
					fmt.Fprintf(cmd.OutOrStdout(), "Release selection: %s\n", report.Selection)
				case releases.PublishResult:
					value, phases = report.Release, report.Phases
					fmt.Fprintf(cmd.OutOrStdout(), "Release %s; confirmed=%t; writeAttempted=%t\n", report.Tag, report.Confirmed, report.WriteAttempted)
				}
				if value != nil {
					fmt.Fprintf(cmd.OutOrStdout(), "%s %s @ %s\n%s\n%s\n", value.Tag, value.Name, value.TargetCommit, value.URL, value.Notes)
				}
				for _, p := range phases {
					fmt.Fprintf(cmd.OutOrStdout(), "%s: %.3fs\n", p.Name, p.DurationSeconds)
				}
			}
			return err
		}
		root.AddCommand(cmd)
	}
	return root
}
