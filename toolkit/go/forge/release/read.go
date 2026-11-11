package release

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type wireRelease struct {
	Tag         string `json:"tag_name"`
	Name        string `json:"name"`
	Body        string `json:"body"`
	Description string `json:"description"`
	HTMLURL     string `json:"html_url"`
	PublishedAt string `json:"published_at"`
	ReleasedAt  string `json:"released_at"`
	Draft       *bool  `json:"draft"`
	Prerelease  *bool  `json:"prerelease"`
	Upcoming    bool   `json:"upcoming_release"`
	Links       struct {
		Self string `json:"self"`
	} `json:"_links"`
}

func (s *Service) read(ctx context.Context, repository, tag string, latest bool) (*Release, error) {
	base := s.connection.RepositoryPath(repository)
	path := base + "/releases/tags/" + url.PathEscape(tag)
	if s.connection.Provider() == "gitlab" {
		path = base + "/releases/" + url.PathEscape(tag)
	}
	if latest {
		path = base + "/releases/latest"
		if s.connection.Provider() == "gitlab" {
			path = base + "/releases/permalink/latest"
		}
	}
	var wire wireRelease
	if err := s.connection.Request(ctx, http.MethodGet, path, nil, &wire); err != nil {
		if notFound(err) {
			return nil, s.accessible(ctx, repository)
		}
		return nil, err
	}
	if !validTag(wire.Tag) || !latest && wire.Tag != tag {
		return nil, problem("invalid_response", "Release response has an unexpected tag")
	}
	result := &Release{Tag: wire.Tag, Name: wire.Name, Notes: wire.Body, URL: wire.HTMLURL, PublishedAt: wire.PublishedAt}
	if s.connection.Provider() == "gitlab" {
		result.Notes, result.URL, result.PublishedAt = wire.Description, wire.Links.Self, wire.ReleasedAt
		releasedAt, err := time.Parse(time.RFC3339, wire.ReleasedAt)
		if err != nil {
			return result, problem("invalid_response", "Release has no valid released_at timestamp")
		}
		result.Upcoming = wire.Upcoming || releasedAt.After(time.Now())
	} else {
		if wire.Draft == nil || wire.Prerelease == nil {
			return result, problem("invalid_response", "Release publication state is missing")
		}
		result.Draft, result.Prerelease = *wire.Draft, *wire.Prerelease
	}
	commit, err := s.tagCommit(ctx, repository, wire.Tag)
	if err != nil {
		return result, err
	}
	if commit == "" {
		return result, problem("invalid_response", "Release tag is missing")
	}
	result.TargetCommit = commit

	return result, nil
}

// tagCommit reads the exact tag namespace, peeling annotated GitHub tags with a bounded depth.
func (s *Service) tagCommit(ctx context.Context, repository, tag string) (string, error) {
	base := s.connection.RepositoryPath(repository)
	if s.connection.Provider() == "gitlab" {
		var wire struct {
			Name   string `json:"name"`
			Commit struct {
				ID string `json:"id"`
			} `json:"commit"`
		}
		err := s.connection.Request(ctx, http.MethodGet, base+"/repository/tags/"+url.PathEscape(tag), nil, &wire)
		if notFound(err) {
			return "", s.accessible(ctx, repository)
		}
		if err != nil {
			return "", err
		}
		if wire.Name != tag || !objectID.MatchString(wire.Commit.ID) {
			return "", problem("invalid_response", "tag did not resolve to a commit")
		}
		return strings.ToLower(wire.Commit.ID), nil
	}
	var wire struct {
		Ref    string `json:"ref"`
		Object struct {
			Type string `json:"type"`
			SHA  string `json:"sha"`
		} `json:"object"`
	}
	err := s.connection.Request(ctx, http.MethodGet, base+"/git/ref/tags/"+url.PathEscape(tag), nil, &wire)
	if notFound(err) {
		return "", s.accessible(ctx, repository)
	}
	if err != nil {
		return "", err
	}
	if wire.Ref != "refs/tags/"+tag {
		return "", problem("invalid_response", "tag reference identity mismatch")
	}
	for depth := 0; depth < 16; depth++ {
		if !objectID.MatchString(wire.Object.SHA) {
			return "", problem("invalid_response", "invalid tag object ID")
		}
		if wire.Object.Type == "commit" {
			return strings.ToLower(wire.Object.SHA), nil
		}
		if wire.Object.Type != "tag" {
			return "", problem("conflict", "tag points to a non-commit object")
		}
		id := wire.Object.SHA
		wire.Object.Type, wire.Object.SHA = "", ""
		if err := s.connection.Request(ctx, http.MethodGet, base+"/git/tags/"+id, nil, &wire); err != nil {
			return "", err
		}
	}
	return "", problem("invalid_response", "annotated tag nesting exceeds 16 objects")
}
