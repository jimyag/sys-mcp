// Package fileops provides file system operations for the agent, protected by PathGuard.
package fileops

import (
	"fmt"
	"path/filepath"
	"strings"
)

// PathGuard enforces access control on file system paths.
// Blocklist is checked first (priority); then allowlist (if non-empty).
type PathGuard struct {
	allowed []string
	blocked []string
}

// NewPathGuard creates a PathGuard with the given allowed and blocked path prefixes.
// Paths are cleaned before being stored. An empty allowed list means "allow all".
func NewPathGuard(allowed, blocked []string) *PathGuard {
	clean := func(paths []string) []string {
		out := make([]string, 0, len(paths))
		for _, p := range paths {
			c := filepath.Clean(p)
			if !strings.HasSuffix(c, string(filepath.Separator)) {
				c += string(filepath.Separator)
			}
			out = append(out, c)
		}
		return out
	}
	return &PathGuard{
		allowed: clean(allowed),
		blocked: clean(blocked),
	}
}

// Check returns nil if path is accessible, or an error if it is denied.
func (g *PathGuard) Check(path string) error {
	cleaned := filepath.Clean(path)

	// Resolve symlinks so that a symlink pointing outside the allowed tree
	// cannot be used to escape the guard. EvalSymlinks fails for non-existent
	// paths (pre-creation checks), so fall back to the cleaned path in that case.
	if real, err := filepath.EvalSymlinks(cleaned); err == nil {
		cleaned = real
	}

	// Ensure we compare with separator to prevent prefix attacks like /proc2.
	candidate := cleaned
	if !strings.HasSuffix(candidate, string(filepath.Separator)) {
		candidate += string(filepath.Separator)
	}

	// Blocklist has priority.
	for _, b := range g.blocked {
		if strings.HasPrefix(candidate, b) {
			return fmt.Errorf("fileops: path %q is blocked", path)
		}
	}

	// Allowlist: if empty, allow all.
	if len(g.allowed) == 0 {
		return nil
	}
	for _, a := range g.allowed {
		if strings.HasPrefix(candidate, a) {
			return nil
		}
	}
	return fmt.Errorf("fileops: path %q is not in allowed paths", path)
}
