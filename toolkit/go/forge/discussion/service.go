package discussion

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
	"github.com/compforge/repokit/toolkit/go/forge/model"
)

// Service owns discussion queries and operations independently of PR lifecycle maintenance.
// Construct through forge.Client.Discussions to reuse its configured connection.
type Service struct {
	connection *transport.Client
	verifyPR   func(context.Context, model.Key) error
}

// New binds discussion operations to an already configured Forge transport.
// verifyPR performs the PR lifecycle client's live read before a GitHub issue-comment write.
// +rule=Discussion and PR lifecycle operations share one connection and cooldown without a package dependency cycle.
// +link=component://docs/pr.md#pr-discussions
func New(connection *transport.Client, verifyPR func(context.Context, model.Key) error) *Service {
	return &Service{connection: connection, verifyPR: verifyPR}
}

func (c *Service) validateDiscussionKey(key model.Key) error {
	if key.Number <= 0 || key.Repository.Path == "" || key.Repository != c.connection.Repository(key.Repository.Path) {
		return &errs.Error{Code: "forge.invalid_argument", Message: "discussion requires a PR identity on this Forge"}
	}
	return nil
}

// List returns all published conversation comments and review threads, excluding system notes.
// Pagination is bounded; failed or truncated reads are errors, never empty success.
func (c *Service) List(ctx context.Context, key model.Key) (result []Discussion, err error) {
	defer func() {
		err = errs.Wrap(err, "forge.discussions_failed", "forge.discussions", fmt.Sprintf("%s #%d", key.Repository.Path, key.Number))
	}()
	if err = c.validateDiscussionKey(key); err != nil {
		return nil, err
	}
	if c.connection.Provider() == "github" {
		return c.githubList(ctx, key)
	}
	return c.gitlabList(ctx, key)
}

// Get verifies membership in the specified PR, even for globally unique thread IDs.
func (c *Service) Get(ctx context.Context, key model.Key, id string) (Discussion, error) {
	if strings.TrimSpace(id) == "" {
		return Discussion{}, &errs.Error{Code: "forge.invalid_argument", Message: "thread ID is required"}
	}
	threads, err := c.List(ctx, key)
	if err != nil {
		return Discussion{}, err
	}
	for _, thread := range threads {
		if thread.ID == id {
			return thread, nil
		}
	}
	return Discussion{}, &errs.Error{Code: "forge.not_found", Op: "forge.discussion", Subject: id, Message: "thread not found in the specified PR"}
}

// Create publishes an unanchored conversation comment, without assigning a review verdict.
func (c *Service) Create(ctx context.Context, key model.Key, body string) (result Discussion, err error) {
	defer func() {
		err = discussionWriteError(err, "forge.comment", key, "")
		if err == nil {
			slog.InfoContext(ctx, "PR comment published", "repository", key.Repository.Path, "number", key.Number, "thread", result.ID)
		}
	}()
	if err = c.validateDiscussionKey(key); err != nil {
		return result, err
	}
	if strings.TrimSpace(body) == "" {
		return result, &errs.Error{Code: "forge.invalid_argument", Message: "comment body must not be empty"}
	}
	if c.connection.Provider() == "github" {
		if err = c.verifyPR(ctx, key); err != nil {
			return result, err
		}
		var wire githubComment
		err = c.connection.Request(ctx, "POST", c.connection.RepositoryPath(key.Repository.Path)+"/issues/"+strconv.Itoa(key.Number)+"/comments", map[string]any{"body": body}, &wire)
		if err != nil {
			return result, err
		}
		comment, e := wire.comment()
		if e != nil {
			return result, transport.UnknownFailure(e)
		}
		return Discussion{PullRequest: key, ID: "issue:" + comment.ID, Comments: []Comment{comment}, Resolution: "not_applicable"}, nil
	}
	var wire gitlabDiscussion
	err = c.connection.Request(ctx, "POST", c.connection.PRPath(key.Repository.Path, key.Number)+"/discussions", map[string]any{"body": body}, &wire)
	if err != nil {
		return result, err
	}
	result, err = wire.discussion(key)
	if err == nil && len(result.Comments) == 0 {
		err = errors.New("discussion response contains no published comment")
	}
	if err != nil {
		return result, transport.UnknownFailure(err)
	}
	return result, nil
}

// Reply verifies membership and platform capability immediately before sending one reply.
func (c *Service) Reply(ctx context.Context, key model.Key, id, body string) (result CommentResult, err error) {
	defer func() {
		err = discussionWriteError(err, "forge.reply", key, id)
		if err == nil {
			slog.InfoContext(ctx, "PR reply published", "repository", key.Repository.Path, "number", key.Number, "thread", id, "comment", result.Comment.ID)
		}
	}()
	if strings.TrimSpace(body) == "" {
		return result, &errs.Error{Code: "forge.invalid_argument", Message: "reply body must not be empty"}
	}
	thread, err := c.Get(ctx, key, id)
	if err != nil {
		return result, err
	}
	if !thread.Replyable {
		return result, &errs.Error{Code: "forge.unsupported_operation", Message: "this thread does not support replies"}
	}
	result.PullRequest, result.ThreadID = key, id
	if c.connection.Provider() == "github" {
		var wire githubComment
		err = c.connection.Request(ctx, "POST", c.connection.PRPath(key.Repository.Path, key.Number)+"/comments/"+url.PathEscape(thread.Comments[0].ID)+"/replies", map[string]any{"body": body}, &wire)
		if err != nil {
			return result, err
		}
		result.Comment, err = wire.comment()
	} else {
		var wire gitlabNote
		err = c.connection.Request(ctx, "POST", c.connection.PRPath(key.Repository.Path, key.Number)+"/discussions/"+url.PathEscape(id)+"/notes", map[string]any{"body": body}, &wire)
		if err != nil {
			return result, err
		}
		result.Comment, err = wire.comment()
	}
	if err != nil {
		return result, transport.UnknownFailure(err)
	}
	return result, nil
}

// Resolve changes platform resolution only. A resolved thread is an observed no-op.
func (c *Service) Resolve(ctx context.Context, key model.Key, id string) (result Discussion, err error) {
	defer func() {
		err = discussionWriteError(err, "forge.resolve_discussion", key, id)
		if err == nil {
			slog.InfoContext(ctx, "PR thread resolution confirmed", "repository", key.Repository.Path, "number", key.Number, "thread", id)
		}
	}()
	thread, err := c.Get(ctx, key, id)
	if err != nil {
		return result, err
	}
	if !thread.Resolvable {
		return result, &errs.Error{Code: "forge.unsupported_operation", Message: "this thread cannot be resolved"}
	}
	if thread.Resolution == "resolved" {
		return thread, nil
	}
	if c.connection.Provider() == "github" {
		var data struct {
			Resolve *struct {
				Thread *struct {
					ID       string `json:"id"`
					Resolved bool   `json:"isResolved"`
				} `json:"thread"`
			} `json:"resolveReviewThread"`
		}
		err = c.graphql(ctx, `mutation ResolveThread($thread: ID!) { resolveReviewThread(input: {threadId: $thread}) { thread { id isResolved } } }`, map[string]any{"thread": id}, &data, true)
		if err != nil {
			return result, err
		}
		if data.Resolve == nil || data.Resolve.Thread == nil || data.Resolve.Thread.ID != id || !data.Resolve.Thread.Resolved {
			return result, transport.UnknownFailure(errors.New("Forge did not confirm thread resolution"))
		}
		thread.Resolution = "resolved"
		return thread, nil
	}
	var wire gitlabDiscussion
	err = c.connection.Request(ctx, http.MethodPut, c.connection.PRPath(key.Repository.Path, key.Number)+"/discussions/"+url.PathEscape(id), map[string]any{"resolved": true}, &wire)
	if err != nil {
		return result, err
	}
	result, err = wire.discussion(key)
	if err == nil && len(result.Comments) == 0 {
		err = errors.New("discussion response contains no published comment")
	}
	if err != nil {
		return result, transport.UnknownFailure(err)
	}
	if result.ID != id || result.Resolution != "resolved" {
		return result, transport.UnknownFailure(errors.New("Forge did not confirm thread resolution"))
	}
	return result, nil
}

func discussionWriteError(err error, op string, key model.Key, id string) error {
	if err == nil {
		return nil
	}
	wrapped := errs.Wrap(err, "forge.discussion_write_failed", op, fmt.Sprintf("%s #%d thread=%s", key.Repository.Path, key.Number, id))
	var problem *errs.Error
	if errors.As(wrapped, &problem) && problem.Uncertain {
		copied := *problem
		copied.Op = op
		copied.Subject = fmt.Sprintf("%s #%d thread=%s", key.Repository.Path, key.Number, id)
		copied.Hint = "Read this PR's discussions and inspect the target thread/message before retrying; writes are never automatically retried."
		return &copied
	}
	return wrapped
}
