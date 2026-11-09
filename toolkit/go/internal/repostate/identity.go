// Package repostate defines the shared repository information layout.
package repostate

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// Part preserves readable lower-case names and distinguishes case on insensitive filesystems.
func Part(value string) string {
	var b strings.Builder
	for _, c := range []byte(value) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			fmt.Fprintf(&b, "%%%02X", c)
		}
	}
	name := b.String()
	if name == "" || name == "." || name == ".." {
		name = strings.ReplaceAll(name, ".", "%2E")
		if name == "" {
			name = "%00"
		}
	}
	if len(name) > 180 {
		name = name[:120] + "--" + Digest(value)
	}
	return name
}
func Digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return fmt.Sprintf("%x", sum[:16])
}
func RepositoryKey(forge, path string) string {
	parts := append([]string{forge}, strings.Split(path, "/")...)
	for i, p := range parts {
		parts[i] = Part(p)
	}
	name := strings.Join(parts, "--")
	if len(name) > 240 {
		name = name[:160] + "--" + Digest(forge+"/"+path)
	}
	return name
}
func LocalKey(common string) string {
	label := filepath.Base(common)
	if label == ".git" {
		label = filepath.Base(filepath.Dir(common))
	}
	sum := sha256.Sum256([]byte(common))
	return fmt.Sprintf("local--%s--%x", Part(label), sum[:8])
}
