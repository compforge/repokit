package discussion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
	"github.com/compforge/repokit/toolkit/go/forge/model"
)

type githubComment struct {
	ID   json.Number `json:"id"`
	Body *string     `json:"body"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
	CreatedAt    string      `json:"created_at"`
	URL          string      `json:"html_url"`
	Path         string      `json:"path"`
	Line         *int        `json:"line"`
	OriginalLine *int        `json:"original_line"`
	Parent       json.Number `json:"in_reply_to_id"`
}

func (w githubComment) comment() (Comment, error) {
	if w.ID == "" || w.Body == nil {
		return Comment{}, errors.New("invalid GitHub comment response")
	}
	line := w.Line
	if line == nil {
		line = w.OriginalLine
	}
	return Comment{ID: string(w.ID), Body: *w.Body, Author: w.User.Login, CreatedAt: w.CreatedAt, URL: w.URL, Path: w.Path, Line: line}, nil
}

func (c *Service) graphql(ctx context.Context, query string, variables map[string]any, dest any, write bool) error {
	var response struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	endpoint := strings.TrimSuffix(c.connection.Base(), "/v3") + "/graphql"
	if err := c.connection.RequestURL(ctx, http.MethodPost, endpoint, map[string]any{"query": query, "variables": variables}, &response, write); err != nil {
		return err
	}
	var err error
	if len(response.Errors) > 0 {
		err = &errs.Error{Code: "forge.graphql_failed", Message: "GitHub GraphQL returned errors; result is incomplete"}
	} else if len(response.Data) == 0 || string(response.Data) == "null" {
		err = errors.New("GitHub GraphQL response has no data")
	} else {
		err = json.Unmarshal(response.Data, dest)
	}
	if err != nil && write {
		return transport.UnknownFailure(err)
	}
	return err
}

type githubThread struct {
	ID       string `json:"id"`
	Resolved *bool  `json:"isResolved"`
	Comments *struct {
		Nodes []struct {
			ID json.Number `json:"fullDatabaseId"`
		} `json:"nodes"`
	} `json:"comments"`
}

func (c *Service) githubThreads(ctx context.Context, key model.Key) ([]githubThread, error) {
	owner, name, ok := strings.Cut(key.Repository.Path, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, errors.New("invalid GitHub repository path")
	}
	const query = `query ReviewThreads($owner: String!, $name: String!, $number: Int!, $cursor: String) {
 repository(owner: $owner, name: $name) { pullRequest(number: $number) {
 reviewThreads(first: 100, after: $cursor) { nodes { id isResolved comments(first: 1) { nodes { fullDatabaseId } } }
 pageInfo { hasNextPage endCursor } } } } }`
	result := []githubThread{}
	var cursor *string
	seen := map[string]bool{}
	for page := 0; page < 100; page++ {
		var data struct {
			Repository *struct {
				PR *struct {
					Threads *struct {
						Nodes    []githubThread `json:"nodes"`
						PageInfo *struct {
							Next   bool    `json:"hasNextPage"`
							Cursor *string `json:"endCursor"`
						} `json:"pageInfo"`
					} `json:"reviewThreads"`
				} `json:"pullRequest"`
			} `json:"repository"`
		}
		if err := c.graphql(ctx, query, map[string]any{"owner": owner, "name": name, "number": key.Number, "cursor": cursor}, &data, false); err != nil {
			return nil, err
		}
		if data.Repository == nil || data.Repository.PR == nil {
			return nil, &errs.Error{Code: "forge.not_found", Message: "PR not found in GitHub GraphQL response"}
		}
		connection := data.Repository.PR.Threads
		if connection == nil || connection.Nodes == nil || connection.PageInfo == nil {
			return nil, errors.New("incomplete GitHub thread connection")
		}
		for _, thread := range connection.Nodes {
			if thread.ID == "" || thread.Resolved == nil || thread.Comments == nil || len(thread.Comments.Nodes) != 1 || thread.Comments.Nodes[0].ID == "" {
				return nil, errors.New("invalid GitHub review thread")
			}
			result = append(result, thread)
		}
		if !connection.PageInfo.Next {
			return result, nil
		}
		cursor = connection.PageInfo.Cursor
		if cursor == nil || *cursor == "" || seen[*cursor] {
			return nil, errors.New("invalid GitHub thread pagination cursor")
		}
		seen[*cursor] = true
	}
	return nil, errors.New("GitHub thread list exceeds 100 pages")
}

func (c *Service) githubCommentPages(ctx context.Context, path string) ([]githubComment, error) {
	result := []githubComment{}
	for page := 1; page <= 100; page++ {
		var rows []githubComment
		if err := c.connection.Request(ctx, "GET", path+"?per_page=100&page="+strconv.Itoa(page), nil, &rows); err != nil {
			return nil, err
		}
		if rows == nil {
			return nil, errors.New("invalid GitHub comment list")
		}
		result = append(result, rows...)
		if len(rows) < 100 {
			return result, nil
		}
	}
	return nil, errors.New("GitHub comment list exceeds 10000 items")
}

func (c *Service) githubList(ctx context.Context, key model.Key) ([]Discussion, error) {
	threads, err := c.githubThreads(ctx, key)
	if err != nil {
		return nil, err
	}
	issue, err := c.githubCommentPages(ctx, c.connection.RepositoryPath(key.Repository.Path)+"/issues/"+strconv.Itoa(key.Number)+"/comments")
	if err != nil {
		return nil, err
	}
	review, err := c.githubCommentPages(ctx, c.connection.PRPath(key.Repository.Path, key.Number)+"/comments")
	if err != nil {
		return nil, err
	}
	result := []Discussion{}
	for _, row := range issue {
		comment, err := row.comment()
		if err != nil {
			return nil, err
		}
		result = append(result, Discussion{PullRequest: key, ID: "issue:" + comment.ID, Comments: []Comment{comment}, Resolution: "not_applicable"})
	}
	roots := map[string]Comment{}
	replies := map[string][]Comment{}
	for _, row := range review {
		comment, err := row.comment()
		if err != nil {
			return nil, err
		}
		if row.Parent != "" {
			replies[string(row.Parent)] = append(replies[string(row.Parent)], comment)
		} else {
			roots[comment.ID] = comment
		}
	}
	for _, thread := range threads {
		rootID := string(thread.Comments.Nodes[0].ID)
		root, ok := roots[rootID]
		if !ok {
			return nil, fmt.Errorf("review thread %s root missing from comment observation; refresh required", thread.ID)
		}
		delete(roots, rootID)
		comments := append([]Comment{root}, replies[rootID]...)
		delete(replies, rootID)
		resolution := "unresolved"
		if *thread.Resolved {
			resolution = "resolved"
		}
		result = append(result, Discussion{PullRequest: key, ID: thread.ID, Comments: comments, Replyable: true, Resolvable: true, Resolution: resolution})
	}
	if len(roots) > 0 || len(replies) > 0 {
		return nil, errors.New("review comments and thread observation disagree; refresh required")
	}
	sort.SliceStable(result, func(i, j int) bool { return result[i].Comments[0].CreatedAt < result[j].Comments[0].CreatedAt })
	return result, nil
}
