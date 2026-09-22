package common

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// What a box answers about its version comes from the release build, not from the
// default in constants.go. The first attempt stamped it in .goreleaser.yaml, which no
// release uses, and nothing noticed. This reads the build the release workflow really
// runs: every go build there must carry the stamp.
func TestTheReleaseBuildStampsTheVersion(t *testing.T) {
	workflow, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}

	builds, stamped := 0, 0
	for _, line := range strings.Split(string(workflow), "\n") {
		if !strings.Contains(line, "-ldflags") {
			continue
		}
		builds++
		if strings.Contains(line, "-X github.com/ReCasaOS/CasaOS/common.VERSION=${RELEASE_TAG#v}") {
			stamped++
		}
	}

	if builds == 0 {
		t.Fatal("no -ldflags in release.yml: has the build moved?")
	}
	if stamped != builds {
		t.Errorf("%d of %d release builds stamp the version; the others would answer %q", stamped, builds, VERSION)
	}
}
