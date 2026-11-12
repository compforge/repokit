// Package config loads RepoKit user configuration and resolves host-scoped Forge access.
// Repository state and caller workflow policy remain outside this package.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
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
	var document map[string]json.RawMessage
	if err := json.Unmarshal(data, &document); err != nil || document == nil {
		return nil, invalid(path, "expected a JSON object with a forges map")
	}
	for key := range document {
		if key != "forges" {
			return nil, invalid(path, "unknown configuration field")
		}
	}
	var forges map[string]json.RawMessage
	if raw, exists := document["forges"]; exists {
		if err := json.Unmarshal(raw, &forges); err != nil {
			return nil, invalid(path, "expected a forges map")
		}
	}
	for host, raw := range forges {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil {
			return nil, invalid(path, "expected Forge configuration fields")
		}
		for key := range fields {
			if key != "provider" && key != "apiHost" && key != "token" {
				return nil, invalid(path, "unknown Forge configuration field")
			}
		}
		var e entry
		if err := json.Unmarshal(raw, &e); err != nil {
			return nil, invalid(path, "Forge configuration fields must be strings")
		}

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

var hostLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)
var decimalPort = regexp.MustCompile(`^[0-9]{1,5}$`)

// Host syntax is shared across SDKs rather than inherited from differing URL parsers.
func normalizeHost(host string) (string, error) {
	host = strings.ToLower(host)
	switch host {
	case "github":
		host = "github.com"
	case "gitlab":
		host = "gitlab.com"
	}
	invalidHost := errors.New("invalid Forge host")
	name, port := host, ""
	hasPort := false
	if strings.HasPrefix(host, "[") {
		end := strings.IndexByte(host, ']')
		if end < 0 {
			return "", invalidHost
		}
		name = host[1:end]
		if !strings.Contains(name, ":") || net.ParseIP(name) == nil {
			return "", invalidHost
		}
		suffix := host[end+1:]
		if suffix != "" {
			if !strings.HasPrefix(suffix, ":") {
				return "", invalidHost
			}
			port, hasPort = suffix[1:], true
		}
	} else {
		name, port, hasPort = strings.Cut(host, ":")
		if len(name) > 253 {
			return "", invalidHost
		}
		numeric := true
		for _, ch := range name {
			if (ch < '0' || ch > '9') && ch != '.' {
				numeric = false
			}
		}
		if numeric && net.ParseIP(name) == nil {
			return "", invalidHost
		}
		for _, label := range strings.Split(name, ".") {
			if !hostLabel.MatchString(label) {
				return "", invalidHost
			}
		}
	}
	if hasPort {
		n, err := strconv.Atoi(port)
		if !decimalPort.MatchString(port) || err != nil || n < 1 || n > 65535 {
			return "", invalidHost
		}
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
