// Package git provides Git material queries and shared ref, publication, lease,
// and recovery-record mechanisms. Callers own operation phases and lifecycle policy.
//
// +spec=Git adapters do not depend on state or public Git operation workflows; command execution depends only on checkout environment isolation and low-level error handling.
// +link=component://toolkit/go/AGENTS.md
package git
