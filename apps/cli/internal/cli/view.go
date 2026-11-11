package cli

import (
	"context"
	"fmt"

	"github.com/compforge/repokit/apps/cli/internal/viewer"
	"github.com/compforge/repokit/toolkit/go"
	"github.com/spf13/cobra"
)

func newViewCommand(opts *options) *cobra.Command {
	var addr string
	var maxDocuments int
	command := &cobra.Command{
		Use: "view", Short: "Browse the working tree code graph in a local web UI",
		Long: "Capture tracked and non-ignored working-tree files and browse their code graph.\nThe page and graph renderer are embedded; no Node.js or external service is required.\nUse Refresh in the page to capture edits. --timeout bounds each build, not server lifetime.",
		Args: cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if opts.json {
				return fmt.Errorf("view serves a web UI; --json is not supported")
			}
			if opts.timeout <= 0 {
				return fmt.Errorf("--timeout must be positive")
			}
			if maxDocuments <= 0 {
				return fmt.Errorf("--max-documents must be positive")
			}
			return viewer.ValidateAddress(addr)
		},
		RunE: func(command *cobra.Command, _ []string) error {
			load := func(ctx context.Context) (*repokit.GraphSnapshot, error) {
				buildCtx, cancel := context.WithTimeout(ctx, opts.timeout)
				defer cancel()
				return repokit.Graph(buildCtx, repokit.InputRequest{Repository: opts.repository}, maxDocuments)
			}
			if err := viewer.Run(command.Context(), addr, load, command.OutOrStdout()); err != nil {
				return executionFailure(err)
			}
			return nil
		},
	}
	command.Flags().StringVar(&addr, "addr", "127.0.0.1:5484", "loopback listen address (port 0 chooses a free port)")
	command.Flags().IntVar(&maxDocuments, "max-documents", 2000, "maximum text documents to analyze")
	return command
}
