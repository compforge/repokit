// Package forgeaccess assembles CLI connection flags and RepoKit access for PR and cleanup commands.
package forgeaccess

import (
	"context"
	"errors"

	"github.com/compforge/quality-harness/sdks/go/common"
	cmdutil "github.com/compforge/repokit/apps/cli/internal/cli/command"
	repoconfig "github.com/compforge/repokit/toolkit/go/config"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/spf13/cobra"
)

type Options struct {
	provider, apiHost   string
	Targets             []string
	configuration       *repoconfig.Config
	configurationErr    error
	configurationLoaded bool
}

var ErrNoOrigin = errors.New("checkout has no Forge origin")

func (o *Options) Flags(cmd *cobra.Command) {
	flags := cmd.PersistentFlags()
	flags.StringVar(&o.provider, "provider", "", "Forge provider: github or gitlab")
	flags.StringVar(&o.apiHost, "api-host", "", "Forge API URL override")
	flags.StringArrayVar(&o.Targets, "target-repository", nil, "target repository path")
}
func (o *Options) Validate(opts *cmdutil.Options) error {
	if opts.Timeout <= 0 {
		return errors.New("--timeout must be positive")
	}
	if o.provider != "" && o.provider != "github" && o.provider != "gitlab" {
		return errors.New("--provider must be github or gitlab")
	}
	return nil
}
func (o *Options) Context(ctx context.Context, repo string, stateRoot string) (*forge.Context, error) {
	identity, err := forge.RepositoryIdentity(ctx, repo)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, ErrNoOrigin
	}
	client, err := o.client(identity.Forge.Name, stateRoot)
	if err != nil {
		return nil, err
	}
	return forge.NewContext(ctx, client, repo, o.Targets)
}

func (o *Options) client(host, stateRoot string) (*forge.Client, error) {
	if !o.configurationLoaded {
		o.configuration, o.configurationErr = repoconfig.Load(stateRoot)
		o.configurationLoaded = true
	}
	if o.configurationErr != nil {
		return nil, o.configurationErr
	}
	access, err := o.configuration.Forge(host)
	if err != nil {
		return nil, err
	}
	if o.provider != "" && o.provider != access.Provider {
		return nil, errs.Wrap(errors.New("provider override conflicts with configured Forge provider"), "forge.provider_mismatch", "connection", host)
	}
	if o.apiHost != "" {
		access.APIHost = o.apiHost
	}
	client, err := forge.NewClient(access)
	if err != nil {
		return nil, err
	}
	return client, nil
}

// MergedPR reads Forge directly; a cache record is only a resource locator.
func (o *Options) MergedPR(ctx context.Context, repository common.Repository, number int, home string) (bool, error) {
	client, err := o.client(repository.Forge.Name, home)
	if err != nil {
		return false, err
	}
	pr, err := client.Get(ctx, repository.Path, number)
	return err == nil && pr.State == "merged", err
}
