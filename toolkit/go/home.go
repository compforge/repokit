package repokit

import "github.com/compforge/repokit/toolkit/go/internal/home"

// ResolveHome resolves explicit home, REPOKIT_HOME or ~/.repokit, in that order.
// It does not create directories, migrate data or perform resource operations.
func ResolveHome(explicit string) (string, error) { return home.Resolve(explicit) }
