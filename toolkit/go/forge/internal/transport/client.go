// Package transport owns Forge HTTP requests, response bounds and connection cooldown.
package transport

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/compforge/quality-harness/sdks/go/common"
	"github.com/compforge/repokit/toolkit/go/errs"
)

type Client struct {
	host, provider, token, base string
	http                        *http.Client
	mu                          sync.Mutex
	cooldown                    time.Time
}

func New(host, provider, token, base string, client *http.Client) *Client {
	return &Client{host: host, provider: provider, token: token, base: base, http: client}
}
func (c *Client) Provider() string { return c.provider }
func (c *Client) Base() string     { return c.base }

func (c *Client) Repository(path string) common.Repository {
	name := c.host
	if name == "github.com" {
		name = "github"
	}
	if name == "gitlab.com" {
		name = "gitlab"
	}
	return common.Repository{Forge: common.Forge{Name: name}, Path: path}
}

func (c *Client) Request(ctx context.Context, method, path string, payload any, dest any) (err error) {
	return c.RequestURL(ctx, method, c.base+path, payload, dest, method != "GET")
}

// RequestURL shares authentication, limits and cooldown with GraphQL queries and mutations.
func (c *Client) RequestURL(ctx context.Context, method, endpoint string, payload any, dest any, write bool) (err error) {
	defer func() { err = errs.Wrap(err, "forge.request_failed", method, endpoint) }()
	c.mu.Lock()
	defer c.mu.Unlock()
	if time.Now().Before(c.cooldown) {
		return HTTPFailure(429, &c.cooldown)
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	if c.token != "" {
		if c.provider == "github" {
			req.Header.Set("Authorization", "Bearer "+c.token)
		} else {
			req.Header.Set("PRIVATE-TOKEN", c.token)
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	response, err := c.http.Do(req)
	if err != nil {
		if write {
			return UnknownFailure(err)
		}
		return err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 16*1024*1024+1))
	if err == nil && len(data) > 16*1024*1024 {
		err = errors.New("Forge response exceeds 16 MiB")
	}
	if err != nil {
		if write {
			return UnknownFailure(err)
		}
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		httpErr := HTTPFailure(response.StatusCode, nil)
		if response.StatusCode == 429 || response.StatusCode == 403 && (response.Header.Get("Retry-After") != "" || response.Header.Get("X-RateLimit-Remaining") == "0") {
			c.cooldown = time.Now().Add(30 * time.Second)
			if seconds, err := strconv.Atoi(response.Header.Get("Retry-After")); err == nil {
				c.cooldown = time.Now().Add(time.Duration(max(1, seconds)) * time.Second)
			} else if at, err := http.ParseTime(response.Header.Get("Retry-After")); err == nil {
				c.cooldown = at
			}
			if epoch, err := strconv.ParseInt(response.Header.Get("X-RateLimit-Reset"), 10, 64); err == nil && time.Unix(epoch, 0).After(c.cooldown) {
				c.cooldown = time.Unix(epoch, 0)
			}
			retryAt := c.cooldown
			httpErr.RetryAt = &retryAt
		}
		if write && (response.StatusCode >= 500 || response.StatusCode >= 300 && response.StatusCode < 400) {
			return UnknownFailure(httpErr)
		}
		return httpErr
	}
	if dest != nil {
		if err = json.Unmarshal(data, dest); err != nil {
			if write {
				return UnknownFailure(err)
			}
			return err
		}
	}
	return nil
}

func (c *Client) RepositoryPath(repository string) string {
	if c.provider == "gitlab" {
		return "/projects/" + url.PathEscape(repository)
	}
	parts := strings.Split(repository, "/")
	for i := range parts {
		parts[i] = url.PathEscape(parts[i])
	}
	return "/repos/" + strings.Join(parts, "/")
}
func (c *Client) PRPath(repository string, number int) string {
	kind := "/pulls"
	if c.provider == "gitlab" {
		kind = "/merge_requests"
	}
	path := c.RepositoryPath(repository) + kind
	if number > 0 {
		path += "/" + strconv.Itoa(number)
	}
	return path
}
