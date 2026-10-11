package osreadiness

import (
	"context"
	"testing"
	"time"
)

func TestOSReleaseIsReadTheWayItIsWritten(t *testing.T) {
	got := parseOSRelease("# a comment\nPRETTY_NAME=\"Debian GNU/Linux 11 (bullseye)\"\nID=debian\nVERSION_ID='11'\n\nbroken line\n=novalue\nNAME = \"spaced\"\r\nHOME_URL=\"https://www.debian.org/\"\n")
	for key, want := range map[string]string{
		"PRETTY_NAME": "Debian GNU/Linux 11 (bullseye)", "ID": "debian", "VERSION_ID": "11", "NAME": "spaced", "HOME_URL": "https://www.debian.org/",
	} {
		if got[key] != want {
			t.Errorf("%s = %q, want %q", key, got[key], want)
		}
	}
	if len(got) != 5 {
		t.Errorf("read %v", got)
	}
}

func TestAnOSReleaseThatIsNotInEtcIsLookedForInUsrLib(t *testing.T) {
	f := newBox(t)
	f.remove("/etc/os-release")
	f.set("/usr/lib/os-release", debian11OSRelease)
	if session := identify(context.Background(), f); !session.Identity.Known || session.Hop == nil {
		t.Fatalf("session = %+v", session)
	}
}

func TestTheHopIsTheTablesAndTheLatestReleaseHasNone(t *testing.T) {
	for _, tc := range []struct {
		name   string
		setup  func(*fakeProbe)
		hop    HopID
		to     string
		latest bool
	}{
		{"Debian 11", nil, HopDebian11to12, "bookworm", false},
		{"Debian 12", debian12, HopDebian12to13, "trixie", false},
		{"Ubuntu 22.04", ubuntu2204, HopUbuntu2204to2404, "noble", false},
		{"Ubuntu 24.04", ubuntu2404, HopUbuntu2404to2604, "resolute", false},
		{"Debian 13 is the latest", func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("debian", "13", "trixie", "Debian GNU/Linux 13 (trixie)"))
		}, "", "", true},
		{"Ubuntu 26.04 is the latest", func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("ubuntu", "26.04", "resolute", "Ubuntu 26.04 LTS"))
		}, "", "", true},
		// Ubuntu 20.04 has ended and has no step offered from it: it is not "latest"
		{"Ubuntu 20.04 has no hop and is not the latest", func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("ubuntu", "20.04", "focal", "Ubuntu 20.04.6 LTS"))
		}, "", "", false},
		{"Debian 14 is not in the table", func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("debian", "14", "forky", "Debian GNU/Linux 14 (forky)"))
		}, "", "", false},
		{"Ubuntu 25.10 is not an LTS", func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("ubuntu", "25.10", "questing", "Ubuntu 25.10"))
		}, "", "", false},
		{"a table that has gone stale claims nothing", func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("debian", "13", "trixie", "Debian GNU/Linux 13 (trixie)"))
			f.now = time.Date(2028, 5, 1, 0, 0, 0, 0, time.UTC)
		}, "", "", false},
		{"Linux Mint", func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"21.3\"\nVERSION_CODENAME=virginia\n")
		}, "", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBox(t)
			if tc.setup != nil {
				tc.setup(f)
			}
			session := identify(context.Background(), f)
			if session.HopOf() != tc.hop || session.Latest != tc.latest {
				t.Fatalf("hop, latest = %q, %v; want %q, %v", session.HopOf(), session.Latest, tc.hop, tc.latest)
			}
			if tc.hop != "" && session.Hop.To.Codename != tc.to {
				t.Fatalf("to = %+v", session.Hop.To)
			}
		})
	}
}
