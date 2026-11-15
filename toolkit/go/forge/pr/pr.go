// Package pr defines PR lifecycle facts and the provider interface.
package pr

import (
	"context"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/forge/model"
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

type CreateRequest struct {
	SourceRepository, SourceBranch, TargetRepository, TargetBranch, Title, Body string
	Draft                                                                       bool
}

type UpdateRequest struct{ Title, Body, TargetBranch *string }

type Forge interface {
	Get(context.Context, string, int) (PullRequest, error)
	List(context.Context, string, string, string, string) ([]PullRequest, error)
	Create(context.Context, CreateRequest) (PullRequest, error)
	Update(context.Context, model.Key, UpdateRequest) (PullRequest, error)
	Close(context.Context, model.Key) (PullRequest, error)
	Merge(context.Context, model.Key, string, string) (PullRequest, error)
}
