package github

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/url"
	"slices"
	"strconv"
	"strings"

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

func (c *PullRequests) decodePR(ctx context.Context, repository string, data json.RawMessage) (PullRequest, error) {
	pr := PullRequest{Key: model.Key{Repository: c.Connection.Repository(repository)}}

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
	pr.SourceRepository = c.Connection.Repository(wire.Head.Repo.Name)
	pr.Draft = wire.Draft
	if wire.Merged || wire.MergedAt != nil {
		pr.State = "merged"
	}
	if wire.Base.Repo.Name != "" && wire.Base.Repo.Name != repository {
		return pr, errors.New("PR target repository mismatch")
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

		query.Set("sort", "created")
		query.Set("direction", "desc")
		if state == "merged" {
			query.Set("state", "closed")
		}
		if branch != "" && source != "" {
			owner, _, _ := strings.Cut(source, "/")
			query.Set("head", owner+":"+branch)
		}

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

	owner, _, _ := strings.Cut(r.SourceRepository, "/")
	_, name, _ := strings.Cut(r.SourceRepository, "/")
	payload["head"] = owner + ":" + r.SourceBranch
	payload["head_repo"] = name
	payload["base"] = r.TargetBranch
	payload["body"] = r.Body
	payload["draft"] = r.Draft

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

	return c.change(ctx, key, payload)
}
func (c *PullRequests) change(ctx context.Context, key model.Key, payload any) (PullRequest, error) {
	if key.Number <= 0 || key.Repository != c.Connection.Repository(key.Repository.Path) {
		return PullRequest{}, errors.New("invalid PR identity")
	}
	return c.Mutate(ctx, key, func() (PullRequest, error) {
		method := "PATCH"

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

		var data json.RawMessage
		if err := c.Connection.Request(ctx, "PUT", c.Connection.PRPath(key.Repository.Path, key.Number)+"/merge", payload, &data); err != nil {
			return PullRequest{}, err
		}

		var result struct{ Merged bool }
		if err := json.Unmarshal(data, &result); err != nil {
			return PullRequest{}, transport.UnknownFailure(err)
		}
		if !result.Merged {
			return PullRequest{}, errors.New("PR was not merged")
		}

		pr, err := c.Get(ctx, key.Repository.Path, key.Number)
		if err != nil {
			return pr, transport.UnknownFailure(err)
		}
		return pr, nil
	})
}
