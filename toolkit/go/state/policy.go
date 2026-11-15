package state

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
)

// CountPolicy is a soft cache bound, independent of Git/Forge deletion authority.
type CountPolicy struct {
	MaxCount int `json:"max_count"`
}
type CleanupPolicy struct {
	Repository CountPolicy `json:"repository"`
	PR         CountPolicy `json:"pr"`
	Comment    CountPolicy `json:"comment"`
	Branch     CountPolicy `json:"branch"`
	Validation CountPolicy `json:"validation"`
}

func DefaultCleanupPolicy() CleanupPolicy {
	return CleanupPolicy{CountPolicy{100}, CountPolicy{100}, CountPolicy{5000}, CountPolicy{200}, CountPolicy{1000}}
}
func (p CleanupPolicy) Validate() error {
	for _, n := range []int{p.Repository.MaxCount, p.PR.MaxCount, p.Comment.MaxCount, p.Branch.MaxCount, p.Validation.MaxCount} {
		if n < 0 {
			return errors.New("cleanup max_count must be nonnegative")
		}
	}
	return nil
}

// LoadCleanupPolicy overlays repository values on Home defaults. Zero is an explicit limit.
// +spec=Omission inherits; null, negative and fractional limits are rejected. Budgets never truncate successful remote reads.
// +link=component://docs/home.md#数量预算与访问事实
func LoadCleanupPolicy(home, key string) (CleanupPolicy, error) {
	p := DefaultCleanupPolicy()
	resolved, err := ResolveHome(home)
	if err != nil {
		return p, err
	}
	paths := []string{filepath.Join(resolved, "config.json")}
	if key != "" {
		if filepath.Base(key) != key || key == "." || key == ".." {
			return p, errors.New("invalid repository key")
		}
		paths = append(paths, filepath.Join(resolved, "repositories", key, "config.json"))
	}
	for _, path := range paths {
		if err := overlayPolicy(path, "cleanup", &p); err != nil {
			return p, err
		}
	}
	return p, p.Validate()
}
func overlayPolicy(path, name string, target any) error {
	var document map[string]json.RawMessage
	found, err := Read(path, &document)
	if err != nil || !found {
		return err
	}
	if document == nil {
		return errors.New("configuration must be an object")
	}
	raw, ok := document[name]
	if !ok {
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	if fields == nil {
		return fmt.Errorf("%s must be an object", name)
	}
	for key, value := range fields {
		if string(value) == "null" {
			return fmt.Errorf("%s.%s must not be null", name, key)
		}
		if name == "cleanup" {
			var count map[string]json.RawMessage
			if err := json.Unmarshal(value, &count); err != nil {
				return err
			}
			if count == nil {
				return errors.New("resource policy must be an object")
			}
			for k, v := range count {
				if k != "max_count" || string(v) == "null" {
					return fmt.Errorf("invalid cleanup %s field", key)
				}
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	return decoder.Decode(target)
}
