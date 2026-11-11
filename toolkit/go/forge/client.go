// Package forge provides native GitHub/GitLab PR operations. Callers supply credentials
// and own validation and merge authorization; writes are never automatically retried.
package forge

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/discussion"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
	"github.com/compforge/repokit/toolkit/go/forge/model"
	"github.com/compforge/repokit/toolkit/go/forge/release"
	repohome "github.com/compforge/repokit/toolkit/go/state"
)

type PullRequest struct {
	Key              model.Key         `json:"key"`
	Title            string            `json:"title"`
	Body             string            `json:"body"`
	URL              string            `json:"url"`
	State            string            `json:"state"`
	SHA              string            `json:"headSha"`
	SourceBranch     string            `json:"sourceBranch"`
	TargetBranch     string            `json:"targetBranch"`
	SourceRepository common.Repository `json:"sourceRepository"`
	Draft            bool              `json:"draft"`
}

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

type githubPR struct {
	Number             int
	Title, Body, State string
	URL                string `json:"html_url"`
	Merged             bool
	MergedAt           *string `json:"merged_at"`
	Draft              bool
	Head               struct {
		SHA, Ref string
		Repo     struct {
			Name string `json:"full_name"`
		}
	}
	Base struct {
		Ref  string
		Repo struct {
			Name string `json:"full_name"`
		}
	}
}
type gitlabPR struct {
	IID                            int
	Title, Description, State, SHA string
	URL                            string `json:"web_url"`
	Draft                          bool
	SourceBranch                   string `json:"source_branch"`
	TargetBranch                   string `json:"target_branch"`
	SourceID                       int    `json:"source_project_id"`
	TargetID                       int    `json:"target_project_id"`
}

func (c *Client) decodePR(ctx context.Context, repository string, data json.RawMessage) (PullRequest, error) {
	pr := PullRequest{Key: model.Key{Repository: c.Repository(repository)}}
	if c.config.Provider == "github" {
		var wire githubPR
		if err := json.Unmarshal(data, &wire); err != nil {
			return pr, err
		}
		pr.Key.Number = wire.Number
		pr.Title = wire.Title
		pr.Body = wire.Body
		pr.State = wire.State
		pr.URL = wire.URL
		pr.SHA = wire.Head.SHA
		pr.SourceBranch = wire.Head.Ref
		pr.TargetBranch = wire.Base.Ref
		pr.SourceRepository = c.Repository(wire.Head.Repo.Name)
		pr.Draft = wire.Draft
		if wire.Merged || wire.MergedAt != nil {
			pr.State = "merged"
		}
		if wire.Base.Repo.Name != "" && wire.Base.Repo.Name != repository {
			return pr, errors.New("PR target repository mismatch")
		}
	} else {
		var wire gitlabPR
		if err := json.Unmarshal(data, &wire); err != nil {
			return pr, err
		}
		pr.Key.Number = wire.IID
		pr.Title = wire.Title
		pr.Body = wire.Description
		pr.State = wire.State
		if pr.State == "opened" {
			pr.State = "open"
		}
		pr.URL = wire.URL
		pr.SHA = wire.SHA
		pr.SourceBranch = wire.SourceBranch
		pr.TargetBranch = wire.TargetBranch
		pr.Draft = wire.Draft
		if wire.SourceID == wire.TargetID && wire.SourceID != 0 {
			pr.SourceRepository = c.Repository(repository)
		} else if wire.SourceID != 0 {
			var project struct {
				Path string `json:"path_with_namespace"`
			}
			if err := c.transport.Request(ctx, "GET", "/projects/"+strconv.Itoa(wire.SourceID), nil, &project); err != nil {
				return pr, err
			}
			pr.SourceRepository = c.Repository(project.Path)
		}
	}
	if pr.Key.Number <= 0 || pr.URL == "" || !slices.Contains([]string{"open", "closed", "merged"}, pr.State) {
		return pr, errors.New("invalid PR response")
	}
	return pr, nil
}

func (c *Client) Get(ctx context.Context, repository string, number int) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.get_failed", "forge.get", repository) }()
	if number <= 0 {
		return PullRequest{}, errors.New("PR number must be positive")
	}
	var data json.RawMessage
	if err := c.transport.Request(ctx, "GET", c.transport.PRPath(repository, number), nil, &data); err != nil {
		return PullRequest{}, err
	}
	pr, err := c.decodePR(ctx, repository, data)
	if err == nil && pr.Key.Number != number {
		err = errors.New("PR number mismatch")
	}
	return pr, err
}

// List returns a complete bounded list; a failed page never becomes an empty list.
func (c *Client) List(ctx context.Context, repository, state, source, branch string) (result []PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.list_failed", "forge.list", repository) }()
	if !slices.Contains([]string{"open", "closed", "merged", "all"}, state) {
		return nil, errors.New("invalid PR state")
	}
	result = []PullRequest{}
	for page := 1; page <= 100; page++ {
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}, "state": {state}}
		if c.config.Provider == "github" {
			query.Set("sort", "created")
			query.Set("direction", "desc")
			if state == "merged" {
				query.Set("state", "closed")
			}
			if branch != "" && source != "" {
				owner, _, _ := strings.Cut(source, "/")
				query.Set("head", owner+":"+branch)
			}
		} else {
			if state == "open" {
				query.Set("state", "opened")
			}
			if branch != "" {
				query.Set("source_branch", branch)
			}
			query.Set("order_by", "created_at")
			query.Set("sort", "desc")
		}
		var rows []json.RawMessage
		if err := c.transport.Request(ctx, "GET", c.transport.PRPath(repository, 0)+"?"+query.Encode(), nil, &rows); err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, errors.New("invalid PR list response")
		}
		for _, row := range rows {
			pr, err := c.decodePR(ctx, repository, row)
			if err != nil {
				return nil, err
			}
			if state != "all" && pr.State != state || source != "" && pr.SourceRepository.Path != source || branch != "" && pr.SourceBranch != branch {
				continue
			}
			result = append(result, pr)
		}
		if len(rows) < 100 {
			return result, nil
		}
	}
	return nil, errors.New("PR list exceeds 10000 items")
}

type CreateRequest struct {
	SourceRepository, SourceBranch, TargetRepository, TargetBranch, Title, Body string
	Draft                                                                       bool
}

func (c *Client) Create(ctx context.Context, r CreateRequest) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.create_failed", "forge.create", r.TargetRepository) }()
	if r.SourceRepository == "" || r.TargetRepository == "" || r.SourceBranch == "" || r.TargetBranch == "" || r.Title == "" {
		return PullRequest{}, errors.New("PR source, target, branches and title are required")
	}
	repository := r.TargetRepository
	payload := map[string]any{"title": r.Title}
	if c.config.Provider == "github" {
		owner, _, _ := strings.Cut(r.SourceRepository, "/")
		_, name, _ := strings.Cut(r.SourceRepository, "/")
		payload["head"] = owner + ":" + r.SourceBranch
		payload["head_repo"] = name
		payload["base"] = r.TargetBranch
		payload["body"] = r.Body
		payload["draft"] = r.Draft
	} else {
		var target struct{ ID int }
		if err := c.transport.Request(ctx, "GET", c.transport.RepositoryPath(r.TargetRepository), nil, &target); err != nil {
			return PullRequest{}, err
		}
		if target.ID <= 0 {
			return PullRequest{}, errors.New("invalid target project ID")
		}
		repository = r.SourceRepository
		payload["target_project_id"] = target.ID
		payload["source_branch"] = r.SourceBranch
		payload["target_branch"] = r.TargetBranch
		payload["description"] = r.Body
		if r.Draft {
			payload["title"] = "Draft: " + r.Title
		}
	}
	var data json.RawMessage
	if err := c.transport.Request(ctx, "POST", c.transport.PRPath(repository, 0), payload, &data); err != nil {
		return PullRequest{}, err
	}
	pr, err := c.decodePR(ctx, r.TargetRepository, data)
	if err != nil {
		return pr, transport.UnknownFailure(err)
	}
	if _, err := c.mutate(ctx, pr.Key, func() (PullRequest, error) { return pr, nil }); err != nil {
		slog.WarnContext(ctx, "PR cache unavailable after successful creation", "repository", pr.Key.Repository.Path, "number", pr.Key.Number, "error", err)
	}
	return pr, nil
}

type UpdateRequest struct{ Title, Body, TargetBranch *string }

func (c *Client) Update(ctx context.Context, key model.Key, r UpdateRequest) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.update_failed", "forge.update", key.Repository.Path) }()
	payload := map[string]any{}
	if r.Title != nil {
		payload["title"] = *r.Title
	}
	body, base := "body", "base"
	if c.config.Provider == "gitlab" {
		body, base = "description", "target_branch"
	}
	if r.Body != nil {
		payload[body] = *r.Body
	}
	if r.TargetBranch != nil {
		payload[base] = *r.TargetBranch
	}
	return c.change(ctx, key, payload)
}
func (c *Client) Close(ctx context.Context, key model.Key) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.close_failed", "forge.close", key.Repository.Path) }()
	payload := map[string]any{"state": "closed"}
	if c.config.Provider == "gitlab" {
		payload = map[string]any{"state_event": "close"}
	}
	return c.change(ctx, key, payload)
}
func (c *Client) change(ctx context.Context, key model.Key, payload any) (PullRequest, error) {
	if key.Number <= 0 || key.Repository != c.Repository(key.Repository.Path) {
		return PullRequest{}, errors.New("invalid PR identity")
	}
	return c.mutate(ctx, key, func() (PullRequest, error) {
		method := "PATCH"
		if c.config.Provider == "gitlab" {
			method = "PUT"
		}
		var data json.RawMessage
		if err := c.transport.Request(ctx, method, c.transport.PRPath(key.Repository.Path, key.Number), payload, &data); err != nil {
			return PullRequest{}, err
		}
		pr, err := c.decodePR(ctx, key.Repository.Path, data)
		if err != nil {
			return pr, transport.UnknownFailure(err)
		}
		return pr, nil
	})
}
func (c *Client) Merge(ctx context.Context, key model.Key, expectedSHA, method string) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.merge_failed", "forge.merge", key.Repository.Path) }()
	if expectedSHA == "" || key.Number <= 0 || key.Repository != c.Repository(key.Repository.Path) {
		return PullRequest{}, errors.New("merge requires PR identity and expected SHA")
	}
	if !slices.Contains([]string{"merge", "squash", "rebase"}, method) {
		return PullRequest{}, errors.New("invalid merge method")
	}
	return c.mutate(ctx, key, func() (PullRequest, error) {
		payload := map[string]any{"sha": expectedSHA, "merge_method": method}
		if c.config.Provider == "gitlab" {
			if method == "rebase" {
				return PullRequest{}, errors.New("GitLab rebase must complete before merging")
			}
			payload = map[string]any{"sha": expectedSHA, "squash": method == "squash", "should_remove_source_branch": false}
		}
		var data json.RawMessage
		if err := c.transport.Request(ctx, "PUT", c.transport.PRPath(key.Repository.Path, key.Number)+"/merge", payload, &data); err != nil {
			return PullRequest{}, err
		}
		if c.config.Provider == "github" {
			var result struct{ Merged bool }
			if err := json.Unmarshal(data, &result); err != nil {
				return PullRequest{}, transport.UnknownFailure(err)
			}
			if !result.Merged {
				return PullRequest{}, errors.New("PR was not merged")
			}
		}
		pr, err := c.Get(ctx, key.Repository.Path, key.Number)
		if err != nil {
			return pr, transport.UnknownFailure(err)
		}
		return pr, nil
	})
}

// Repository identifies a resource on this client's configured Forge.
func (c *Client) Repository(path string) common.Repository { return c.transport.Repository(path) }

// Discussions shares this connection's credentials, HTTP client, budgets and cooldown.
func (c *Client) Discussions() *discussion.Service {
	return discussion.New(c.transport, func(ctx context.Context, key model.Key) error {
		_, err := c.Get(ctx, key.Repository.Path, key.Number)
		return err
	})
}

// Releases shares this client's configured connection without PR lifecycle maintenance.
func (c *Client) Releases() *release.Service { return release.New(c.transport) }
