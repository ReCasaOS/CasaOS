package osreadiness

import (
	"context"
	"errors"
	"io/fs"
	"slices"
	"testing"
)

// What the report says of a box that is not one the upgrade is for, or has nothing to upgrade to:
// the reason only, and no other check is run.
func TestABoxTheUpgradeIsNotForIsReportedWithItsReasonsOnly(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeProbe)
		codes []string
	}{
		{"an unknown system", func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=fedora\nVERSION_ID=\"41\"\n")
		}, []string{"unsupported_os=refuse"}},
		{"a derivative that says what it is built on", func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nVERSION_ID=\"21.3\"\nVERSION_CODENAME=virginia\n")
		}, []string{"unsupported_os=refuse"}},
		{"Debian testing, which has no VERSION_ID", func(f *fakeProbe) {
			f.set("/etc/os-release", "ID=debian\nPRETTY_NAME=\"Debian GNU/Linux forky/sid\"\n")
		}, []string{"unsupported_os=refuse"}},
		{"a release that has no upgrade from here", func(f *fakeProbe) {
			f.set("/etc/os-release", osRelease("ubuntu", "20.04", "focal", "Ubuntu 20.04.6 LTS"))
		}, []string{"unsupported_os=refuse"}},
		{"Raspberry Pi OS, which says Debian", func(f *fakeProbe) {
			f.set("/etc/rpi-issue", "Raspberry Pi reference 2024-03-15")
		}, []string{"vendor_os=refuse"}},
		{"an arm64 machine", func(f *fakeProbe) { f.arch = "arm64" }, []string{"unproven_arch=refuse"}},
		{"a Raspberry Pi 64-bit, which is a variant and arm64", func(f *fakeProbe) {
			f.set("/etc/rpi-issue", "x")
			f.arch = "arm64"
		}, []string{"unproven_arch=refuse", "vendor_os=refuse"}},
		{"a system whose os-release cannot be read", func(f *fakeProbe) {
			f.fail("/etc/os-release", errors.New("input/output error"))
		}, []string{"unsupported_os=refuse"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newBox(t)
			tc.setup(f)
			report := Run(context.Background(), f, Options{})
			if report.State != StateUnsupported || report.Hop != nil {
				t.Fatalf("state, hop = %s, %v; findings %v", report.State, report.Hop, codesOf(report))
			}
			got := codesOf(report)
			slices.Sort(got)
			if !slices.Equal(got, tc.codes) {
				t.Fatalf("findings = %v, want %v", got, tc.codes)
			}
			if report.Counts.Refuse != len(tc.codes) {
				t.Fatalf("counts = %+v", report.Counts)
			}
			// nothing past the identity was asked of the machine
			for _, argv := range f.attemptedCommands() {
				if !slices.Equal(argv, argvArch) {
					t.Fatalf("a check that is not of the identity ran %v", argv)
				}
			}
		})
	}
}

func TestTheLatestReleaseHasNothingToUpgradeTo(t *testing.T) {
	f := newBox(t)
	f.set("/etc/os-release", osRelease("debian", "13", "trixie", "Debian GNU/Linux 13 (trixie)"))
	report := Run(context.Background(), f, Options{})
	if report.State != StateLatest || len(report.Checks) != 0 || report.Hop != nil || report.Counts != (Counts{}) {
		t.Fatalf("report = %+v", report)
	}
	if report.Checks == nil {
		t.Fatal("the checks of a report are a list, never null")
	}
	for _, argv := range f.attemptedCommands() {
		if !slices.Equal(argv, argvArch) {
			t.Fatalf("a check that is not of the identity ran %v", argv)
		}
	}
}

func TestAMarkerThatCannotBeLookedAtIsNotAnAnswer(t *testing.T) {
	f := newBox(t)
	f.fail("/etc/rpi-issue", &fs.PathError{Op: "stat", Path: "/etc/rpi-issue", Err: fs.ErrPermission})
	report := Run(context.Background(), f, Options{})
	finding := findingOf(t, report, "vendor_os")
	if report.State != StateUnsupported || finding.Data["unknown"] != true || finding.Data["reason"] != ReasonUnreadable {
		t.Fatalf("state %s, vendor_os %+v", report.State, finding)
	}
}
