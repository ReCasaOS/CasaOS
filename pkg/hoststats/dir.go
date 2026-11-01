package hoststats

import "path/filepath"

// DirForDBPath returns the host-stats store directory under dbPath.
func DirForDBPath(dbPath string) string {
	return filepath.Join(dbPath, "stats", "core")
}
