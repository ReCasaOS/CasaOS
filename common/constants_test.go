package common

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// These files ship with the installer and decide which release feed a host
// polls: 03-setup-casaos.sh rewrites /etc/casaos/casaos.conf on every install
// and upgrade, and the two samples seed it on a fresh one. The Go resolvers prefer the config file over the
// constants below, so retargeting the constants without these files leaves
// installed hosts fetching someone else's distribution.
func TestShippedUpdateURLsMatchConstants(t *testing.T) {
	url := regexp.MustCompile(`https://\S+?/(?:install\.sh|version\.json)`)

	for _, path := range []string{
		"../build/scripts/setup/script.d/03-setup-casaos.sh",
		"../build/sysroot/etc/casaos/casaos.conf.sample",
		"../conf/conf.conf.sample",
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}

		found := url.FindAllString(string(content), -1)
		if len(found) == 0 {
			t.Errorf("%s: no installer or version URL found", path)
		}

		for _, got := range found {
			if got != FORK_UPDATE_URL && got != FORK_VERSION_URL {
				t.Errorf("%s: %q is neither FORK_UPDATE_URL (%q) nor FORK_VERSION_URL (%q)", path, got, FORK_UPDATE_URL, FORK_VERSION_URL)
			}
		}
	}
}

// The guard above only reads the files it is given, and a file nobody thought to add
// is exactly how an IceWhale URL survives a rebrand: delete-old-service.sh shipped
// into /usr/share/casaos/shell on every box for years, calling IceWhale's release API
// from a script nothing invoked. This one asks the opposite question -- what ships
// that still names somebody else -- so a new file has to be excused on purpose rather
// than merely overlooked.
func TestNothingShippedNamesIceWhaleExceptTheCatalogue(t *testing.T) {
	// The App Store is IceWhale's and stays IceWhale's, and so do the migration
	// entries: those releases exist nowhere else. Everything else is ours to answer
	// for.
	allowed := regexp.MustCompile(`(?i)IceWhaleTech/CasaOS-AppStore|icon\.casaos\.io|cloudoauth\.files\.casaos\.app|IceWhaleTech/_appstore`)
	mentions := regexp.MustCompile(`(?i)icewhale`)

	roots := []string{"../build/sysroot", "../build/scripts"}

	for _, root := range roots {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			// migration.list is a list of releases that only ever existed upstream
			if d.Name() == "migration.list" {
				return nil
			}

			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}

			for _, line := range strings.Split(string(content), "\n") {
				if !mentions.MatchString(line) || allowed.MatchString(line) {
					continue
				}
				// a per-file copyright notice is an attribution obligation
				trimmed := strings.TrimLeft(line, " \t#*/-")
				if strings.HasPrefix(trimmed, "@") || strings.Contains(line, "Copyright (c)") {
					continue
				}
				t.Errorf("%s ships and names IceWhale: %q", path, strings.TrimSpace(line))
			}
			return nil
		})
		if err != nil {
			t.Fatalf("%s: %v", root, err)
		}
	}
}
