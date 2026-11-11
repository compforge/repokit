package release

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge/internal/transport"
)

type Service struct{ connection *transport.Client }

// New binds Release operations to the same authenticated transport as other Forge operations.
// Construct through forge.Client.Releases.
func New(connection *transport.Client) *Service { return &Service{connection: connection} }

func phase(phases *[]Phase, name string, fn func() error) error {
	start := time.Now()
	defer func() { *phases = append(*phases, Phase{name, time.Since(start).Seconds()}) }()
	return fn()
}

func problem(code, message string) error {
	return &errs.Error{Code: "forge.release_" + code, Message: message}
}

func (s *Service) validate(repository common.Repository) error {
	if repository.Path == "" || repository != s.connection.Repository(repository.Path) {
		return problem("invalid_argument", "repository must belong to this Forge connection")
	}
	for _, part := range strings.Split(repository.Path, "/") {
		if part == "" || part == "." || part == ".." {
			return problem("invalid_argument", "invalid repository path")
		}
	}
	return nil
}

// Show observes the named Release and independently resolves its current tag commit.
// A successful null Release means absence on an accessible repository, never an access failure.
func (s *Service) Show(ctx context.Context, repository common.Repository, tag string) (result QueryResult, err error) {
	result = QueryResult{Repository: repository, Selection: "tag"}
	defer func() { err = errs.Wrap(err, "forge.release_query_failed", "release.show", repository.Path+"@"+tag) }()
	if err = s.validate(repository); err != nil {
		return
	}
	if !validTag(tag) {
		return result, problem("invalid_argument", "invalid Git tag name")
	}
	err = phase(&result.Phases, "read", func() error {
		result.Release, err = s.read(ctx, repository.Path, tag, false)
		return err
	})
	return
}

// Latest preserves provider selection semantics; it does not compute a maximum version.
func (s *Service) Latest(ctx context.Context, repository common.Repository) (result QueryResult, err error) {
	result = QueryResult{Repository: repository, Selection: "github_latest_published"}
	if s.connection.Provider() == "gitlab" {
		result.Selection = "gitlab_released_at"
	}
	defer func() { err = errs.Wrap(err, "forge.release_query_failed", "release.latest", repository.Path) }()
	if err = s.validate(repository); err != nil {
		return
	}
	err = phase(&result.Phases, "read", func() error {
		result.Release, err = s.read(ctx, repository.Path, "", true)
		return err
	})
	return
}

// Create publishes one ordinary Release at an explicit remote commit/ref.
// +spec=Resolve target once, refuse existing conflicts, write at most once, and confirm Release content plus the live tag commit.
// +why=Forge create endpoints can ignore the requested target when a tag already exists; transport success is insufficient evidence.
// +link=component://docs/forge-releases.md
func (s *Service) Create(ctx context.Context, repository common.Repository, request CreateRequest) (result PublishResult, err error) {
	result = PublishResult{Repository: repository, Tag: request.Tag, RequestedTarget: request.Target}
	defer func() {
		err = errs.Wrap(err, "forge.release_publish_failed", "release.create", repository.Path+"@"+request.Tag)
		if err != nil {
			slog.WarnContext(ctx, "Release publication incomplete", "repository", repository.Path, "tag", request.Tag, "writeAttempted", result.WriteAttempted, "confirmed", result.Confirmed, "errorCode", errorCode(err))
		} else {
			slog.InfoContext(ctx, "Release confirmed", "repository", repository.Path, "tag", request.Tag, "commit", result.TargetCommit, "writeAttempted", result.WriteAttempted)
		}
	}()
	if err = s.validate(repository); err != nil {
		return
	}
	if !validTag(request.Tag) || strings.TrimSpace(request.Target) == "" {
		return result, problem("invalid_argument", "valid tag and explicit target are required")
	}
	if request.Name == "" {
		request.Name = request.Tag
	}
	err = phase(&result.Phases, "prepare", func() error {
		var e error
		result.TargetCommit, e = s.resolveCommit(ctx, repository.Path, request.Target)
		return e
	})
	if err != nil {
		return
	}
	err = phase(&result.Phases, "preflight", func() error {
		var e error
		result.Release, e = s.read(ctx, repository.Path, request.Tag, false)
		if e != nil {
			return e
		}
		if result.Release != nil {
			result.ObservedTagCommit = result.Release.TargetCommit
			return matches(result.Release, request, result.TargetCommit)
		}
		commit, e := s.tagCommit(ctx, repository.Path, request.Tag)
		result.ObservedTagCommit = commit
		if e != nil {
			return e
		}
		if commit != "" && commit != result.TargetCommit {
			return problem("conflict", "existing tag points to a different commit")
		}
		return nil
	})
	if err != nil {
		return
	}
	if result.Release != nil {
		result.Confirmed = true
		return
	}
	if err = ctx.Err(); err != nil {
		return
	}
	// Reserve verification time within the caller's budget; never detach from cancellation.
	writeCtx := ctx
	cancel := func() {}
	if deadline, ok := ctx.Deadline(); ok {
		remaining := time.Until(deadline)
		writeCtx, cancel = context.WithDeadline(ctx, deadline.Add(-min(remaining/5, 10*time.Second)))
	}
	var writeErr error
	_ = phase(&result.Phases, "write", func() error {
		defer cancel()
		result.WriteAttempted = true
		payload := map[string]any{"tag_name": request.Tag, "name": request.Name}
		if s.connection.Provider() == "github" {
			payload["target_commitish"], payload["body"] = result.TargetCommit, request.Notes
			payload["draft"], payload["prerelease"] = false, false
		} else {
			payload["ref"], payload["description"] = result.TargetCommit, request.Notes
		}
		writeErr = s.connection.Request(writeCtx, http.MethodPost, s.connection.RepositoryPath(repository.Path)+"/releases", payload, nil)
		return writeErr
	})
	result.ObservedTagCommit = ""
	verifyErr := phase(&result.Phases, "verify", func() error {
		var e error
		result.Release, e = s.read(ctx, repository.Path, request.Tag, false)
		return e
	})
	if result.Release != nil {
		result.ObservedTagCommit = result.Release.TargetCommit
	}
	if verifyErr == nil && result.Release != nil {
		if err = matches(result.Release, request, result.TargetCommit); err == nil {
			result.Confirmed = true
		}
		return
	}
	// Only a definite rejected request followed by a successful observation can be a definite failure.
	var failure *errs.Error
	if verifyErr == nil && errors.As(writeErr, &failure) && !failure.Uncertain && failure.HTTPStatus >= 400 && failure.HTTPStatus < 500 {
		return result, writeErr
	}
	cause := errors.Join(writeErr, verifyErr)
	return result, &errs.Error{Code: "forge.release_outcome_unknown", Message: "Release publication could not be confirmed", Uncertain: true, Cause: cause,
		Hint: "Run release show for the same repository and tag; compare targetCommit, name and notes before retrying. Writes are never automatically retried."}
}

func matches(release *Release, request CreateRequest, commit string) error {
	if release.TargetCommit != commit || release.Name != request.Name || release.Notes != request.Notes || release.Draft || release.Prerelease || release.Upcoming {
		return problem("conflict", "existing Release differs from the requested commit, name, notes or publication state")
	}
	return nil
}

var objectID = regexp.MustCompile(`^(?:[0-9a-fA-F]{40}|[0-9a-fA-F]{64})$`)

func validTag(tag string) bool {
	if tag == "" || strings.HasPrefix(tag, "/") || strings.HasSuffix(tag, "/") || strings.HasSuffix(tag, ".") || strings.Contains(tag, "..") || strings.Contains(tag, "@{") || strings.ContainsAny(tag, " ~^:?*[\\") {
		return false
	}
	for _, c := range tag {
		if c < 32 || c == 127 {
			return false
		}
	}
	for _, part := range strings.Split(tag, "/") {
		if part == "" || strings.HasPrefix(part, ".") || strings.HasSuffix(part, ".lock") {
			return false
		}
	}
	return true
}

func notFound(err error) bool { var p *errs.Error; return errors.As(err, &p) && p.HTTPStatus == 404 }

// A resource 404 can hide a missing/private repository. Require a live repository read before treating it as absence.
func (s *Service) accessible(ctx context.Context, repository string) error {
	return s.connection.Request(ctx, http.MethodGet, s.connection.RepositoryPath(repository), nil, nil)
}

func (s *Service) resolveCommit(ctx context.Context, repository, ref string) (string, error) {
	path := s.connection.RepositoryPath(repository)
	if s.connection.Provider() == "github" {
		path += "/commits/"
	} else {
		path += "/repository/commits/"
	}
	var wire struct {
		SHA string `json:"sha"`
		ID  string `json:"id"`
	}
	if err := s.connection.Request(ctx, http.MethodGet, path+url.PathEscape(ref), nil, &wire); err != nil {
		return "", err
	}
	commit := wire.SHA
	if s.connection.Provider() == "gitlab" {
		commit = wire.ID
	}
	if !objectID.MatchString(commit) {
		return "", problem("invalid_response", "Forge did not return a full commit ID")
	}
	return strings.ToLower(commit), nil
}

func errorCode(err error) string {
	var failure *errs.Error
	if errors.As(err, &failure) {
		return failure.Code
	}
	return "forge.release_publish_failed"
}
