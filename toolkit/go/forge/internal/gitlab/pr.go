package gitlab

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strconv"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
	"github.com/compforge/repokit/toolkit/go/forge/model"
	"github.com/compforge/repokit/toolkit/go/forge/pr"
)

type PullRequest = pr.PullRequest
type CreateRequest = pr.CreateRequest
type UpdateRequest = pr.UpdateRequest
type PullRequests struct {
	Connection *transport.Client
	Mutate     func(context.Context, model.Key, func() (pr.PullRequest, error)) (pr.PullRequest, error)
}

var _ pr.Forge = (*PullRequests)(nil)

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

func (c *PullRequests) decodePR(ctx context.Context, repository string, data json.RawMessage) (PullRequest, error) {
	pr := PullRequest{Key: model.Key{Repository: c.Connection.Repository(repository)}}

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
		pr.SourceRepository = c.Connection.Repository(repository)
	} else if wire.SourceID != 0 {
		var project struct {
			Path string `json:"path_with_namespace"`
		}
		if err := c.Connection.Request(ctx, "GET", "/projects/"+strconv.Itoa(wire.SourceID), nil, &project); err != nil {
			return pr, err
		}
		pr.SourceRepository = c.Connection.Repository(project.Path)
	}

	if pr.Key.Number <= 0 || pr.URL == "" || !slices.Contains([]string{"open", "closed", "merged"}, pr.State) {
		return pr, errors.New("invalid PR response")
	}
	return pr, nil
}

func (c *PullRequests) Get(ctx context.Context, repository string, number int) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.get_failed", "forge.get", repository) }()
	if number <= 0 {
		return PullRequest{}, errors.New("PR number must be positive")
	}
	var data json.RawMessage
	if err := c.Connection.Request(ctx, "GET", c.Connection.PRPath(repository, number), nil, &data); err != nil {
		return PullRequest{}, err
	}
	pr, err := c.decodePR(ctx, repository, data)
	if err == nil && pr.Key.Number != number {
		err = errors.New("PR number mismatch")
	}
	return pr, err
}

func (c *PullRequests) List(ctx context.Context, repository, state, source, branch string) (result []PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.list_failed", "forge.list", repository) }()
	if !slices.Contains([]string{"open", "closed", "merged", "all"}, state) {
		return nil, errors.New("invalid PR state")
	}
	result = []PullRequest{}
	for page := 1; page <= 100; page++ {
		query := url.Values{"per_page": {"100"}, "page": {strconv.Itoa(page)}, "state": {state}}

		if state == "open" {
			query.Set("state", "opened")
		}
		if branch != "" {
			query.Set("source_branch", branch)
		}
		query.Set("order_by", "created_at")
		query.Set("sort", "desc")

		var rows []json.RawMessage
		if err := c.Connection.Request(ctx, "GET", c.Connection.PRPath(repository, 0)+"?"+query.Encode(), nil, &rows); err != nil {
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

func (c *PullRequests) Create(ctx context.Context, r CreateRequest) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.create_failed", "forge.create", r.TargetRepository) }()
	if r.SourceRepository == "" || r.TargetRepository == "" || r.SourceBranch == "" || r.TargetBranch == "" || r.Title == "" {
		return PullRequest{}, errors.New("PR source, target, branches and title are required")
	}
	repository := r.TargetRepository
	payload := map[string]any{"title": r.Title}

	var target struct{ ID int }
	if err := c.Connection.Request(ctx, "GET", c.Connection.RepositoryPath(r.TargetRepository), nil, &target); err != nil {
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

	var data json.RawMessage
	if err := c.Connection.Request(ctx, "POST", c.Connection.PRPath(repository, 0), payload, &data); err != nil {
		return PullRequest{}, err
	}
	pr, err := c.decodePR(ctx, r.TargetRepository, data)
	if err != nil {
		return pr, transport.UnknownFailure(err)
	}
	if _, err := c.Mutate(ctx, pr.Key, func() (PullRequest, error) { return pr, nil }); err != nil {
		slog.WarnContext(ctx, "PR cache unavailable after successful creation", "repository", pr.Key.Repository.Path, "number", pr.Key.Number, "error", err)
	}
	return pr, nil
}

func (c *PullRequests) Update(ctx context.Context, key model.Key, r UpdateRequest) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.update_failed", "forge.update", key.Repository.Path) }()
	payload := map[string]any{}
	if r.Title != nil {
		payload["title"] = *r.Title
	}
	body, base := "body", "base"

	body, base = "description", "target_branch"

	if r.Body != nil {
		payload[body] = *r.Body
	}
	if r.TargetBranch != nil {
		payload[base] = *r.TargetBranch
	}
	return c.change(ctx, key, payload)
}
func (c *PullRequests) Close(ctx context.Context, key model.Key) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.close_failed", "forge.close", key.Repository.Path) }()
	payload := map[string]any{"state": "closed"}

	payload = map[string]any{"state_event": "close"}

	return c.change(ctx, key, payload)
}
func (c *PullRequests) change(ctx context.Context, key model.Key, payload any) (PullRequest, error) {
	if key.Number <= 0 || key.Repository != c.Connection.Repository(key.Repository.Path) {
		return PullRequest{}, errors.New("invalid PR identity")
	}
	return c.Mutate(ctx, key, func() (PullRequest, error) {
		method := "PATCH"

		method = "PUT"

		var data json.RawMessage
		if err := c.Connection.Request(ctx, method, c.Connection.PRPath(key.Repository.Path, key.Number), payload, &data); err != nil {
			return PullRequest{}, err
		}
		pr, err := c.decodePR(ctx, key.Repository.Path, data)
		if err != nil {
			return pr, transport.UnknownFailure(err)
		}
		return pr, nil
	})
}
func (c *PullRequests) Merge(ctx context.Context, key model.Key, expectedSHA, method string) (result PullRequest, err error) {
	defer func() { err = errs.Wrap(err, "forge.merge_failed", "forge.merge", key.Repository.Path) }()
	if expectedSHA == "" || key.Number <= 0 || key.Repository != c.Connection.Repository(key.Repository.Path) {
		return PullRequest{}, errors.New("merge requires PR identity and expected SHA")
	}
	if !slices.Contains([]string{"merge", "squash", "rebase"}, method) {
		return PullRequest{}, errors.New("invalid merge method")
	}
	return c.Mutate(ctx, key, func() (PullRequest, error) {
		payload := map[string]any{"sha": expectedSHA, "merge_method": method}

		if method == "rebase" {
			return PullRequest{}, errors.New("GitLab rebase must complete before merging")
		}
		payload = map[string]any{"sha": expectedSHA, "squash": method == "squash", "should_remove_source_branch": false}

		var data json.RawMessage
		if err := c.Connection.Request(ctx, "PUT", c.Connection.PRPath(key.Repository.Path, key.Number)+"/merge", payload, &data); err != nil {
			return PullRequest{}, err
		}

		pr, err := c.Get(ctx, key.Repository.Path, key.Number)
		if err != nil {
			return pr, transport.UnknownFailure(err)
		}
		return pr, nil
	})
}
