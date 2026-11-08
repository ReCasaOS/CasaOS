package osplan

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
)

// the file as Canonical served it on 2026-11-08: noble and resolute (26.04.1) are both Supported: 1
func metaReleaseFixture(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("testdata/real/ubuntu-meta-release-lts-2026-11-08.txt")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var readAt = time.Date(2026, 11, 8, 10, 13, 43, 0, time.UTC)

func TestCanonicalOffersBothHopsInTheFileOf20261108(t *testing.T) {
	for _, hop := range []HopSpec{jammyToNoble, nobleToResolute} {
		if f := offered(hop, []byte(metaReleaseFixture(t)), readAt); f != nil {
			t.Errorf("%s: %v", hop.ID, f)
		}
	}
}

func TestAHopThatCanonicalDoesNotOfferIsRefused(t *testing.T) {
	file := metaReleaseFixture(t)
	resolute := strings.Index(file, "Dist: resolute")
	last := strings.LastIndex(file, "Supported: 1")
	if resolute < 0 || last < resolute {
		t.Fatal("the fixture does not end with the stanza of resolute")
	}
	for _, tc := range []struct {
		name, body, reason string
		unknown            bool
	}{
		{"no stanza of the target (before 26.04.1, or held back)", file[:resolute], "absent", false},
		{"a stanza that says it is not supported", file[:last] + "Supported: 0" + file[last+len("Supported: 1"):], "unsupported", false},
		{"a later stanza of the target decides", file + "\nDist: resolute\nSupported: 0\n", "unsupported", false},
		{"a file that is not stanzas", "<html>Service Unavailable</html>\n", "", true},
	} {
		f := offered(nobleToResolute, []byte(tc.body), readAt)
		if f == nil || f.Code != CodeNotOffered || f.Data["target"] != "resolute" {
			t.Errorf("%s: %v", tc.name, f)
			continue
		}
		if tc.unknown {
			if f.Data["unknown"] != true {
				t.Errorf("%s: data %v, want unknown", tc.name, f.Data)
			}
			continue
		}
		if f.Data["reason"] != tc.reason || f.Data["read_at"] != "2026-11-08T10:13:43Z" {
			t.Errorf("%s: data %v, want reason %s and the time of the reading", tc.name, f.Data, tc.reason)
		}
	}
}

// ubuntuBox is the box of the tests on Ubuntu 24.04, with the sources of its image: the caller plans the hop (withUbuntuTestHop).
func ubuntuBox(t *testing.T) *testBox {
	t.Helper()
	b := newBox(t)
	b.files["/etc/os-release"] = "ID=ubuntu\nVERSION_ID=\"24.04\"\nVERSION_CODENAME=noble\nPRETTY_NAME=\"Ubuntu 24.04.3 LTS\"\n"
	b.files["/etc/apt/sources.list"] = string(realSource(t, "sources.list", "ubuntu-24.04-image-sources.list").Content)
	b.files["/etc/apt/sources.list.d/ubuntu.sources"] = string(realSource(t, "ubuntu.sources", "ubuntu-24.04-image-ubuntu.sources").Content)
	delete(b.files, "/etc/apt/sources.list.d/docker.list")
	return b
}

func TestThePlanOfAnUbuntuHopAsksCanonicalBeforeTheSandbox(t *testing.T) {
	withUbuntuTestHop(t)
	file := metaReleaseFixture(t)
	withoutResolute := file[:strings.Index(file, "Dist: resolute")]
	for _, tc := range []struct {
		name    string
		fetch   func(context.Context) ([]byte, error)
		refused bool
		unknown bool
	}{
		{"offered", func(context.Context) ([]byte, error) { return []byte(file), nil }, false, false},
		{"not offered", func(context.Context) ([]byte, error) { return []byte(withoutResolute), nil }, true, false},
		{"the file cannot be had", func(context.Context) ([]byte, error) { return nil, errors.New("dial tcp: no route to host") }, true, true},
	} {
		b := ubuntuBox(t)
		sb := newSandbox(b.fakeFiles, realOutputs(t))
		_, f := runPlan(t, b, sb, Options{MetaRelease: tc.fetch})
		switch {
		case !tc.refused:
			if f != nil && f.Code == CodeNotOffered || sb.made != 1 {
				t.Errorf("%s: %v, %d sandboxes made; want the plan to go on to the sandbox", tc.name, f, sb.made)
			}
		case f == nil || f.Code != CodeNotOffered || f.Phase != PhaseSources || (f.Data["unknown"] == true) != tc.unknown:
			t.Errorf("%s: %v; want not_offered in %s, unknown %v", tc.name, f, PhaseSources, tc.unknown)
		case sb.made != 0:
			t.Errorf("%s: a sandbox was made", tc.name)
		}
	}
}

func TestAHopOfDebianDoesNotAskCanonical(t *testing.T) {
	asked := 0
	b := newBox(t)
	sb := newSandbox(b.fakeFiles, realOutputs(t))
	if _, f := runPlan(t, b, sb, Options{MetaRelease: func(context.Context) ([]byte, error) { asked++; return nil, errors.New("no") }}); f != nil {
		t.Fatalf("no plan: %v", f)
	}
	if asked != 0 {
		t.Errorf("Canonical was asked %d times for a hop of Debian", asked)
	}
}
