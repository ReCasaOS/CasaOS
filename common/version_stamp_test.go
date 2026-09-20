package common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a box answers about its version comes from the release build, so the constant
// in this file cannot be what keeps it right. This is what keeps it right: every
// binary the release builds carries the stamp -- including the one somebody adds for
// a new architecture next year, which is the case that went wrong the first time.
func TestEveryReleaseBuildStampsTheVersion(t *testing.T) {
	stamp := "-X github.com/ReCasaOS/CasaOS/common.VERSION={{.Version}}"

	config, err := os.ReadFile(filepath.Join("..", ".goreleaser.yaml"))
	if err != nil {
		t.Fatal(err)
	}

	stamped := map[string]bool{}
	id := ""
	inBuilds := false

	for _, line := range strings.Split(string(config), "\n") {
		line = strings.TrimRight(line, "\r")

		switch {
		case line == "builds:":
			inBuilds = true

			continue
		case inBuilds && len(line) > 0 && !strings.HasPrefix(line, " "):
			// the next top-level key ends the builds
			inBuilds = false
		}

		if !inBuilds {
			continue
		}

		if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "- id: ") {
			id = strings.TrimPrefix(trimmed, "- id: ")
			stamped[id] = false
		}

		if id != "" && strings.Contains(line, stamp) {
			stamped[id] = true
		}
	}

	if len(stamped) == 0 {
		t.Fatal("no build found in .goreleaser.yaml -- has the file moved?")
	}

	for id, ok := range stamped {
		if !ok {
			t.Errorf("the build %s does not stamp the version, so its binary would answer %q for ever", id, VERSION)
		}
	}
}
