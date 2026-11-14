package rebase

import (
	"encoding/json"
	"errors"
	repogit "github.com/compforge/repokit/toolkit/go/internal/git"
	"os"
	"path/filepath"
)

type record struct {
	Version int `json:"version"`
	Transaction
	Phase string `json:"phase"`
	// Destination is private: transport URLs may contain credentials. Never include it in reports/errors.
	Destination string `json:"destination"`
}

const recordName = "repokit-rebase.json"

func readRecord(dir string) (*record, error) {
	b, e := os.ReadFile(filepath.Join(dir, recordName))
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var r record
	if e = json.Unmarshal(b, &r); e != nil {
		return nil, failure("record_invalid", "cannot decode rebase recovery record", e)
	}
	if r.Version != 1 || r.Branch == "" || r.Target == "" || r.Remote == "" || r.Destination == "" || !oid(r.StartedHead) || !oid(r.TargetCommit) || !oid(r.ExpectedRemote) || (r.PublishHead != "" && !oid(r.PublishHead)) || r.StartedAt.IsZero() {
		return nil, failure("record_invalid", "invalid rebase recovery identity", nil)
	}
	switch r.Phase {
	case "prepared", "paused", "ready", "publishing", "published", "aborted", "failed":
	default:
		return nil, failure("record_invalid", "invalid rebase recovery phase", nil)
	}
	if (r.Phase == "publishing" || r.Phase == "published") && r.PublishHead == "" {
		return nil, failure("record_invalid", "missing frozen publication commit", nil)
	}
	return &r, nil
}
func writeRecord(dir string, r *record) error {
	return repogit.WriteRecord(filepath.Join(dir, recordName), r)
}

func terminal(phase string) bool {
	return phase == "published" || phase == "aborted" || phase == "failed"
}
func oid(s string) bool {
	if len(s) != 40 && len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}
