package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/compforge/repokit/apps/cli/internal/upgrade"
	"github.com/spf13/cobra"
)

type upgradeClient interface {
	Check(context.Context, string) (upgrade.Release, error)
	Install(context.Context, upgrade.Release, string) error
}

func newUpgradeCommand(opts *options, version string, client upgradeClient) *cobra.Command {
	var check bool
	command := &cobra.Command{
		Use:   "upgrade",
		Short: "Check for or install the latest stable CLI release",
		Args:  cobra.NoArgs,
		PreRunE: func(_ *cobra.Command, _ []string) error {
			if opts.timeout <= 0 {
				return fmt.Errorf("timeout must be positive")
			}
			return nil
		},
		RunE: func(command *cobra.Command, _ []string) error {
			ctx, cancel := context.WithTimeout(command.Context(), opts.timeout)
			defer cancel()
			release, err := client.Check(ctx, version)
			if err != nil {
				return executionFailure(err)
			}
			updated := false
			if !check && release.UpdateAvailable {
				executable, err := os.Executable()
				if err != nil {
					return executionFailure(err)
				}
				if err := client.Install(ctx, release, executable); err != nil {
					return executionFailure(err)
				}
				updated = true
			}
			if opts.json {
				err = writeJSON(command.OutOrStdout(), struct {
					upgrade.Release
					Updated bool `json:"updated"`
				}{release, updated})
			} else if updated {
				_, err = fmt.Fprintf(command.OutOrStdout(), "Upgraded repocli %s -> %s\n", release.CurrentVersion, release.LatestVersion)
			} else if release.UpdateAvailable {
				_, err = fmt.Fprintf(command.OutOrStdout(), "repocli %s is available (installed %s); run repocli upgrade\n", release.LatestVersion, release.CurrentVersion)
			} else {
				_, err = fmt.Fprintf(command.OutOrStdout(), "repocli %s is up to date (latest stable %s)\n", release.CurrentVersion, release.LatestVersion)
			}
			if err != nil {
				return executionFailure(err)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&check, "check", false, "only check for updates; do not change the installed binary")
	return command
}
