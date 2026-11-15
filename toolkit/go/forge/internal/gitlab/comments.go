package gitlab

import (
	"context"
	"crypto/sha1"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/compforge/repokit/toolkit/go/forge/comment"
	"github.com/compforge/repokit/toolkit/go/forge/internal/commentutil"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
	"github.com/compforge/repokit/toolkit/go/forge/model"
)

type Comments struct{ Connection *transport.Client }

var _ comment.Forge = (*Comments)(nil)

type user struct {
	Username string `json:"username"`
}
type notePosition struct {
	NewPath string `json:"new_path"`
	OldPath string `json:"old_path"`
	NewLine *int   `json:"new_line"`
	OldLine *int   `json:"old_line"`
	Head    string `json:"head_sha"`
	Base    string `json:"base_sha"`
	Range   *struct {
		Start struct {
			NewLine *int   `json:"new_line"`
			OldLine *int   `json:"old_line"`
			Type    string `json:"type"`
		} `json:"start"`
	} `json:"line_range"`
}
type note struct {
	ID         json.Number   `json:"id"`
	Body       *string       `json:"body"`
	Author     user          `json:"author"`
	CreatedAt  string        `json:"created_at"`
	UpdatedAt  string        `json:"updated_at"`
	URL        string        `json:"url"`
	System     bool          `json:"system"`
	Resolvable *bool         `json:"resolvable"`
	Resolved   *bool         `json:"resolved"`
	ResolvedBy *user         `json:"resolved_by"`
	ResolvedAt string        `json:"resolved_at"`
	Position   *notePosition `json:"position"`
}
type discussion struct {
	ID         string `json:"id"`
	Individual *bool  `json:"individual_note"`
	Notes      []note `json:"notes"`
}

// root is the platform's first published user note, not a body/time heuristic.
func (d discussion) root() string {
	for _, n := range d.Notes {
		if !n.System {
			return "note:" + string(n.ID)
		}
	}
	return ""
}
func (d discussion) resolution() comment.Resolution {
	if d.Individual != nil && *d.Individual {
		return comment.Unsupported
	}
	count := 0
	unknown := false
	for _, n := range d.Notes {
		if n.System || n.Resolvable != nil && !*n.Resolvable {
			continue
		}
		count++
		if n.Resolved == nil {
			unknown = true
		} else if !*n.Resolved {
			return comment.Unresolved
		}
	}
	if count == 0 {
		return comment.Unsupported
	}
	if unknown {
		return comment.Unknown
	}
	return comment.Resolved
}
func (n note) value(key model.Key, d *discussion) (comment.Comment, error) {
	if n.ID == "" || n.Body == nil {
		return comment.Comment{}, errors.New("invalid GitLab note response")
	}
	out := comment.Comment{ID: "note:" + string(n.ID), PR: key, Body: *n.Body, Author: n.Author.Username, CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt, URL: n.URL, Resolution: comment.Unknown, ResolvedAt: n.ResolvedAt}
	if d != nil {
		out.Resolution = d.resolution()
		if root := d.root(); root != out.ID {
			out.ReplyToID = root
		}
	}
	if n.ResolvedBy != nil {
		out.ResolvedBy = n.ResolvedBy.Username
	}
	if p := n.Position; p != nil {
		a := &comment.Anchor{Path: p.NewPath, OldPath: p.OldPath, Revision: p.Head, OriginalRevision: p.Base, Side: "RIGHT", Line: p.NewLine}
		if a.Path == "" {
			a.Path = p.OldPath
		}
		if p.NewLine == nil {
			a.Side = ""
			if p.OldLine != nil {
				a.Side = "LEFT"
			}
			a.Line = p.OldLine
		}
		if p.Range != nil {
			if p.Range.Start.Type == "old" {
				a.StartSide = "LEFT"
			}
			a.StartLine = p.Range.Start.OldLine
			if p.Range.Start.Type == "new" {
				a.StartSide = "RIGHT"
				a.StartLine = p.Range.Start.NewLine
			}
		}
		out.Anchor = a
	}
	return out, nil
}
func (c *Comments) inventory(ctx context.Context, key model.Key) ([]comment.Comment, map[string]discussion, error) {
	if e := commentutil.Key(c.Connection, key); e != nil {
		return nil, nil, e
	}
	ds, e := commentutil.Pages[discussion](ctx, c.Connection, c.Connection.PRPath(key.Repository.Path, key.Number)+"/discussions")
	if e != nil {
		return nil, nil, e
	}
	out := []comment.Comment{}
	targets := map[string]discussion{}
	for _, d := range ds {
		if d.ID == "" || d.Individual == nil || len(d.Notes) == 0 {
			return nil, nil, errors.New("invalid GitLab discussion response")
		}
		for _, n := range d.Notes {
			if n.System {
				continue
			}
			v, e := n.value(key, &d)
			if e != nil {
				return nil, nil, e
			}
			if _, ok := targets[v.ID]; ok {
				return nil, nil, errors.New("duplicate GitLab comment identity")
			}
			out = append(out, v)
			targets[v.ID] = d
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt < out[j].CreatedAt })
	return out, targets, nil
}
func (c *Comments) List(ctx context.Context, key model.Key) ([]comment.Comment, error) {
	v, _, e := c.inventory(ctx, key)
	return v, e
}
func (c *Comments) target(ctx context.Context, key model.Key, id string) (comment.Comment, discussion, error) {
	v, ts, e := c.inventory(ctx, key)
	if e != nil {
		return comment.Comment{}, discussion{}, e
	}
	v0, e := commentutil.Find(v, id)
	return v0, ts[id], e
}
func (c *Comments) Get(ctx context.Context, key model.Key, id string) (comment.Comment, error) {
	v, _, e := c.target(ctx, key, id)
	return v, e
}
func (c *Comments) position(ctx context.Context, key model.Key, a *comment.Anchor) (map[string]any, error) {
	type version struct {
		Head  string `json:"head_commit_sha"`
		Base  string `json:"base_commit_sha"`
		Start string `json:"start_commit_sha"`
	}
	versions, e := commentutil.Pages[version](ctx, c.Connection, c.Connection.PRPath(key.Repository.Path, key.Number)+"/versions")
	if e != nil {
		return nil, e
	}
	var v version
	for _, candidate := range versions {
		if candidate.Head == a.Revision {
			v = candidate
			break
		}
	}
	if v.Base == "" || v.Start == "" {
		return nil, commentutil.Invalid("anchor revision is not an observed MR diff version")
	}
	old := a.OldPath
	if old == "" {
		old = a.Path
	}
	p := map[string]any{"position_type": "file", "new_path": a.Path, "old_path": old, "base_sha": v.Base, "head_sha": v.Head, "start_sha": v.Start}
	if a.Line != nil {
		p["position_type"] = "text"
		lineKey := "new_line"
		if a.Side == "LEFT" {
			lineKey = "old_line"
		}
		p[lineKey] = *a.Line
		if a.StartLine != nil {
			endpoint := func(line int, side string) map[string]any {
				old, new := 0, line
				typ := "new"
				if side == "LEFT" {
					old, new = line, 0
					typ = "old"
				}
				return map[string]any{"line_code": fmt.Sprintf("%x_%d_%d", sha1.Sum([]byte(a.Path)), old, new), "type": typ, typ + "_line": line}
			}
			side := a.StartSide
			if side == "" {
				side = a.Side
			}
			p["line_range"] = map[string]any{"start": endpoint(*a.StartLine, side), "end": endpoint(*a.Line, a.Side)}
		}
	}
	return p, nil
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
	payload := map[string]any{"body": body}
	if a != nil {
		p, e := c.position(ctx, key, a)
		if e != nil {
			return comment.Comment{}, e
		}
		payload["position"] = p
	}
	var d discussion
	e := c.Connection.Request(ctx, "POST", c.Connection.PRPath(key.Repository.Path, key.Number)+"/discussions", payload, &d)
	if e != nil {
		return comment.Comment{}, e
	}
	if d.ID == "" || d.Individual == nil || len(d.Notes) != 1 {
		return comment.Comment{}, transport.UnknownFailure(errors.New("GitLab did not confirm new comment"))
	}
	v, e := d.Notes[0].value(key, &d)
	return commentutil.Confirm(v, body, "", e)
}
func (c *Comments) notePath(key model.Key, id string, d discussion) string {
	base := c.Connection.PRPath(key.Repository.Path, key.Number)
	if !*d.Individual {
		base += "/discussions/" + url.PathEscape(d.ID)
	}
	return base + "/notes/" + url.PathEscape(strings.TrimPrefix(id, "note:"))
}
func (c *Comments) Reply(ctx context.Context, key model.Key, id, body string) (comment.Comment, error) {
	if e := commentutil.Body(body); e != nil {
		return comment.Comment{}, e
	}
	_, d, e := c.target(ctx, key, id)
	if e != nil {
		return comment.Comment{}, e
	}
	var n note
	e = c.Connection.Request(ctx, "POST", c.Connection.PRPath(key.Repository.Path, key.Number)+"/discussions/"+url.PathEscape(d.ID)+"/notes", map[string]any{"body": body}, &n)
	if e != nil {
		return comment.Comment{}, e
	}
	v, e := n.value(key, &d)
	return commentutil.Confirm(v, body, "", e)
}
func (c *Comments) Update(ctx context.Context, key model.Key, id, body string) (comment.Comment, error) {
	if e := commentutil.Body(body); e != nil {
		return comment.Comment{}, e
	}
	_, d, e := c.target(ctx, key, id)
	if e != nil {
		return comment.Comment{}, e
	}
	var n note
	e = c.Connection.Request(ctx, "PUT", c.notePath(key, id, d), map[string]any{"body": body}, &n)
	if e != nil {
		return comment.Comment{}, e
	}
	v, e := n.value(key, &d)
	return commentutil.Confirm(v, body, id, e)
}
func (c *Comments) Delete(ctx context.Context, key model.Key, id string) (comment.DeleteResult, error) {
	out := comment.DeleteResult{PR: key, CommentID: id}
	_, d, e := c.target(ctx, key, id)
	if e != nil {
		return out, e
	}
	notes := []note{}
	for _, n := range d.Notes {
		if !n.System {
			notes = append(notes, n)
		}
	}
	if len(notes) > 1 && "note:"+string(notes[0].ID) == id {
		return out, commentutil.Unsupported("delete replies explicitly before deleting their root")
	}
	e = c.Connection.Request(ctx, "DELETE", c.notePath(key, id, d), nil, nil)
	out.Deleted = e == nil
	return out, e
}
func (c *Comments) Resolve(ctx context.Context, key model.Key, id string, resolved bool) (comment.Comment, error) {
	target, d, e := c.target(ctx, key, id)
	if e != nil {
		return target, e
	}
	if target.Resolution == comment.Unsupported {
		return target, commentutil.Unsupported("comment has no resolution capability")
	}
	desired := comment.Unresolved
	if resolved {
		desired = comment.Resolved
	}
	if target.Resolution == desired {
		return target, nil
	}
	var result discussion
	e = c.Connection.Request(ctx, "PUT", c.Connection.PRPath(key.Repository.Path, key.Number)+"/discussions/"+url.PathEscape(d.ID), map[string]any{"resolved": resolved}, &result)
	if e != nil {
		return target, e
	}
	if result.ID != d.ID || len(result.Notes) == 0 || result.resolution() != desired {
		return target, transport.UnknownFailure(errors.New("GitLab did not confirm resolution"))
	}
	for _, n := range result.Notes {
		if "note:"+string(n.ID) == id {
			v, e := n.value(key, &result)
			if e != nil {
				return v, transport.UnknownFailure(e)
			}
			return v, nil
		}
	}
	return target, transport.UnknownFailure(errors.New("GitLab resolution response omitted comment"))
}
