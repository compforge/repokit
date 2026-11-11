package config_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go/config"
)

// +case=One host-scoped configuration contract is shared by native Go, Python and TypeScript consumers.
func TestForgeConfigContract(t *testing.T) {
	var cases []struct {
		Name     string
		Config   json.RawMessage
		Env      map[string]string
		Host     string
		Expected struct{ Host, Provider, Token, APIHost string }
		Error    string
	}
	data, err := os.ReadFile("../../../conformance/config/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(data, &cases); err != nil {
		t.Fatal(err)
	}
	for _, row := range cases {
		t.Run(row.Name, func(t *testing.T) {
			for _, name := range []string{"GH_TOKEN", "GITHUB_TOKEN", "GITLAB_TOKEN", "FIXTURE_FORGE_TOKEN"} {
				t.Setenv(name, "")
			}
			for k, v := range row.Env {
				t.Setenv(k, v)
			}
			home := t.TempDir()
			if row.Config != nil {
				if err := os.WriteFile(filepath.Join(home, "config.json"), row.Config, 0600); err != nil {
					t.Fatal(err)
				}
			}
			c, err := config.Load(home)
			if err == nil {
				dumped, _ := json.Marshal(c)
				if strings.Contains(string(dumped)+fmt.Sprintf("%v %+v %#v", c, c, c), "secret-canary") {
					t.Fatal("config exposed credential")
				}
				access, e := c.Forge(row.Host)
				err = e
				if e == nil {
					want := row.Expected
					endpoint := ""
					if want.APIHost != "" {
						endpoint = "https://" + want.APIHost + "/api/v4"
					}
					if access.Host != want.Host || access.Provider != want.Provider || access.Token != want.Token || access.APIHost != endpoint || access.Home != home {
						t.Fatal("resolved access differs from contract")
					}
				}
			}
			if row.Error != "" {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), row.Error) || strings.Contains(err.Error(), "secret-canary") {
					t.Fatal("wrong or unsafe error", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestConfigHomePermissionsAndSnapshot(t *testing.T) {
	home := filepath.Join(t.TempDir(), "not-created")
	t.Setenv("REPOKIT_HOME", home)
	c, err := config.Load("")
	if err != nil || c.Home != home {
		t.Fatal(c, err)
	}
	if _, err = os.Stat(home); !os.IsNotExist(err) {
		t.Fatal("load created home")
	}
	home = t.TempDir()
	path := filepath.Join(home, "config.json")
	os.WriteFile(path, []byte(`{"forges":{"github.com":{"token":"secret-canary"}}}`), 0600)
	c, err = config.Load(home)
	if err != nil {
		t.Fatal(err)
	}
	data, _ := json.Marshal(c)
	if strings.Contains(string(data)+fmt.Sprintf("%v %+v %#v", c, c, c), "secret-canary") {
		t.Fatal("secret exposed")
	}
	os.WriteFile(path, []byte(`{"token":"secret-canary",`), 0600)
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GITHUB_TOKEN", "")
	access, err := c.Forge("github")
	if err != nil || access.Token != "secret-canary" {
		t.Fatal("config was re-read", err)
	}
	if _, err = config.Load(home); err == nil || strings.Contains(err.Error(), "secret-canary") {
		t.Fatal("malformed config leaked", err)
	}
	os.Chmod(path, 0644)
	if _, err = config.Load(home); err == nil || !strings.Contains(err.Error(), "chmod 600") {
		t.Fatal(err)
	}
}
