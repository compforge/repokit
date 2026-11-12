package branch

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type record struct {
	Version         int    `json:"version"`
	Branch          string `json:"branch"`
	BaseCommit      string `json:"baseCommit"`
	SourceBranch    string `json:"sourceBranch"`
	SourceHead      string `json:"sourceHead"`
	Marker          string `json:"marker"`
	StashOID        string `json:"stashOid,omitempty"`
	Phase           string `json:"phase"`
	HadChanges      bool   `json:"hadChanges"`
	ChangesRestored bool   `json:"changesRestored"`
}

func recordPath(dir, name string) string {
	sum := sha256.Sum256([]byte(name))
	return filepath.Join(dir, "repokit-branches", hex.EncodeToString(sum[:])+".json")
}
func readRecord(path, name string) (*record, error) {
	data, e := os.ReadFile(path)
	if errors.Is(e, os.ErrNotExist) {
		return nil, nil
	}
	if e != nil {
		return nil, e
	}
	var r record
	if e = json.Unmarshal(data, &r); e != nil {
		return nil, failure("record_invalid", "invalid branch recovery record", e)
	}
	if r.Version != 1 || r.Branch != name || !oid(r.BaseCommit) || !oid(r.SourceHead) || r.Marker == "" || (r.StashOID != "" && !oid(r.StashOID)) {
		return nil, failure("record_invalid", "invalid branch recovery identity", nil)
	}
	switch r.Phase {
	case "prepared", "saving", "saved", "creating", "created", "restoring", "restored", "failed", "conflict":
	default:
		return nil, failure("record_invalid", "invalid branch recovery phase", nil)
	}
	return &r, nil
}
func newMarker() (string, error) {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return "", e
	}
	return "repokit-branch-" + hex.EncodeToString(b), nil
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
func terminal(r *record) bool {
	return r.Phase == "restored" || r.Phase == "failed" || (r.Phase == "created" && !r.HadChanges)
}
