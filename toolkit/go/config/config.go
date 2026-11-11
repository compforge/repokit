// Package config loads RepoKit user configuration and resolves host-scoped Forge access.
// Repository state and caller workflow policy remain outside this package.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/compforge/repokit/toolkit/go/errs"
	"github.com/compforge/repokit/toolkit/go/forge"
	"github.com/compforge/repokit/toolkit/go/state"
)

type entry struct {
	Provider string `json:"provider"`
	APIHost  string `json:"apiHost"`
	Token    string `json:"token"`
}

// Config is a read-only snapshot of one RepoKit home's config.json.
// Load once per operation and reuse it across repositories. Credentials are never
// included in JSON, formatted output or logs.
type Config struct {
	Home   string `json:"home"`
	forges map[string]entry
}

func (c Config) String() string   { return fmt.Sprintf("RepoKit config (%s)", c.Home) }
func (c Config) GoString() string { return c.String() }

// Load does not create files or read application-owned configuration.
// +spec=`Missing config is empty. Invalid or non-private config fails without exposing its contents.`
// +link=component://docs/configuration.md
func Load(home string) (*Config, error) {
	root, err := state.ResolveHome(home)
	if err != nil {
		return nil, err
	}
	c := &Config{Home: root, forges: map[string]entry{}}
	path := filepath.Join(root, "config.json")
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, nil
	}
	if err != nil {
		return nil, errs.Wrap(err, "config.read_failed", "config.load", path)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, errs.Wrap(err, "config.read_failed", "config.load", path)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, &errs.Error{Code: "config.invalid_permissions", Op: "config.load", Subject: path, Message: "RepoKit config must be a private regular file (chmod 600)", Hint: "Restrict config.json to its owner."}
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return nil, errs.Wrap(err, "config.read_failed", "config.load", path)
	}
	var document struct {
		Forges map[string]entry `json:"forges"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil || !bytes.HasPrefix(bytes.TrimSpace(data), []byte("{")) {
		return nil, invalid(path, "expected a JSON object with a forges map")
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return nil, invalid(path, "expected one JSON object")
	}
	for host, e := range document.Forges {
		normalized, err := normalizeHost(host)
		if err != nil {
			return nil, invalid(path, "invalid Forge host key")
		}
		if _, exists := c.forges[normalized]; exists {
			return nil, invalid(path, "duplicate normalized Forge host")
		}
		if e.Provider != "" && e.Provider != "github" && e.Provider != "gitlab" {
			return nil, invalid(path, "provider must be github or gitlab")
		}
		if e.APIHost != "" {
			e.APIHost, err = normalizeHost(e.APIHost)
			if err != nil {
				return nil, invalid(path, "apiHost must be a host, optionally with a port")
			}
		}
		c.forges[normalized] = e
	}
	slog.Debug("RepoKit configuration loaded", "path", path, "forges", len(c.forges))
	return c, nil
}
func invalid(path, reason string) error {
	return &errs.Error{Code: "config.invalid", Op: "config.load", Subject: path, Message: "Invalid RepoKit config: " + reason}
}
func normalizeHost(host string) (string, error) {
	host = strings.ToLower(host)
	switch host {
	case "github":
		host = "github.com"
	case "gitlab":
		host = "gitlab.com"
	}
	parsed, err := url.Parse("https://" + host)
	if err != nil || host == "" || strings.TrimSpace(host) != host || parsed.Host != host || parsed.Hostname() == "" || parsed.User != nil || strings.ContainsAny(host, "/@?#\\") {
		return "", errors.New("invalid Forge host")
	}
	return host, nil
}

// Forge resolves access for a repository's Forge identity; it performs no network I/O.
// +spec=`Credentials come only from the selected host in config.json. Environment variables and caller configuration never supply or override credentials.`
// +link=component://docs/configuration.md
func (c *Config) Forge(name string) (forge.Config, error) {
	host, err := normalizeHost(name)
	if err != nil {
		return forge.Config{}, errs.Wrap(err, "forge.invalid_host", "config.forge", "")
	}
	e := c.forges[host]
	provider := e.Provider
	if provider == "" {
		switch host {
		case "github.com":
			provider = "github"
		case "gitlab.com":
			provider = "gitlab"
		}
	}
	if provider == "" {
		return forge.Config{}, &errs.Error{Code: "forge.provider_required", Op: "config.forge", Subject: host, Message: "Configure a provider for Forge " + host + " in " + filepath.Join(c.Home, "config.json")}
	}
	token := strings.TrimSpace(e.Token)
	if token == "" {
		return forge.Config{}, &errs.Error{Code: "forge.authentication_required", Op: "config.forge", Subject: host, Message: fmt.Sprintf("no %s credential for %s; configure %s", provider, host, filepath.Join(c.Home, "config.json"))}
	}
	apiHost := ""
	if e.APIHost != "" {
		apiHost = "https://" + e.APIHost + "/api/v4"
		if provider == "github" {
			apiHost = "https://" + e.APIHost + "/api/v3"
			if e.APIHost == "github.com" {
				apiHost = "https://api.github.com"
			}
		}
	}
	slog.Debug("Forge access resolved", "host", host, "provider", provider)
	return forge.Config{Host: host, Provider: provider, APIHost: apiHost, Token: token, Home: c.Home}, nil
}
