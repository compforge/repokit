package repostate

import (
	"encoding/json"
	"os"
	"testing"
)

func TestSharedIdentity(t *testing.T) {
	data, err := os.ReadFile("../../../../conformance/repository-state.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Repositories []struct{ Forge, Path, Key string }
		Branches     []struct{ Name, Key string }
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, row := range fixture.Repositories {
		if got := RepositoryKey(row.Forge, row.Path); got != row.Key {
			t.Errorf("%s: %s != %s", row.Path, got, row.Key)
		}
	}
	for _, row := range fixture.Branches {
		if got := Part(row.Name); got != row.Key {
			t.Errorf("%s: %s != %s", row.Name, got, row.Key)
		}
	}
}
