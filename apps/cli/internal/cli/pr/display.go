package pr

import (
	"fmt"
	"github.com/compforge/repokit/toolkit/go/forge/comment"
	"io"
)

func displayComment(w io.Writer, c comment.Comment) {
	fmt.Fprintf(w, "\nComment %s by %s (%s); resolution=%s\n", c.ID, c.Author, c.CreatedAt, c.Resolution)
	if c.ReplyToID != "" {
		fmt.Fprintf(w, "Reply to %s\n", c.ReplyToID)
	}
	if a := c.Anchor; a != nil {
		fmt.Fprintf(w, "%s @ %s (%s)", a.Path, a.Revision, a.Side)
		if a.Line != nil {
			fmt.Fprintf(w, ":%d", *a.Line)
		} else if a.OriginalLine != nil {
			fmt.Fprintf(w, "; original line %d @ %s", *a.OriginalLine, a.OriginalRevision)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, c.Body)
}
