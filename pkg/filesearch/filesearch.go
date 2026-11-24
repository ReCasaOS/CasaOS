// Package filesearch finds files and folders by name under some folders, for the search of
// Files (CasaOS#184). It reads names only, never contents, does not follow links (a link is
// matched by its own name and not walked into), and stops at a number of hits, a number of
// entries read or a deadline, whichever comes first, saying so.
package filesearch

import (
	"context"
	"io/fs"
	"path/filepath"
	"strings"
	"time"
)

// Limits bound one search.
type Limits struct {
	Hits    int           // hits returned at most
	Entries int           // entries read at most
	Time    time.Duration // how long the walk may take
}

// DefaultLimits is what a search of Files is held to: enough for a person to read, and
// light enough for a small box.
var DefaultLimits = Limits{Hits: 200, Entries: 200_000, Time: 8 * time.Second}

// Hit is a file or folder whose name holds the query.
type Hit struct {
	Name  string
	Path  string
	IsDir bool
	Size  int64
	Date  time.Time
}

// Result is the hits, in the order the walk met them, and whether a limit stopped it before
// every folder was read.
type Result struct {
	Hits      []Hit
	Truncated bool
}

// pseudo is what a walk from "/" never enters: the kernel's and the run time's file systems,
// where names mean nothing to a person and reading can hang or never end.
var pseudo = map[string]bool{"/proc": true, "/sys": true, "/dev": true, "/run": true}

// Search walks each folder of starts and returns the entries whose name holds query, case
// not minded. A folder it cannot read is skipped. ctx ends it early too.
func Search(ctx context.Context, starts []string, query string, limits Limits) Result {
	needle := strings.ToLower(strings.TrimSpace(query))
	result := Result{Hits: []Hit{}}
	if needle == "" {
		return result
	}
	ctx, cancel := context.WithTimeout(ctx, limits.Time)
	defer cancel()

	read := 0
	stop := func() bool {
		if len(result.Hits) >= limits.Hits || read >= limits.Entries || ctx.Err() != nil {
			result.Truncated = true
			return true
		}
		return false
	}
	for _, start := range starts {
		if stop() {
			break
		}
		_ = filepath.WalkDir(start, func(p string, d fs.DirEntry, err error) error {
			if stop() {
				return filepath.SkipAll
			}
			if err != nil {
				// a folder that cannot be read: what is past it is skipped, the rest goes on
				if d != nil && d.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			read++
			if p == start {
				return nil
			}
			if d.IsDir() && (pseudo[p] || d.Name() == ".temp") {
				return filepath.SkipDir
			}
			if !strings.Contains(strings.ToLower(d.Name()), needle) {
				return nil
			}
			hit := Hit{Name: d.Name(), Path: p, IsDir: d.IsDir()}
			if info, err := d.Info(); err == nil {
				hit.Date = info.ModTime()
				if !hit.IsDir {
					hit.Size = info.Size()
				}
			}
			result.Hits = append(result.Hits, hit)
			return nil
		})
	}

	return result
}
