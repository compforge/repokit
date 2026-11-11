// Package release owns Forge Release observations and confirmed publication.
package release

import "github.com/compforge/quality-harness/sdks/go/common"

type Release struct {
	Tag          string `json:"tag"`
	Name         string `json:"name"`
	Notes        string `json:"notes"`
	URL          string `json:"url"`
	TargetCommit string `json:"targetCommit"`
	PublishedAt  string `json:"publishedAt"`
	Draft        bool   `json:"draft"`
	Prerelease   bool   `json:"prerelease"`
	Upcoming     bool   `json:"upcoming"`
}

type Phase struct {
	Name            string  `json:"name"`
	DurationSeconds float64 `json:"durationSeconds"`
}

type QueryResult struct {
	Repository common.Repository `json:"repository"`
	Release    *Release          `json:"release"`
	Selection  string            `json:"selection"`
	Phases     []Phase           `json:"phases"`
}

type CreateRequest struct {
	Tag    string
	Target string // Explicit remote commit or ref, resolved once before writing.
	Name   string
	Notes  string
}

type PublishResult struct {
	Repository        common.Repository `json:"repository"`
	Tag               string            `json:"tag"`
	RequestedTarget   string            `json:"requestedTarget"`
	ObservedTagCommit string            `json:"observedTagCommit,omitempty"`
	TargetCommit      string            `json:"targetCommit"`
	Release           *Release          `json:"release"`
	Confirmed         bool              `json:"confirmed"`
	WriteAttempted    bool              `json:"writeAttempted"`
	Phases            []Phase           `json:"phases"`
}
