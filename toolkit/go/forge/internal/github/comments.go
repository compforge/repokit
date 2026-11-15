package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/compforge/repokit/toolkit/go/forge/comment"
	"github.com/compforge/repokit/toolkit/go/forge/internal/commentutil"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
	"github.com/compforge/repokit/toolkit/go/forge/model"
)

type Comments struct {
	Connection *transport.Client
	VerifyPR   func(context.Context, model.Key) error
}

var _ comment.Forge = (*Comments)(nil)

type commentWire struct {
	ID                json.Number            `json:"id"`
	Body              *string                `json:"body"`
	User              struct{ Login string } `json:"user"`
	CreatedAt         string                 `json:"created_at"`
	UpdatedAt         string                 `json:"updated_at"`
	SubmittedAt       string                 `json:"submitted_at"`
	State             string                 `json:"state"`
	URL               string                 `json:"html_url"`
	Path              string                 `json:"path"`
	Side              string                 `json:"side"`
	StartSide         string                 `json:"start_side"`
	Line              *int                   `json:"line"`
	StartLine         *int                   `json:"start_line"`
	OriginalLine      *int                   `json:"original_line"`
	OriginalStartLine *int                   `json:"original_start_line"`
	Revision          string                 `json:"commit_id"`
	OriginalRevision  string                 `json:"original_commit_id"`
	Parent            json.Number            `json:"in_reply_to_id"`
}

func (w commentWire) value(key model.Key, kind string, thread *githubThread) (comment.Comment, error) {
	if w.ID == "" || w.Body == nil {
		return comment.Comment{}, errors.New("invalid GitHub comment response")
	}
	out := comment.Comment{ID: kind + ":" + string(w.ID), PR: key, Body: *w.Body, Author: w.User.Login, CreatedAt: w.CreatedAt, UpdatedAt: w.UpdatedAt, URL: w.URL, Resolution: comment.Unsupported}
	if out.CreatedAt == "" {
		out.CreatedAt = w.SubmittedAt
	}
	if w.Parent != "" {
		out.ReplyToID = "review:" + string(w.Parent)
	}
	if kind == "review" {
		out.Resolution = comment.Unknown
		if w.Path != "" {
			out.Anchor = &comment.Anchor{Path: w.Path, Revision: w.Revision, Side: w.Side, Line: w.Line, StartLine: w.StartLine, StartSide: w.StartSide, OriginalRevision: w.OriginalRevision, OriginalLine: w.OriginalLine, OriginalStartLine: w.OriginalStartLine}
		}
		if thread != nil {
			out.Resolution = comment.Unresolved
			if *thread.Resolved {
				out.Resolution = comment.Resolved
			}
			out.ResolvedBy = thread.ResolvedBy.Login
			if out.Anchor != nil {
				out.Anchor.Outdated = thread.Outdated
			}
		}
	}
	return out, nil
}
func (c *Comments) inventory(ctx context.Context, key model.Key) ([]comment.Comment, map[string]githubThread, error) {
	if err := commentutil.Key(c.Connection, key); err != nil {
		return nil, nil, err
	}
	threads, err := c.githubThreads(ctx, key)
	if err != nil {
		return nil, nil, err
	}
	roots := map[string]githubThread{}
	for _, t := range threads {
		id := string(t.Comments.Nodes[0].ID)
		if _, exists := roots[id]; exists {
			return nil, nil, errors.New("duplicate GitHub thread root")
		}
		roots[id] = t
	}
	base := c.Connection.PRPath(key.Repository.Path, key.Number)
	issue, err := commentutil.Pages[commentWire](ctx, c.Connection, c.Connection.RepositoryPath(key.Repository.Path)+"/issues/"+strconv.Itoa(key.Number)+"/comments")
	if err != nil {
		return nil, nil, err
	}
	review, err := commentutil.Pages[commentWire](ctx, c.Connection, base+"/comments")
	if err != nil {
		return nil, nil, err
	}
	summaries, err := commentutil.Pages[commentWire](ctx, c.Connection, base+"/reviews")
	if err != nil {
		return nil, nil, err
	}
	result := []comment.Comment{}
	targets := map[string]githubThread{}
	seen := map[string]bool{}
	observedRoots := map[string]bool{}
	add := func(w commentWire, kind string, t *githubThread) error {
		v, e := w.value(key, kind, t)
		if e != nil {
			return e
		}
		if seen[v.ID] {
			return errors.New("duplicate GitHub comment identity")
		}
		seen[v.ID] = true
		result = append(result, v)
		return nil
	}
	for _, w := range issue {
		if err = add(w, "conversation", nil); err != nil {
			return nil, nil, err
		}
	}
	for _, w := range review {
		root := string(w.Parent)
		if root == "" {
			root = string(w.ID)
			observedRoots[root] = true
		}
		t, ok := roots[root]
		if !ok {
			return nil, nil, errors.New("GitHub comment/thread mismatch; refresh required")
		}
		if err = add(w, "review", &t); err != nil {
			return nil, nil, err
		}
		targets["review:"+string(w.ID)] = t
	}
	if len(observedRoots) != len(roots) {
		return nil, nil, errors.New("GitHub thread root missing from comment inventory")
	}
	for _, w := range summaries {
		if w.State != "PENDING" && w.SubmittedAt != "" && w.Body != nil && *w.Body != "" {
			if err = add(w, "summary", nil); err != nil {
				return nil, nil, err
			}
		}
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].CreatedAt < result[j].CreatedAt })
	return result, targets, nil
}
func (c *Comments) List(ctx context.Context, key model.Key) ([]comment.Comment, error) {
	v, _, e := c.inventory(ctx, key)
	return v, e
}
func (c *Comments) Get(ctx context.Context, key model.Key, id string) (comment.Comment, error) {
	v, e := c.List(ctx, key)
	if e != nil {
		return comment.Comment{}, e
	}
	return commentutil.Find(v, id)
}
func (c *Comments) write(ctx context.Context, key model.Key, method, path string, payload any, kind, body, id string) (comment.Comment, error) {
	var wire commentWire
	if err := c.Connection.Request(ctx, method, path, payload, &wire); err != nil {
		return comment.Comment{}, err
	}
	v, e := wire.value(key, kind, nil)
	return commentutil.Confirm(v, body, id, e)
}
func (c *Comments) Create(ctx context.Context, key model.Key, body string, a *comment.Anchor) (comment.Comment, error) {
	if e := commentutil.Key(c.Connection, key); e != nil {
		return comment.Comment{}, e
	}
	if e := commentutil.Body(body); e != nil {
		return comment.Comment{}, e
	}
	if e := commentutil.ValidateAnchor(a); e != nil {
		return comment.Comment{}, e
	}
	if e := c.VerifyPR(ctx, key); e != nil {
		return comment.Comment{}, e
	}
	payload := map[string]any{"body": body}
	kind := "conversation"
	path := c.Connection.RepositoryPath(key.Repository.Path) + "/issues/" + strconv.Itoa(key.Number) + "/comments"
	if a != nil {
		kind = "review"
		path = c.Connection.PRPath(key.Repository.Path, key.Number) + "/comments"
		payload["commit_id"] = a.Revision
		payload["path"] = a.Path
		if a.Line == nil {
			payload["subject_type"] = "file"
		} else {
			payload["line"] = *a.Line
			payload["side"] = a.Side
			if a.StartLine != nil {
				payload["start_line"] = *a.StartLine
				side := a.StartSide
				if side == "" {
					side = a.Side
				}
				payload["start_side"] = side
			}
		}
	}
	return c.write(ctx, key, "POST", path, payload, kind, body, "")
}
func (c *Comments) Reply(ctx context.Context, key model.Key, id, body string) (comment.Comment, error) {
	if e := commentutil.Body(body); e != nil {
		return comment.Comment{}, e
	}
	target, e := c.Get(ctx, key, id)
	if e != nil {
		return comment.Comment{}, e
	}
	if !strings.HasPrefix(id, "review:") {
		return comment.Comment{}, commentutil.Unsupported("GitHub only supports replies within review comments")
	}
	if target.ReplyToID != "" {
		id = target.ReplyToID
	}
	result, e := c.write(ctx, key, "POST", c.Connection.PRPath(key.Repository.Path, key.Number)+"/comments/"+strings.TrimPrefix(id, "review:")+"/replies", map[string]any{"body": body}, "review", body, "")
	if e == nil && result.ReplyToID != id {
		return result, transport.UnknownFailure(errors.New("GitHub did not confirm reply parent"))
	}
	result.Resolution = target.Resolution
	return result, e
}
func (c *Comments) Update(ctx context.Context, key model.Key, id, body string) (comment.Comment, error) {
	if e := commentutil.Body(body); e != nil {
		return comment.Comment{}, e
	}
	target, e := c.Get(ctx, key, id)
	if e != nil {
		return comment.Comment{}, e
	}
	kind, raw, _ := strings.Cut(id, ":")
	path := c.Connection.RepositoryPath(key.Repository.Path)
	method := "PATCH"
	switch kind {
	case "conversation":
		path += "/issues/comments/" + raw
	case "review":
		path += "/pulls/comments/" + raw
	case "summary":
		method = "PUT"
		path = c.Connection.PRPath(key.Repository.Path, key.Number) + "/reviews/" + raw
	}
	result, e := c.write(ctx, key, method, path, map[string]any{"body": body}, kind, body, id)
	result.Resolution = target.Resolution
	return result, e
}
func (c *Comments) Delete(ctx context.Context, key model.Key, id string) (comment.DeleteResult, error) {
	result := comment.DeleteResult{PR: key, CommentID: id}
	rows, e := c.List(ctx, key)
	if e != nil {
		return result, e
	}
	if _, e = commentutil.Find(rows, id); e != nil {
		return result, e
	}
	kind, raw, _ := strings.Cut(id, ":")
	if kind == "summary" {
		return result, commentutil.Unsupported("GitHub cannot delete a submitted review summary")
	}
	for _, r := range rows {
		if r.ReplyToID == id {
			return result, commentutil.Unsupported("delete replies explicitly before deleting their root")
		}
	}
	surface := "pulls"
	if kind == "conversation" {
		surface = "issues"
	}
	e = c.Connection.Request(ctx, "DELETE", c.Connection.RepositoryPath(key.Repository.Path)+"/"+surface+"/comments/"+raw, nil, nil)
	result.Deleted = e == nil
	return result, e
}
func (c *Comments) Resolve(ctx context.Context, key model.Key, id string, resolved bool) (comment.Comment, error) {
	rows, targets, e := c.inventory(ctx, key)
	if e != nil {
		return comment.Comment{}, e
	}
	target, e := commentutil.Find(rows, id)
	if e != nil {
		return target, e
	}
	t, ok := targets[id]
	if !ok {
		return target, commentutil.Unsupported("comment has no resolution capability")
	}
	if *t.Resolved == resolved {
		return target, nil
	}
	action := "resolveReviewThread"
	if !resolved {
		action = "unresolveReviewThread"
	}
	var data map[string]struct {
		Thread *struct {
			ID         string                  `json:"id"`
			Resolved   *bool                   `json:"isResolved"`
			ResolvedBy *struct{ Login string } `json:"resolvedBy"`
		} `json:"thread"`
	}
	e = c.graphql(ctx, fmt.Sprintf("mutation CommentResolution($thread:ID!){%s(input:{threadId:$thread}){thread{id isResolved resolvedBy{login}}}}", action), map[string]any{"thread": t.ID}, &data, true)
	if e != nil {
		return target, e
	}
	observed := data[action].Thread
	if observed == nil || observed.ID != t.ID || observed.Resolved == nil || *observed.Resolved != resolved {
		return target, transport.UnknownFailure(errors.New("GitHub did not confirm resolution"))
	}
	target.Resolution = comment.Unresolved
	target.ResolvedBy = ""
	if resolved {
		target.Resolution = comment.Resolved
	}
	if observed.ResolvedBy != nil {
		target.ResolvedBy = observed.ResolvedBy.Login
	}
	return target, nil
}
