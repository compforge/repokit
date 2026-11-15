package pr

import (
	"bytes"
	"strings"
	"testing"

	"github.com/compforge/repokit/toolkit/go/forge/comment"
)

func TestCommentDisplayRetainsBodyAndOriginalAnchor(t *testing.T) {
	var out bytes.Buffer
	body := strings.Repeat("evidence\n  exact\n", 40)
	displayComment(&out, comment.Comment{ID: "review:1", ReplyToID: "review:0", Body: body, Resolution: comment.Unknown, Anchor: &comment.Anchor{Path: "a.go", Revision: "new", OriginalRevision: "old", OriginalLine: new(42)}})
	if !strings.Contains(out.String(), body) || !strings.Contains(out.String(), "original line 42 @ old") || !strings.Contains(out.String(), "Reply to review:0") {
		t.Fatal(out.String())
	}
}
