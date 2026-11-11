package cli

import (
	"fmt"
	"io"

	"github.com/compforge/repokit/toolkit/go"
)

func writeRawDiff(w io.Writer, d repokit.DiffReport, structured bool) error {
	if !structured {
		recordResult(w, d)
	}
	if structured {
		return writeJSON(w, d)
	}
	for _, ch := range d.Changes {
		if _, err := fmt.Fprintf(w, "diff %s (+%d -%d)\n%s\n", ch.Path(), ch.Insertions, ch.Deletions, ch.Diff); err != nil {
			return err
		}
	}
	for _, diagnostic := range d.Diagnostics {
		if _, err := fmt.Fprintf(w, "[%s] %s: %s\n", diagnostic.Code, diagnostic.Path, diagnostic.Message); err != nil {
			return err
		}
	}
	return nil
}
