package pr

import (
	"bytes"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go/forge/discussion"
)

func TestDiscussionTextPreservesBodiesAndReplies(t *testing.T) {
	// Presentation uses the same public objects as JSON, without summary-length limits.
	body := strings.Repeat("Markdown\n  indentation\n", 100)
	var out bytes.Buffer
	displayDiscussion(&out, discussion.Discussion{ID: "thread", Resolution: "unresolved", Comments: []discussion.Comment{{ID: "1", Body: body, Path: "a.go", Line: new(42)}, {ID: "2", Body: "last reply"}}}, true)
	if !strings.Contains(out.String(), body) || !strings.Contains(out.String(), "last reply") || !strings.Contains(out.String(), "a.go:42") {
		t.Fatal(out.String())
	}
}
