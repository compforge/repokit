package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
	"github.com/compforge/repokit/toolkit/go/forge/model"
)

func (c *Comments) graphql(ctx context.Context, query string, variables map[string]any, dest any, write bool) error {
	var response struct {
		Data   json.RawMessage   `json:"data"`
		Errors []json.RawMessage `json:"errors"`
	}
	endpoint := strings.TrimSuffix(c.Connection.Base(), "/v3") + "/graphql"
	if err := c.Connection.RequestURL(ctx, http.MethodPost, endpoint, map[string]any{"query": query, "variables": variables}, &response, write); err != nil {
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
	ID         string                 `json:"id"`
	Outdated   *bool                  `json:"isOutdated"`
	ResolvedBy struct{ Login string } `json:"resolvedBy"`
	Resolved   *bool                  `json:"isResolved"`
	Comments   *struct {
		Nodes []struct {
			ID json.Number `json:"fullDatabaseId"`
		} `json:"nodes"`
	} `json:"comments"`
}

func (c *Comments) githubThreads(ctx context.Context, key model.Key) ([]githubThread, error) {
	owner, name, ok := strings.Cut(key.Repository.Path, "/")
	if !ok || owner == "" || name == "" || strings.Contains(name, "/") {
		return nil, errors.New("invalid GitHub repository path")
	}
	const query = `query ReviewThreads($owner: String!, $name: String!, $number: Int!, $cursor: String) {
 repository(owner: $owner, name: $name) { pullRequest(number: $number) {
 reviewThreads(first: 100, after: $cursor) { nodes { id isResolved isOutdated resolvedBy { login } comments(first: 1) { nodes { fullDatabaseId } } }
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
