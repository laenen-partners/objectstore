package objectstore

import (
	"fmt"
	"path/filepath"
	"strings"
)

// safePath joins basePath with additional path components and verifies the
// result does not escape basePath. Returns an error on path traversal attempts.
func safePath(basePath string, parts ...string) (string, error) {
	for _, p := range parts {
		if p == "" {
			return "", fmt.Errorf("path component must not be empty")
		}
		cleaned := filepath.Clean(p)
		if strings.HasPrefix(cleaned, "..") || filepath.IsAbs(cleaned) {
			return "", fmt.Errorf("invalid path component: %q", p)
		}
	}

	absBase, err := filepath.Abs(basePath)
	if err != nil {
		return "", fmt.Errorf("resolve base path: %w", err)
	}

	allParts := append([]string{absBase}, parts...)
	joined := filepath.Join(allParts...)

	if !strings.HasPrefix(joined, absBase+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes base directory")
	}

	return joined, nil
}
