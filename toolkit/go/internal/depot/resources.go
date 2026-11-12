// Package depot owns shared Bin identity and receipt evidence for preparation and reclamation.
package depot

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/compforge/repokit/toolkit/go/internal/git"
	"github.com/compforge/repokit/toolkit/go/internal/project"
)

var ErrUnsafeReceipt = errors.New("unsafe dependency receipt")
var ErrInvalidReceipt = errors.New("invalid dependency receipt")

const ReceiptName = ".repokit-dependencies.json"

type Receipt struct {
	Manager     string `json:"manager"`
	Fingerprint string `json:"fingerprint"`
	Generation  string `json:"generation"`
}

// ReadReceipt separates absent ownership evidence from corrupt or unsafe evidence.
func ReadReceipt(directory string) (*Receipt, error) {
	file := filepath.Join(directory, ReceiptName)
	info, err := os.Lstat(file)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 2<<20 {
		return nil, fmt.Errorf("%w: %s", ErrUnsafeReceipt, file)
	}
	stream, err := os.Open(file)
	if err != nil {
		return nil, err
	}
	defer stream.Close()
	data, err := io.ReadAll(io.LimitReader(stream, 2<<20+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 2<<20 {
		return nil, fmt.Errorf("%w: %s", ErrUnsafeReceipt, file)
	}
	var receipt Receipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, fmt.Errorf("%w: %s", ErrInvalidReceipt, file)
	}
	if receipt.Manager == "" || receipt.Fingerprint == "" {
		return nil, fmt.Errorf("%w: %s", ErrInvalidReceipt, file)
	}
	return &receipt, nil
}

type Resource struct {
	Path    string
	Managed bool
}

// Resources uses the same declared Depot Bins as preparation. Ownership is independent
// of freshness: a stale installation remains owned, while a receipt-free one is preserved.
func Resources(ctx context.Context, root string) ([]Resource, error) {
	repo, err := git.Open(ctx, root)
	if err != nil {
		return nil, err
	}
	files, err := repo.Catalog(ctx, "", false, project.NeedsContent)
	if err != nil {
		return nil, err
	}
	layout, err := project.Load(files, "")
	if err != nil {
		return nil, err
	}
	paths := map[string]bool{}
	for _, binding := range layout.Components {
		if binding.Depot == nil {
			continue
		}
		for _, bin := range binding.Depot.Bins {
			if bin.Kind == "dependencies" {
				paths[bin.Path] = true
			}
		}
	}
	names := []string{}
	for path := range paths {
		names = append(names, path)
	}
	slices.Sort(names)
	result := []Resource{}
	for _, path := range names {
		directory := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Lstat(directory)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.IsDir() {
			return nil, fmt.Errorf("unsafe dependency path: %s", path)
		}
		receipt, err := ReadReceipt(directory)
		if err != nil {
			return nil, err
		}
		result = append(result, Resource{Path: directory, Managed: receipt != nil})
	}
	return result, nil
}
