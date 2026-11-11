package discussion

import "github.com/compforge/repokit/toolkit/go/forge/model"

// Comment is a published message. IDs are opaque and scoped to its PR and thread.
type Comment struct {
	ID        string `json:"id"`
	Body      string `json:"body"`
	Author    string `json:"author"`
	CreatedAt string `json:"createdAt"`
	URL       string `json:"url,omitempty"`
	Path      string `json:"path,omitempty"`
	Line      *int   `json:"line,omitempty"`
}

// Discussion keeps platform capabilities separate from application review verdicts.
// +spec=Full bodies and replies are retained; conversation comments do not acquire synthetic resolve/reply capabilities
// +spec=Resolve capability is true, false or unknown (nil); missing platform evidence never means unsupported
// +link=component://docs/pr.md
type Discussion struct {
	PullRequest model.Key `json:"pullRequest"`
	ID          string    `json:"id"`
	Comments    []Comment `json:"comments"`
	Replyable   bool      `json:"replyable"`
	Resolvable  *bool     `json:"resolvable"`
	Resolution  string    `json:"resolution"` // not_applicable, unknown, unresolved, resolved
}

type CommentResult struct {
	PullRequest model.Key `json:"pullRequest"`
	ThreadID    string    `json:"threadId"`
	Comment     Comment   `json:"comment"`
}
