package forge

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
)

type gitlabNote struct {
	ID     json.Number `json:"id"`
	Body   *string     `json:"body"`
	Author struct {
		Username string `json:"username"`
	} `json:"author"`
	CreatedAt  string `json:"created_at"`
	System     bool   `json:"system"`
	Resolvable bool   `json:"resolvable"`
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

func (w gitlabDiscussion) discussion(key Key) (Discussion, error) {
	if w.ID == "" || w.Individual == nil || len(w.Notes) == 0 {
		return Discussion{}, errors.New("invalid GitLab discussion response")
	}
	result := Discussion{PullRequest: key, ID: w.ID, Comments: []Comment{}, Replyable: !*w.Individual, Resolution: "not_applicable"}
	allResolved := true
	for _, note := range w.Notes {
		if note.System {
			continue
		}
		comment, err := note.comment()
		if err != nil {
			return Discussion{}, err
		}
		result.Comments = append(result.Comments, comment)
		if note.Resolvable {
			if note.Resolved == nil {
				return Discussion{}, errors.New("missing GitLab note resolution")
			}
			result.Resolvable = true
			allResolved = allResolved && *note.Resolved
		}
	}
	if result.Resolvable {
		result.Resolution = "unresolved"
		if allResolved {
			result.Resolution = "resolved"
		}
	}
	return result, nil
}
func (c *Client) gitlabDiscussions(ctx context.Context, key Key) ([]Discussion, error) {
	result := []Discussion{}
	for page := 1; page <= 100; page++ {
		var rows []gitlabDiscussion
		err := c.request(ctx, "GET", c.prPath(key.Repository.Path, key.Number)+"/discussions?per_page=100&page="+strconv.Itoa(page), nil, &rows)
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
