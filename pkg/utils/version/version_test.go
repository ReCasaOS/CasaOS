package version

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
)

func TestIsVersionNewer(t *testing.T) {
	tests := []struct {
		name    string
		latest  string
		current string
		want    bool
	}{
		{name: "platform-neutral migration", latest: "v0.4.20", current: "v0.4.17-ubuntu26.3", want: true},
		{name: "next release", latest: "v0.4.20", current: "v0.4.19", want: true},
		{name: "next CasaOS minor", latest: "v0.5.0", current: "v0.4.20", want: true},
		{name: "same release", latest: "v0.4.20", current: "v0.4.20", want: false},
		{name: "older compatibility release", latest: "v0.4.19", current: "v0.4.20", want: false},
		{name: "missing latest", latest: "", current: "v0.4.20", want: false},
		{name: "invalid latest", latest: "latest", current: "v0.4.20", want: false},
		// a pre-release comes before its release, as the plan of a module reads min_recasaos
		{name: "the release after its candidate", latest: "v0.5.30", current: "0.5.30-rc1", want: true},
		{name: "a candidate is not newer than its release", latest: "v0.5.30-rc1", current: "0.5.30", want: false},
		{name: "the candidate of the next release", latest: "v0.5.31-rc1", current: "0.5.30", want: true},
		{name: "a build is the release", latest: "v0.5.30+2", current: "0.5.30", want: false},
		{name: "numbers, not text", latest: "v0.5.100", current: "0.5.99", want: true},
		{name: "the next candidate", latest: "v0.5.30-rc2", current: "0.5.30-rc1", want: true},
		{name: "the tenth candidate after the second", latest: "v0.5.30-rc10", current: "0.5.30-rc2", want: true},
		{name: "an earlier candidate", latest: "v0.5.30-rc1", current: "0.5.30-rc2", want: false},
		{name: "a candidate after a beta", latest: "v0.5.30-rc1", current: "0.5.30-beta3", want: true},
		// what the tags of the distribution never are, said
		{name: "four numbers are no version", latest: "v0.5.30.1", current: "0.5.30", want: false},
		{name: "a suffix is a pre-release of its release", latest: "v0.5.30-1", current: "0.5.30", want: false},
		{name: "a suffix is after the release before", latest: "v0.5.30-1", current: "0.5.29", want: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsVersionNewer(test.latest, test.current); got != test.want {
				t.Fatalf("IsVersionNewer(%q, %q) = %v, want %v", test.latest, test.current, got, test.want)
			}
		})
	}
}

func TestCurrentVersionFromFile(t *testing.T) {
	// The version is reported without the "v" of the release tag: the dashboard
	// prefixes it itself, and both sources below hold the tag verbatim.
	fallback := strings.TrimPrefix(common.FORK_RELEASE_VERSION, "v")

	versionFile := filepath.Join(t.TempDir(), "fork-release")
	if got := currentVersionFromFile(versionFile); got != fallback {
		t.Fatalf("missing version file returned %q, want %q", got, fallback)
	}

	if err := os.WriteFile(versionFile, []byte("v0.4.20\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := currentVersionFromFile(versionFile); got != "0.4.20" {
		t.Fatalf("installed version returned %q", got)
	}
}

// The marker may name a distribution NEWER than the one this binary was built for,
// because a distribution release need not ship this component. The marker wins: it is
// what the installer wrote on this host, and the constant is only the floor. Reading
// the two as equal is what made every distribution release drag a release of this
// component along.
func TestInstalledVersionWinsOverTheBuildFloor(t *testing.T) {
	versionFile := filepath.Join(t.TempDir(), "fork-release")
	if err := os.WriteFile(versionFile, []byte("v9.9.9\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	current := currentVersionFromFile(versionFile)
	if current != "9.9.9" {
		t.Fatalf("installed version returned %q, want %q", current, "9.9.9")
	}

	// and the host is not then told to update to the release it is already running
	if IsVersionNewer("v9.9.9", current) {
		t.Fatal("a host was offered the release it already has")
	}
}

// An empty or blank marker is not a version. It falls back rather than reporting "",
// which IsVersionNewer would read as older than everything.
func TestBlankMarkerFallsBack(t *testing.T) {
	fallback := strings.TrimPrefix(common.FORK_RELEASE_VERSION, "v")

	for _, content := range []string{"", "\n", "   \n\t"} {
		versionFile := filepath.Join(t.TempDir(), "fork-release")
		if err := os.WriteFile(versionFile, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}

		if got := currentVersionFromFile(versionFile); got != fallback {
			t.Fatalf("blank marker %q returned %q, want %q", content, got, fallback)
		}
	}
}
