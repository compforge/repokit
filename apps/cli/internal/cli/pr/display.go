package pr

import (
	"fmt"
	"io"
	"strconv"

	"github.com/compforge/repokit/toolkit/go/forge/discussion"
)

func displayDiscussion(w io.Writer, thread discussion.Discussion, full bool) {
	resolvable := "unknown"
	if thread.Resolvable != nil {
		resolvable = strconv.FormatBool(*thread.Resolvable)
	}
	fmt.Fprintf(w, "Thread %s: %s; %d comment(s); replyable=%t resolvable=%s\n", thread.ID, thread.Resolution, len(thread.Comments), thread.Replyable, resolvable)
	for _, comment := range thread.Comments {
		if full {
			displayComment(w, comment)
		} else {
			fmt.Fprintf(w, "  comment %s by %s", comment.ID, comment.Author)
			if comment.Path != "" {
				fmt.Fprintf(w, " at %s", comment.Path)
				if comment.Line != nil {
					fmt.Fprintf(w, ":%d", *comment.Line)
				}
			}
			fmt.Fprintln(w)
		}
	}
}
func displayComment(w io.Writer, comment discussion.Comment) {
	fmt.Fprintf(w, "\nComment %s by %s (%s)\n", comment.ID, comment.Author, comment.CreatedAt)
	if comment.Path != "" {
		fmt.Fprint(w, comment.Path)
		if comment.Line != nil {
			fmt.Fprintf(w, ":%d", *comment.Line)
		}
		fmt.Fprintln(w)
	}
	fmt.Fprintln(w, comment.Body)
}
