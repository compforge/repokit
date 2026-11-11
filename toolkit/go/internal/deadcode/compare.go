package deadcode

import (
	"context"
	"strings"

	shared "github.com/compforge/codegraph"
)

// +spec=`Diff nodes seed dependency analysis in both versions; candidate files never bound incoming-use evidence`
func compare(ctx context.Context, before, after *graphIndex, changed []string, renames map[string]string) ([]Candidate, error) {
	paths := map[string]bool{}
	for _, path := range changed {
		paths[path] = true
	}
	affected := map[string]bool{}
	for i, graph := range []*graphIndex{before, after} {
		seeds := []string{}
		for _, node := range graph.graph.Nodes {
			path := node.Path
			if node.Location != nil {
				path = node.Location.Path
			}
			if paths[path] {
				seeds = append(seeds, node.ID)
			}
		}
		reached, err := walk(ctx, graph.next, seeds)
		if err != nil {
			return nil, err
		}
		for id := range reached {
			mapping := map[string]string(nil)
			if i == 0 {
				mapping = renames
			}
			affected[identity(graph.nodes[id], mapping)] = true
		}
	}
	previous := map[string][]shared.Node{}
	current := map[string]int{}
	previousCandidates := map[string]Candidate{}
	for _, node := range before.graph.Nodes {
		if before.declared[node.ID] {
			key := identity(node, renames)
			previous[key] = append(previous[key], node)
		}
	}
	for _, node := range after.graph.Nodes {
		if after.declared[node.ID] {
			current[identity(node, nil)]++
		}
	}
	for _, candidate := range before.candidates {
		previousCandidates[candidate.ID] = candidate
	}
	out := []Candidate{}
	for _, candidate := range after.candidates {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		key := identity(candidate.Node, nil)
		if !affected[key] {
			continue
		}
		prior := previous[key]
		switch {
		case len(prior) > 1 || current[key] > 1:
			candidate.Change = "unmatched"
			candidate.Risks = append(candidate.Risks, "ambiguous_identity")
		case len(prior) == 0:
			candidate.Change = "introduced"
			if len(before.graph.Diagnostics) > 0 || !before.graph.CaptureComplete {
				candidate.Change = "unmatched"
				candidate.Risks = append(candidate.Risks, "baseline_gaps")
			}
		default:
			old := prior[0]
			candidate.Previous = &old
			candidate.Change = "became_dead"
			if prev, ok := previousCandidates[old.ID]; ok {
				candidate.Change = "pre_existing"
				if prev.Reason == "test_only" && candidate.Reason != "test_only" {
					candidate.Change = "became_dead"
				}
			} else if candidate.Reason == "test_only" {
				candidate.Change = "became_test_only"
			}
			candidate.LostUses = lostUses(before, after, old.ID, candidate.ID, renames)
			if len(before.graph.Diagnostics) > 0 || !before.graph.CaptureComplete {
				candidate.Risks = append(candidate.Risks, "baseline_gaps")
			}
		}
		out = append(out, candidate)
	}
	return out, nil
}

// Location offsets are not identities: inserting a line must not turn every
// existing declaration into an introduced candidate. Ambiguous keys stay unknown.
func identity(node shared.Node, renames map[string]string) string {
	path := node.Path
	if node.Location != nil {
		path = node.Location.Path
	}
	if renamed, ok := renames[path]; ok {
		path = renamed
	}
	name := node.QualifiedName
	if node.Kind == shared.DocumentNodeKind {
		name = path
	}
	if name == "" {
		name = node.Name
	}
	return strings.Join([]string{path, node.Language, string(node.Kind), name}, "\x00")
}

func lostUses(before, after *graphIndex, oldID, newID string, renames map[string]string) []Use {
	current := map[string]bool{}
	for _, use := range after.incomingUses(newID) {
		current[identity(use.Source, nil)+"\x00"+string(use.Kind)] = true
	}
	lost := []Use{}
	seen := map[string]bool{}
	for _, use := range before.incomingUses(oldID) {
		key := identity(use.Source, renames) + "\x00" + string(use.Kind)
		if !current[key] && !seen[key] {
			lost = append(lost, use)
			seen[key] = true
		}
	}
	return lost
}

func (d *graphIndex) incomingUses(target string) []Use {
	uses := []Use{}
	for _, index := range d.incoming[target] {
		edge := d.graph.Relations[index]
		switch edge.Kind {
		case shared.Declares, shared.Contains, shared.Encloses, shared.InNamespace, shared.InDirectory, shared.OccursIn:
			continue
		}
		source := d.nodes[edge.Source]
		location := &edge.Location
		if location.Path == "" {
			location = source.Location
		}
		if owner, ok := d.nodes[d.owner[source.ID]]; ok {
			source = owner
		}
		uses = append(uses, Use{Source: source, Kind: edge.Kind, Location: location, Confidence: edge.Confidence})
	}
	return uses
}
