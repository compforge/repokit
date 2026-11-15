// Package forge provides native GitHub/GitLab PR operations. Callers supply credentials
// and own validation and merge authorization; writes are never automatically retried.
package forge

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/comment"
	"github.com/compforge/repokit/toolkit/go/forge/internal/github"
	"github.com/compforge/repokit/toolkit/go/forge/internal/gitlab"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
	"github.com/compforge/repokit/toolkit/go/forge/model"
	"github.com/compforge/repokit/toolkit/go/forge/pr"
	"github.com/compforge/repokit/toolkit/go/forge/release"
	repohome "github.com/compforge/repokit/toolkit/go/state"
)

type PullRequest = pr.PullRequest
type CreateRequest = pr.CreateRequest
type UpdateRequest = pr.UpdateRequest

type Config struct {
	Host, Provider, APIHost, Token string
	Home                           string // explicit option, REPOKIT_HOME, or ~/.repokit
	HTTPClient                     *http.Client
}
type Client struct {
	config    Config
	transport *transport.Client
}

// DetectProvider applies the same case-insensitive host policy as Python ForgeConnection.
func DetectProvider(host, explicit string) (provider string, err error) {
	defer func() { err = errs.Wrap(err, "forge.invalid_provider", "forge.detect_provider", host) }()
	if explicit != "" {
		if explicit != "github" && explicit != "gitlab" {
			return "", errors.New("provider must be github or gitlab")
		}
		return explicit, nil
	}
	host = strings.ToLower(host)
	if host == "github.com" || strings.HasPrefix(host, "github.") {
		return "github", nil
	}
	return "gitlab", nil
}

func NewClient(config Config) (result *Client, err error) {
	defer func() { err = errs.Wrap(err, "forge.connection_failed", "forge.connect", config.Host) }()
	config.Host = strings.ToLower(config.Host)
	provider, err := DetectProvider(config.Host, config.Provider)
	if err != nil {
		return nil, err
	}
	config.Provider = provider
	config.Home, err = repohome.ResolveHome(config.Home)
	if err != nil {
		return nil, err
	}
	base := config.APIHost
	if base == "" {
		if config.Provider == "github" {
			if config.Host == "github.com" {
				base = "https://api.github.com"
			} else {
				base = "https://" + config.Host + "/api/v3"
			}
		} else {
			base = "https://" + config.Host + "/api/v4"
		}
	}
	parsed, err := url.Parse(base)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.User != nil {
		return nil, errors.New("invalid Forge API URL")
	}
	client := config.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	}
	return &Client{config: config, transport: transport.New(config.Host, config.Provider, config.Token, strings.TrimRight(base, "/"), client)}, nil
}

// PullRequests selects the provider implementation; observation remains owned by Client.
func (c *Client) PullRequests() pr.Forge {
	if c.config.Provider == "github" {
		return &github.PullRequests{Connection: c.transport, Mutate: c.mutate}
	}
	return &gitlab.PullRequests{Connection: c.transport, Mutate: c.mutate}
}
func (c *Client) Get(ctx context.Context, repository string, number int) (PullRequest, error) {
	return c.PullRequests().Get(ctx, repository, number)
}
func (c *Client) List(ctx context.Context, repository, state, source, branch string) ([]PullRequest, error) {
	return c.PullRequests().List(ctx, repository, state, source, branch)
}
func (c *Client) Create(ctx context.Context, r CreateRequest) (PullRequest, error) {
	return c.PullRequests().Create(ctx, r)
}
func (c *Client) Update(ctx context.Context, key model.Key, r UpdateRequest) (PullRequest, error) {
	return c.PullRequests().Update(ctx, key, r)
}
func (c *Client) Close(ctx context.Context, key model.Key) (PullRequest, error) {
	return c.PullRequests().Close(ctx, key)
}
func (c *Client) Merge(ctx context.Context, key model.Key, sha, method string) (PullRequest, error) {
	return c.PullRequests().Merge(ctx, key, sha, method)
}

// Repository identifies a resource on this client's configured Forge.
func (c *Client) Repository(path string) common.Repository { return c.transport.Repository(path) }

// Comments returns the provider implementation of the comment domain interface.
func (c *Client) Comments() comment.Forge {
	return &observedComments{client: c, remote: c.commentProvider()}
}
func (c *Client) commentProvider() comment.Forge {
	if c.config.Provider == "github" {
		return &github.Comments{Connection: c.transport, VerifyPR: func(ctx context.Context, key model.Key) error {
			_, e := c.Get(ctx, key.Repository.Path, key.Number)
			return e
		}}
	}
	return &gitlab.Comments{Connection: c.transport}
}

// Releases shares this client's configured connection without PR lifecycle maintenance.
func (c *Client) Releases() *release.Service { return release.New(c.transport) }
