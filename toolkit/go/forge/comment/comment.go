// Package comment defines PR comment facts and operations, independent of provider threads.
package comment

import (
	"context"

	"github.com/compforge/repokit/toolkit/go/forge/model"
)

// ID is opaque to consumers. Within one PR it is <resource>:<native-id>;
// adapters own the resource namespace and never derive identity from local cache state.
type ID = string

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
// +spec=List is flat and counts every reply; ReplyToID is the platform-recoverable structural parent; adapters normalize grouped replies to the discussion root.
// +spec=Resolution is the containing discussion's platform state, not human acceptance or proof of a fix.
// +link=component://docs/pr.md#pr-comments
type Comment struct {
	ID         ID         `json:"id"`
	PR         model.Key  `json:"pr"`
	ReplyToID  ID         `json:"replyToId,omitempty"`
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
	CommentID ID        `json:"commentId"`
	Deleted   bool      `json:"deleted"`
}

// Forge is the comment domain interface. Adapters own provider resources and write handles.
// Writes are never retried automatically. Resolve may affect the whole discussion;
// Reply appends to the target conversation; the returned parent may be its root.
// Delete must not silently cascade. Unknown write outcomes require reconciliation.
type Forge interface {
	List(context.Context, model.Key) ([]Comment, error)
	Get(context.Context, model.Key, ID) (Comment, error)
	Create(context.Context, model.Key, string, *Anchor) (Comment, error)
	Reply(context.Context, model.Key, ID, string) (Comment, error)
	Update(context.Context, model.Key, ID, string) (Comment, error)
	Delete(context.Context, model.Key, ID) (DeleteResult, error)
	Resolve(context.Context, model.Key, ID, bool) (Comment, error)
}
