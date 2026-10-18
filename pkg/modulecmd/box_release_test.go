package modulecmd

import (
	"os"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The release of the distribution that min_recasaos is compared with is the marker the installer writes, and nothing else: a box whose marker is not there or does not hold a release
// is a box whose release is unknown, which is not a release that is old enough.

func TestTheReleaseIsWhatTheMarkerSaysAndNothingElse(t *testing.T) {
	for name, c := range map[string]struct {
		content *string
		want    string
	}{
		"no marker":            {nil, ""},
		"an empty marker":      {ptr(""), ""},
		"a blank marker":       {ptr(" \n\t\n"), ""},
		"a tag":                {ptr("v0.5.29\n"), "v0.5.29"},
		"a number":             {ptr("0.5.29"), "0.5.29"},
		"spaces around":        {ptr("  v0.5.29  \n"), "v0.5.29"},
		"words (not judged)":   {ptr("latest\n"), "latest"},
		"a file that is long":  {ptr(strings.Repeat("v0.5.29", 20)), ""},
		"a file of many lines": {ptr(strings.Repeat("0.5.29\n", 20)), ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if c.content != nil {
				f.writeFile(pathForkRelease, *c.content)
			}
			if got := distributionVersion(f.env.Roots); got != c.want {
				t.Errorf("%q, want %q", got, c.want)
			}
		})
	}
	// a marker that is a directory is not one
	f := newFixture(t)
	if err := os.MkdirAll(f.path(pathForkRelease), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := distributionVersion(f.env.Roots); got != "" {
		t.Errorf("a directory: %q", got)
	}
}

func ptr(s string) *string { return &s }

// through the commands: no marker, or one that is no release, refuses the module, and says that the release is unknown; a release that is old enough does not
func TestABoxWhoseReleaseIsNotKnownIsRefusedAsUnknown(t *testing.T) {
	for name, c := range map[string]struct {
		marker  *string
		refused any
		detail  string
	}{
		"no marker":                        {nil, osmodule.CheckReCasaOS, "unknown"},
		"a blank marker":                   {ptr("\n"), osmodule.CheckReCasaOS, "unknown"},
		"a marker that is not a release":   {ptr("latest\n"), osmodule.CheckReCasaOS, "unknown"},
		"a marker with a release and more": {ptr("v0.5.29; id\n"), osmodule.CheckReCasaOS, "unknown"},
		"a release that is too old":        {ptr("v0.5.22\n"), osmodule.CheckReCasaOS, "have v0.5.22 need 0.5.23"},
		"a release that is enough":         {ptr("v0.5.29\n"), nil, ""},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t)
			if c.marker != nil {
				f.writeFile(pathForkRelease, *c.marker)
			}
			f.env.Version = distributionVersion(f.env.Roots)
			plan := f.json("plan", "virt")
			if plan["refused"] != c.refused {
				t.Fatalf("refused: %v, want %v", plan["refused"], c.refused)
			}
			if got := checkOf(plan, osmodule.CheckReCasaOS)["detail"]; c.refused != nil && got != c.detail {
				t.Errorf("the detail: %v, want %q", got, c.detail)
			}
			// the list says the same
			mods := f.json("list")["modules"].([]any)
			sys := mods[0].(map[string]any)["system"].(map[string]any)
			if (c.refused == nil) != (sys["supported"] == true) {
				t.Errorf("the list: %v", sys)
			}
			// ...and an install does not start
			if c.refused != nil {
				before := f.treeWithoutBookkeeping()
				if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.CheckReCasaOS+"\n") {
					t.Errorf("install: exit %d\n%s", code, f.out)
				}
				sameTree(t, before, f.treeWithoutBookkeeping())
			}
		})
	}
}

// the release the binary was built for is a floor for the other users of the marker and for nobody here
func TestTheBuildFloorIsNotTheReleaseOfABox(t *testing.T) {
	if common.FORK_RELEASE_VERSION == "" {
		t.Skip("no floor")
	}
	f := newFixture(t)
	if got := distributionVersion(f.env.Roots); got == strings.TrimPrefix(common.FORK_RELEASE_VERSION, "v") || got == common.FORK_RELEASE_VERSION {
		t.Errorf("a box with no marker has the release %q", got)
	}
}
