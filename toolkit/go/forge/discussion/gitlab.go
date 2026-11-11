package discussion

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/compforge/repokit/toolkit/go/forge/model"
)

type gitlabNote struct {
	ID     json.Number `json:"id"`
	Body   *string     `json:"body"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	CreatedAt  string `json:"created_at"`
	System     bool   `json:"system"`
	Resolvable *bool  `json:"resolvable"`
	Resolved   *bool  `json:"resolved"`
	Position   *struct {
		NewPath string `json:"new_path"`
		OldPath string `json:"old_path"`
		NewLine *int   `json:"new_line"`
		OldLine *int   `json:"old_line"`
	} `json:"position"`
}

func (w gitlabNote) comment() (Comment, error) {
	if w.ID == "" || w.Body == nil {
		return Comment{}, errors.New("invalid GitLab note response")
	}
	result := Comment{ID: string(w.ID), Body: *w.Body, Author: w.Author.Username, CreatedAt: w.CreatedAt}
	if w.Position != nil {
		result.Path = w.Position.NewPath
		if result.Path == "" {
			result.Path = w.Position.OldPath
		}
		result.Line = w.Position.NewLine
		if result.Line == nil {
			result.Line = w.Position.OldLine
		}
	}
	return result, nil
}

type gitlabDiscussion struct {
	ID         string       `json:"id"`
	Individual *bool        `json:"individual_note"`
	Notes      []gitlabNote `json:"notes"`
}

func (w gitlabDiscussion) discussion(key model.Key) (Discussion, error) {
	if w.ID == "" || w.Individual == nil || len(w.Notes) == 0 {
		return Discussion{}, errors.New("invalid GitLab discussion response")
	}
	result := Discussion{PullRequest: key, ID: w.ID, Comments: []Comment{}, Replyable: !*w.Individual, Resolvable: new(false), Resolution: "not_applicable"}
	allResolved, unresolved := true, false
	canResolve, unknownCapability := false, false
	for _, note := range w.Notes {
		if note.System {
			continue
		}
		comment, err := note.comment()
		if err != nil {
			return Discussion{}, err
		}
		result.Comments = append(result.Comments, comment)
		if *w.Individual || note.Resolvable != nil && !*note.Resolvable {
			continue
		}
		// +why=Self-managed GitLab may omit capability and state independently; preserve both gaps.
		if note.Resolvable == nil {
			unknownCapability = true
		} else {
			canResolve = true
		}
		allResolved = allResolved && note.Resolved != nil && *note.Resolved
		unresolved = unresolved || note.Resolved != nil && !*note.Resolved
	}
	if canResolve {
		result.Resolvable = new(true)
	} else if unknownCapability {
		result.Resolvable = nil
	}
	if canResolve || unknownCapability {
		switch {
		case unresolved:
			result.Resolution = "unresolved"
		case allResolved:
			result.Resolution = "resolved"
		default:
			result.Resolution = "unknown"
		}
	}
	return result, nil
}
func (c *Service) gitlabList(ctx context.Context, key model.Key) ([]Discussion, error) {
	result := []Discussion{}
	for page := 1; page <= 100; page++ {
		var rows []gitlabDiscussion
		err := c.connection.Request(ctx, "GET", c.connection.PRPath(key.Repository.Path, key.Number)+"/discussions?per_page=100&page="+strconv.Itoa(page), nil, &rows)
		if err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, errors.New("invalid GitLab discussion list")
		}
		for _, row := range rows {
			thread, err := row.discussion(key)
			if err != nil {
				return nil, err
			}
			if len(thread.Comments) > 0 {
				result = append(result, thread)
			}
		}
		if len(rows) < 100 {
			return result, nil
		}
	}
	return nil, errors.New("GitLab discussion list exceeds 10000 items")
}
