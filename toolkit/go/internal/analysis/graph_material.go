package analysis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"slices"
	"sort"
	"unicode/utf8"

	shared "github.com/compforge/codegraph"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

// GraphResult retains the graph publication, admitted bytes and separate input
// coverage diagnostics. View identity is available from Graph.Snapshot(); this
// does not manufacture a content-capture report for a caller-owned view.
type GraphResult struct {
	Graph            *shared.Graph
	Report           shared.BuildReport
	InputDiagnostics []shared.Diagnostic
	Documents        int
	Sources          map[string][]byte
}

// GraphOptions separates caller admission and priority from repository loading.
// CodeGraph's document, per-document byte and total byte limits must be positive.
// CodeGraph.Scope bounds directory entry; Select filters document formats.
// Callers should admit the manifests
// needed to establish identity for their selected source files.
type GraphOptions struct {
	// RetryOnBudget permits reduced coverage after a structured graph budget error.
	// Strict consumers leave this disabled.
	RetryOnBudget bool
	CodeGraph     shared.Options
	Prioritize    []string
	Select        func(path string, tags []shared.Tag) bool
}

// GraphFromSource prepares repository materials and publishes their shared graph.
// It never recaptures the source or guesses module names from path prefixes.
// +spec=`Graph, sources and diagnostics share the supplied view; metadata precedes prioritized sources and every omission is reported`
// +link=repo://docs/codegraph.md
func GraphFromSource(ctx context.Context, source GraphSource, opts GraphOptions) (*GraphResult, error) {
	cgOpts := opts.CodeGraph
	if source == nil {
		return nil, fmt.Errorf("graph source is required")
	}
	if cgOpts.MaxDocuments <= 0 || cgOpts.MaxDocumentBytes <= 0 || cgOpts.MaxSourceBytes <= 0 {
		return nil, fmt.Errorf("graph source document and byte limits must be positive")
	}
	matcher, err := shared.NewTagMatcher(cgOpts.TagRules)
	if err != nil {
		return nil, err
	}
	files, rules, err := selectGraphMaterials(ctx, source, matcher, opts)
	if err != nil {
		return nil, err
	}
	priority := map[string]bool{}
	for _, name := range opts.Prioritize {
		priority[name] = true
	}
	metadata := func(name string) bool {
		return project.ManifestEcosystem(name) != "" || slices.Contains(matcher.Match(name), shared.ManifestTag)
	}
	sort.SliceStable(files, func(i, j int) bool {
		a, b := files[i].Path, files[j].Path
		if metadata(a) != metadata(b) {
			return metadata(a)
		}
		if priority[a] != priority[b] {
			return priority[a]
		}
		return a < b
	})
	var docs []shared.Document
	diagnostics := []shared.Diagnostic{}
	issue := func(code, name, message string) {
		diagnostics = append(diagnostics, shared.Diagnostic{Code: code, Subject: shared.DocumentSubject, Location: shared.Location{Path: name}, Message: name + ": " + message})
	}
	var size int64
	for _, file := range files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		doc := shared.Document{Path: file.Path}
		if file.Size < 0 || file.Size > cgOpts.MaxDocumentBytes {
			issue("document_byte_limit", file.Path, "source unavailable or exceeds document byte limit")
			continue
		}
		if len(docs) >= cgOpts.MaxDocuments || size+file.Size > cgOpts.MaxSourceBytes {
			issue("document_limit", file.Path, "repository source budget reached")
			continue
		}
		data, loaded := rules[file.Path]
		var err error
		if !loaded {
			data, err = source.ReadSource(ctx, file.Path)
		}
		if err != nil {
			issue("source_unavailable", file.Path, err.Error())
			continue
		}
		if bytes.IndexByte([]byte(data), 0) >= 0 || !utf8.ValidString(data) {
			continue
		}
		if int64(len(data)) > cgOpts.MaxDocumentBytes || size+int64(len(data)) > cgOpts.MaxSourceBytes {
			issue("document_byte_limit", file.Path, "source exceeds byte limit")
			continue
		}
		doc.Content = []byte(data)
		docs = append(docs, doc)
		size += int64(len(data))
	}
	var graph *shared.Graph
	var report shared.BuildReport
	n := len(docs)
	for {
		graph, report, err = shared.Build(ctx, source.SnapshotID(), docs[:n], cgOpts)
		if err == nil || !opts.RetryOnBudget || !errors.Is(err, shared.ErrBuildBudget) || n <= 1 || ctx.Err() != nil {
			break
		}
		n /= 2
	}
	if err != nil {
		return nil, fmt.Errorf("build code graph: %w", err)
	}
	if n < len(docs) {
		diagnostics = append(diagnostics, shared.Diagnostic{Code: "graph_budget", Subject: shared.DocumentSubject, Message: fmt.Sprintf("graph: omitted %d of %d documents; prioritized materials retained", len(docs)-n, len(docs))})
	}
	sources := map[string][]byte{}
	for _, doc := range docs[:n] {
		sources[doc.Path] = doc.Content
	}
	return &GraphResult{Graph: graph, Report: report, InputDiagnostics: diagnostics, Documents: n, Sources: sources}, nil
}
