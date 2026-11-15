// Package comment defines PR comment facts and operations, independent of provider threads.
package comment

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/forge/model"
)

type Resolution string

const (
	Unresolved  Resolution = "unresolved"
	Resolved    Resolution = "resolved"
	Unsupported Resolution = "unsupported"
	Unknown     Resolution = "unknown"
)

type Anchor struct {
	Path              string `json:"path"`
	Revision          string `json:"revision"`
	Side              string `json:"side,omitempty"`
	Line              *int   `json:"line,omitempty"`
	StartLine         *int   `json:"startLine,omitempty"`
	StartSide         string `json:"startSide,omitempty"`
	OriginalRevision  string `json:"originalRevision,omitempty"`
	OriginalLine      *int   `json:"originalLine,omitempty"`
	OriginalStartLine *int   `json:"originalStartLine,omitempty"`
	OldPath           string `json:"oldPath,omitempty"`
	Outdated          *bool  `json:"outdated,omitempty"`
}

// Comment is one published message. IDs are opaque and unique within its PR.
// +spec=List is flat and counts every reply; ReplyToID is a known direct parent, never inferred from discussion order.
// +spec=Resolution is the containing discussion's platform state, not human acceptance or proof of a fix.
// +link=component://docs/pr.md#pr-comments
type Comment struct {
	ID         string     `json:"id"`
	PR         model.Key  `json:"pr"`
	ReplyToID  string     `json:"replyToId,omitempty"`
	Body       string     `json:"body"`
	Author     string     `json:"author"`
	CreatedAt  string     `json:"createdAt"`
	UpdatedAt  string     `json:"updatedAt,omitempty"`
	URL        string     `json:"url,omitempty"`
	Anchor     *Anchor    `json:"anchor,omitempty"`
	Resolution Resolution `json:"resolution"`
	ResolvedBy string     `json:"resolvedBy,omitempty"`
	ResolvedAt string     `json:"resolvedAt,omitempty"`
}

type DeleteResult struct {
	PR        model.Key `json:"pr"`
	CommentID string    `json:"commentId"`
	Deleted   bool      `json:"deleted"`
}

// Forge is the comment domain interface. Adapters own provider resources and write handles.
// Writes are never retried automatically. Resolve may affect the whole discussion;
// Delete must not silently cascade. Unknown write outcomes require reconciliation.
type Forge interface {
	List(context.Context, model.Key) ([]Comment, error)
	Get(context.Context, model.Key, string) (Comment, error)
	Create(context.Context, model.Key, string, *Anchor) (Comment, error)
	Reply(context.Context, model.Key, string, string) (Comment, error)
	Update(context.Context, model.Key, string, string) (Comment, error)
	Delete(context.Context, model.Key, string) (DeleteResult, error)
	Resolve(context.Context, model.Key, string, bool) (Comment, error)
}
