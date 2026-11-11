// Package state owns repository observations, checkout lifecycle and their persistence.
// Git adapters read and mutate the repository; state owns freshness, publication,
// retention and recovery. A persisted observation never authorizes a destructive action.
//
// +link=component://docs/state.md
package state
