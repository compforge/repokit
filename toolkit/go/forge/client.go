// Package forge provides native GitHub/GitLab PR operations. Callers supply credentials
// and own validation and merge authorization; writes are never automatically retried.
package forge

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/compforge/quality-harness/sdks/go/common"
	repohome "github.com/compforge/repokit/toolkit/go/internal/home"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Key struct {
	Repository common.Repository `json:"repository"`
	Number     int               `json:"number"`
}
type PullRequest struct {
	Key              Key               `json:"key"`
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

// OutcomeUnknown means a write may have taken effect; reconcile before retrying.
type OutcomeUnknown struct{ Err error }

func (e *OutcomeUnknown) Error() string { return "Forge write outcome unknown: " + e.Err.Error() }
func (e *OutcomeUnknown) Unwrap() error { return e.Err }

type HTTPError struct {
	Status  int
	RetryAt time.Time
}

func (e *HTTPError) Error() string { return fmt.Sprintf("Forge HTTP %d", e.Status) }

type Config struct {
	Host, Provider, APIHost, Token string
	Home                           string // explicit option, REPOKIT_HOME, or ~/.repokit
	HTTPClient                     *http.Client
}
type Client struct {
	config   Config
	base     string
	http     *http.Client
	mu       sync.Mutex
	cooldown time.Time
}

// DetectProvider applies the same case-insensitive host policy as Python ForgeConnection.
func DetectProvider(host, explicit string) (string, error) {
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

func NewClient(config Config) (*Client, error) {
	config.Host = strings.ToLower(config.Host)
	provider, err := DetectProvider(config.Host, config.Provider)
	if err != nil {
		return nil, err
	}
	config.Provider = provider
	config.Home, err = repohome.Resolve(config.Home)
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
	return &Client{config: config, base: strings.TrimRight(base, "/"), http: client}, nil
}

func (c *Client) Repository(path string) common.Repository {
	name := c.config.Host
	if name == "github.com" {
		name = "github"
	}
	if name == "gitlab.com" {
		name = "gitlab"
	}
	return common.Repository{Forge: common.Forge{Name: name}, Path: path}
}

func (c *Client) request(ctx context.Context, method, path string, payload any, dest any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.cooldown) {
		return &HTTPError{Status: 429, RetryAt: c.cooldown}
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.config.Token != "" {
		if c.config.Provider == "github" {
			req.Header.Set("Authorization", "Bearer "+c.config.Token)
		} else {
			req.Header.Set("PRIVATE-TOKEN", c.config.Token)
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	response, err := c.http.Do(req)
	write := method != "GET"
	if err != nil {
		if write {
			return &OutcomeUnknown{err}
		}
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024+1))
	if err == nil && len(data) > 16*1024*1024 {
		err = errors.New("Forge response exceeds 16 MiB")
	}
	if err != nil {
		if write {
			return &OutcomeUnknown{err}
		}
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		httpErr := &HTTPError{Status: response.StatusCode}
		if response.StatusCode == 429 || response.StatusCode == 403 && (response.Header.Get("Retry-After") != "" || response.Header.Get("X-RateLimit-Remaining") == "0") {
			c.cooldown = time.Now().Add(30 * time.Second)
			if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil {
				c.cooldown = time.Now().Add(time.Duration(max(1, seconds)) * time.Second)
			} else if at, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
				c.cooldown = at
			}
			if epoch, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && time.Unix(epoch, 0).After(c.cooldown) {
				c.cooldown = time.Unix(epoch, 0)
			}
			httpErr.RetryAt = c.cooldown
		}
		if write && (response.StatusCode >= 500 || response.StatusCode >= 300 && response.StatusCode < 400) {
			return &OutcomeUnknown{httpErr}
		}
		return httpErr
	}
	if dest != nil {
		if err = json.Unmarshal(data, dest); err != nil {
			if write {
				return &OutcomeUnknown{err}
			}
			return err
		}
	}
	return nil
}

func (c *Client) repositoryPath(repository string) string {
	if c.config.Provider == "gitlab" {
		return "/projects/" + url.PathEscape(repository)
	}
	parts := strings.Split(repository, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/repos/" + strings.Join(parts, "/")
}
func (c *Client) prPath(repository string, number int) string {
	kind := "/pulls"
	if c.config.Provider == "gitlab" {
		kind = "/merge_requests"
	}
	path := c.repositoryPath(repository) + kind
	if number > 0 {
		path += "/" + strconv.Itoa(number)
	}
	return path
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
	pr := PullRequest{Key: Key{Repository: c.Repository(repository)}}
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
			if err := c.request(ctx, "GET", "/projects/"+strconv.Itoa(wire.SourceID), nil, &project); err != nil {
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

func (c *Client) Get(ctx context.Context, repository string, number int) (PullRequest, error) {
	if number <= 0 {
		return PullRequest{}, errors.New("PR number must be positive")
	}
	var data json.RawMessage
	if err := c.request(ctx, "GET", c.prPath(repository, number), nil, &data); err != nil {
		return PullRequest{}, err
	}
	pr, err := c.decodePR(ctx, repository, data)
	if err == nil && pr.Key.Number != number {
		err = errors.New("PR number mismatch")
	}
	return pr, err
}

// List returns a complete bounded list; a failed page never becomes an empty list.
func (c *Client) List(ctx context.Context, repository, state, source, branch string) ([]PullRequest, error) {
	if !slices.Contains([]string{"open", "closed", "merged", "all"}, state) {
		return nil, errors.New("invalid PR state")
	}
	result := []PullRequest{}
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
		if err := c.request(ctx, "GET", c.prPath(repository, 0)+"?"+query.Encode(), nil, &rows); err != nil {
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

func (c *Client) Create(ctx context.Context, r CreateRequest) (PullRequest, error) {
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
		if err := c.request(ctx, "GET", c.repositoryPath(r.TargetRepository), nil, &target); err != nil {
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
	if err := c.request(ctx, "POST", c.prPath(repository, 0), payload, &data); err != nil {
		return PullRequest{}, err
	}
	pr, err := c.decodePR(ctx, r.TargetRepository, data)
	if err != nil {
		return pr, &OutcomeUnknown{err}
	}
	if _, err := c.mutate(ctx, pr.Key, func() (PullRequest, error) { return pr, nil }); err != nil {
		slog.WarnContext(ctx, "PR cache unavailable after successful creation", "repository", pr.Key.Repository.Path, "number", pr.Key.Number, "error", err)
	}
	return pr, nil
}

type UpdateRequest struct{ Title, Body, TargetBranch *string }

func (c *Client) Update(ctx context.Context, key Key, r UpdateRequest) (PullRequest, error) {
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
func (c *Client) Close(ctx context.Context, key Key) (PullRequest, error) {
	payload := map[string]any{"state": "closed"}
	if c.config.Provider == "gitlab" {
		payload = map[string]any{"state_event": "close"}
	}
	return c.change(ctx, key, payload)
}
func (c *Client) change(ctx context.Context, key Key, payload any) (PullRequest, error) {
	if key.Number <= 0 || key.Repository != c.Repository(key.Repository.Path) {
		return PullRequest{}, errors.New("invalid PR identity")
	}
	return c.mutate(ctx, key, func() (PullRequest, error) {
		method := "PATCH"
		if c.config.Provider == "gitlab" {
			method = "PUT"
		}
		var data json.RawMessage
		if err := c.request(ctx, method, c.prPath(key.Repository.Path, key.Number), payload, &data); err != nil {
			return PullRequest{}, err
		}
		pr, err := c.decodePR(ctx, key.Repository.Path, data)
		if err != nil {
			return pr, &OutcomeUnknown{err}
		}
		return pr, nil
	})
}
func (c *Client) Merge(ctx context.Context, key Key, expectedSHA, method string) (PullRequest, error) {
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
		if err := c.request(ctx, "PUT", c.prPath(key.Repository.Path, key.Number)+"/merge", payload, &data); err != nil {
			return PullRequest{}, err
		}
		if c.config.Provider == "github" {
			var result struct{ Merged bool }
			if err := json.Unmarshal(data, &result); err != nil {
				return PullRequest{}, &OutcomeUnknown{err}
			}
			if !result.Merged {
				return PullRequest{}, errors.New("PR was not merged")
			}
		}
		pr, err := c.Get(ctx, key.Repository.Path, key.Number)
		if err != nil {
			return pr, &OutcomeUnknown{err}
		}
		return pr, nil
	})
}
