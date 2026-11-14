package forge

import (
	"context"
	"errors"
	"slices"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/model"
	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

// RepositoryIdentity observes origin without storing credentials in the identity.
func RepositoryIdentity(ctx context.Context, path string) (result *common.Repository, err error) {
	defer func() { err = errs.Wrap(err, "forge.identity_failed", "forge.identity", path) }()
	repo, err := git.Open(ctx, path)
	if err != nil {
		return nil, err
	}
	origin, err := repo.Origin(ctx)
	if err != nil {
		return nil, err
	}
	return project.FromOrigin(origin), nil
}

// Context binds PR operations to a checkout and explicit target repositories.
// PR operations never trigger filesystem maintenance.
type Context struct {
	Client   *Client
	Checkout string
	Source   common.Repository
	Targets  []string
}

func NewContext(ctx context.Context, client *Client, checkout string, targets []string) (result *Context, err error) {
	defer func() { err = errs.Wrap(err, "forge.context_failed", "forge.context", checkout) }()
	source, err := RepositoryIdentity(ctx, checkout)
	if err != nil {
		return nil, err
	}
	if source == nil || *source != client.Repository(source.Path) {
		return nil, errors.New("checkout origin does not match Forge connection")
	}
	if len(targets) == 0 {
		targets = []string{source.Path}
	}
	return &Context{client, checkout, *source, targets}, nil
}

func (c *Context) verifyOrigin(ctx context.Context) error {
	source, err := RepositoryIdentity(ctx, c.Checkout)
	if err != nil {
		return err
	}
	if source == nil || *source != c.Source {
		return errors.New("checkout origin changed")
	}
	return nil
}

// MergedPullRequest reuses the same PR query and exact source identity checks as creation.
// +spec=`Closed is not terminal. A unique merged source-branch association permits cleanup even after local commits; open or ambiguous associations do not.`
func (c *Context) MergedPullRequest(ctx context.Context, branch string) (number int, err error) {
	defer func() { err = errs.Wrap(err, "forge.merged_lookup_failed", "forge.merged_lookup", branch) }()
	if err := c.verifyOrigin(ctx); err != nil {
		return 0, err
	}
	var selected *PullRequest
	for _, target := range c.Targets {
		rows, err := c.Client.List(ctx, target, "all", c.Source.Path, branch)
		if err != nil {
			return 0, err
		}
		for _, row := range rows {
			if row.State == "open" {
				return 0, nil
			}
			if selected != nil {
				return 0, nil
			}
			copy := row
			selected = &copy
		}
	}
	if selected == nil || selected.State != "merged" {
		return 0, nil
	}
	// List responses are discovery; use the canonical numbered resource to verify state.
	pr, err := c.Client.Get(ctx, selected.Key.Repository.Path, selected.Key.Number)
	if err != nil {
		return 0, err
	}
	if pr.State != "merged" || pr.SHA != selected.SHA || pr.SourceBranch != branch || pr.SourceRepository != c.Source {
		return 0, errors.New("merged PR identity changed")
	}
	return pr.Key.Number, nil
}

type OperationResult struct {
	PullRequest PullRequest `json:"pull_request"`
	Created     bool        `json:"created"`
}

func (c *Context) Ensure(ctx context.Context, r CreateRequest) (operation OperationResult, err error) {
	defer func() { err = errs.Wrap(err, "forge.ensure_failed", "forge.ensure", r.TargetRepository) }()
	result := OperationResult{}
	if err := c.verifyOrigin(ctx); err != nil {
		return result, err
	}
	if r.SourceRepository == "" {
		r.SourceRepository = c.Source.Path
	}
	if r.SourceRepository != c.Source.Path || !slices.Contains(c.Targets, r.TargetRepository) {
		return result, errors.New("PR source or target outside repository context")
	}
	rows, err := c.Client.List(ctx, r.TargetRepository, "open", r.SourceRepository, r.SourceBranch)
	if err != nil {
		return result, err
	}
	// +spec=`PR creation identity includes the target branch; a source can have several proposals.`
	rows = slices.DeleteFunc(rows, func(pr PullRequest) bool { return pr.TargetBranch != r.TargetBranch })
	if len(rows) > 1 {
		return result, errors.New("multiple open PRs for source and target branch")
	}
	if len(rows) == 1 {
		result.PullRequest = rows[0]
	} else {
		result.PullRequest, err = c.Client.Create(ctx, r)
		if err != nil {
			return result, err
		}
		result.Created = true
	}
	return result, nil
}

func (c *Context) Update(ctx context.Context, key model.Key, r UpdateRequest) (operation OperationResult, err error) {
	defer func() { err = errs.Wrap(err, "forge.update_failed", "forge.update", key.Repository.Path) }()
	if err := c.authorize(ctx, key); err != nil {
		return OperationResult{}, err
	}
	pr, err := c.Client.Update(ctx, key, r)
	return OperationResult{PullRequest: pr}, err
}
func (c *Context) Close(ctx context.Context, key model.Key) (operation OperationResult, err error) {
	defer func() { err = errs.Wrap(err, "forge.close_failed", "forge.close", key.Repository.Path) }()
	if err := c.authorize(ctx, key); err != nil {
		return OperationResult{}, err
	}
	pr, err := c.Client.Close(ctx, key)
	return OperationResult{PullRequest: pr}, err
}
func (c *Context) Merge(ctx context.Context, key model.Key, expectedSHA, method string) (operation OperationResult, err error) {
	defer func() { err = errs.Wrap(err, "forge.merge_failed", "forge.merge", key.Repository.Path) }()
	if err := c.authorize(ctx, key); err != nil {
		return OperationResult{}, err
	}
	pr, err := c.Client.Merge(ctx, key, expectedSHA, method)
	return OperationResult{PullRequest: pr}, err
}
func (c *Context) authorize(ctx context.Context, key model.Key) error {
	if err := c.verifyOrigin(ctx); err != nil {
		return err
	}
	if !slices.Contains(c.Targets, key.Repository.Path) || key.Repository != c.Client.Repository(key.Repository.Path) {
		return errors.New("PR target outside repository context")
	}
	return nil
}
