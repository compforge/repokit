package validation

import (
	"encoding/json"
	"github.com/compforge/repokit/toolkit/go/internal/analysis"
	"github.com/compforge/repokit/toolkit/go/internal/git"
	"reflect"
	"testing"
)

// +case=Only captured file aliases establish reusable content dependencies; lexical normalization cannot bypass missing entries, directory aliases, or opaque gitlink boundaries.
func TestCapturedFileLinkInputs(t *testing.T) {
	for _, tc := range []struct {
		target string
		want   []string
	}{
		{"file", []string{"alias", "file"}},
		{"chain", []string{"alias", "chain", "file"}},
		{"dir/../file", []string{"alias", "file"}},
		{"./dir/data", []string{"alias", "dir/data"}},
		{"opaque", []string{"alias", "opaque"}},
		{"dir", nil}, {"dir/", nil}, {"file/", nil},
		{"file/../file", nil}, {"missing/../file", nil},
		{"diralias/data", nil}, {"diralias/../file", nil},
		{"module", nil}, {"module/file", nil},
		{"/file", nil}, {"../file", nil}, {"missing", nil},
		{"alias", nil}, {"cycle", nil}, {"", nil},
	} {
		t.Run(tc.target, func(t *testing.T) {
			files := map[string]inputEntry{"file": {Kind: "file"}, "dir/data": {Kind: "file"}, "opaque": {Kind: "opaque"}, "module": {Kind: "gitlink"}}
			links := map[string]string{"alias": tc.target, "chain": "file", "cycle": "alias", "diralias": "dir"}
			for name := range links {
				files[name] = inputEntry{Kind: "symlink"}
			}
			if got := capturedLinkInputs(files, links)["alias"]; !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v want %v", got, tc.want)
			}
		})
	}
}

// +case=Version exemptions describe ordinary unreferenced contents, never link payloads or linked VERSION contents, and survive history serialization.
func TestCapturedVersionLinkIdentity(t *testing.T) {
	for _, tc := range []struct {
		name     string
		snapshot git.Snapshot
		ignored  bool
	}{
		{"plain", git.Snapshot{Files: map[string][]byte{"VERSION": []byte("one")}}, true},
		{"referenced", git.Snapshot{Files: map[string][]byte{"VERSION": []byte("one")}, Links: map[string]string{"alias": "VERSION"}}, false},
		{"symlink", git.Snapshot{Files: map[string][]byte{"target": []byte("one")}, Links: map[string]string{"VERSION": "target"}}, false},
		{"gitlink", git.Snapshot{Modules: map[string]string{"VERSION": "commit"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := captureInputs(&analysis.CapturedSnapshot{Contents: tc.snapshot})
			data, err := json.Marshal(manifest)
			if err != nil {
				t.Fatal(err)
			}
			var restored inputManifest
			if err = json.Unmarshal(data, &restored); err != nil {
				t.Fatal(err)
			}
			if restored.Files["VERSION"].Version != tc.ignored {
				t.Fatal(restored.Files)
			}
			changed := restored.Files["VERSION"]
			changed.Digest = "changed"
			restored.Files["VERSION"] = changed
			if got := sameInputs(manifest, []string{"VERSION"}, restored, []string{"VERSION"}); got != tc.ignored {
				t.Fatal("unexpected input identity", got)
			}
		})
	}
}
