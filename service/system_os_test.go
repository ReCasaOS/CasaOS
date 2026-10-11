package service

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
)

// none is a box that has none of the files that tell a variant.
func none(string) bool { return false }

func TestSystemOSReadsTheReleaseAndTheDay(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	read := func() (map[string]string, error) {
		return map[string]string{"ID": "debian", "VERSION_ID": "11", "VERSION_CODENAME": "bullseye", "PRETTY_NAME": "Debian GNU/Linux 11 (bullseye)"}, nil
	}

	got := systemOS(read, none, "amd64", now)

	if !got.Known || got.ID != "debian" || got.VersionID != "11" || got.Arch != "amd64" || got.Support.State != ossupport.SecurityEnded || got.Next == nil || got.Next.VersionID != "12" {
		t.Fatalf("systemOS = %+v", got)
	}
	// the day is the one it was given, and a clock from before the table was written is not believed
	if got := systemOS(read, none, "amd64", time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); got.Support.State != ossupport.Unknown {
		t.Errorf("before the table was written: %s", got.Support.State)
	}
}

func TestSystemOSOfAFileThatCannotBeReadIsNotKnown(t *testing.T) {
	read := func() (map[string]string, error) {
		// a reader that fails may still hand back a half-read map: it is not to be believed
		return map[string]string{"ID": "debian", "VERSION_ID": "11"}, errors.New("read /etc/os-release: permission denied")
	}

	asked := false
	got := systemOS(read, func(string) bool { asked = true; return true }, "amd64", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	if asked {
		t.Error("the box was asked about its files with no os-release to say what it is")
	}

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"arch":"amd64","known":false,"support":{"state":"unknown"}}`; string(body) != want {
		t.Fatalf("systemOS = %s, want %s", body, want)
	}
}

// A box that says Debian and has the marker of a variant is not known, whichever variant it is: the
// answer says which, claims nothing and offers no next release. The files are looked for through the
// seam, so that no real file is needed.
func TestSystemOSOfAVariantIsNotKnown(t *testing.T) {
	read := func() (map[string]string, error) {
		return map[string]string{"ID": "debian", "VERSION_ID": "11", "VERSION_CODENAME": "bullseye", "PRETTY_NAME": "Debian GNU/Linux 11 (bullseye)"}, nil
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	for variant, path := range map[string]string{
		"raspberrypi":    "/etc/rpi-issue",
		"armbian":        "/etc/armbian-release",
		"dietpi":         "/boot/dietpi.txt",
		"proxmox":        "/usr/bin/pveversion",
		"openmediavault": "/etc/openmediavault",
		"proxmox_backup": "/usr/sbin/proxmox-backup-manager",
		"proxmox_mail":   "/usr/bin/pmgversion",
		"truenas":        "/usr/bin/middlewared",
	} {
		t.Run(variant, func(t *testing.T) {
			body, err := json.Marshal(systemOS(read, func(p string) bool { return p == path }, "arm64", now))
			if err != nil {
				t.Fatal(err)
			}
			want := `{"id":"debian","version_id":"11","codename":"bullseye","pretty_name":"Debian GNU/Linux 11 (bullseye)","arch":"arm64","known":false,"variant":"` + variant + `","support":{"state":"unknown"}}`
			if string(body) != want {
				t.Fatalf("systemOS = %s, want %s", body, want)
			}
		})
	}
}

// What the seam says is true of the real machine: a file and a directory exist, what is not there
// does not.
func TestPathExists(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "marker")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]bool{dir: true, file: true, filepath.Join(dir, "missing"): false, filepath.Join(file, "below"): false} {
		if got := pathExists(path); got != want {
			t.Errorf("pathExists(%q) = %v, want %v", path, got, want)
		}
		// and it is what the method looks with
		if got := markerExists(path); got != want {
			t.Errorf("markerExists(%q) = %v, want %v", path, got, want)
		}
	}
}

// The method reads os-release and looks for the files of a variant through the two things it is
// given: a Debian whose /etc/rpi-issue exists is a Raspberry Pi OS, and a box that has no such file is
// the plain Debian.
func TestGetSystemOSUsesTheFilesOfTheMachine(t *testing.T) {
	readBefore, existsBefore := readOSRelease, markerExists
	t.Cleanup(func() { readOSRelease, markerExists = readBefore, existsBefore })
	readOSRelease = func() (map[string]string, error) {
		return map[string]string{"ID": "debian", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"}, nil
	}

	markerExists = func(path string) bool { return path == "/etc/rpi-issue" }
	if got := (&systemService{}).GetSystemOS(); got.Known || got.Variant != "raspberrypi" {
		t.Errorf("with /etc/rpi-issue: %+v, want a Raspberry Pi OS that is not known", got)
	}
	markerExists = func(string) bool { return false }
	if got := (&systemService{}).GetSystemOS(); !got.Known || got.Variant != "" {
		t.Errorf("with none of the files: %+v, want the plain Debian", got)
	}
}

// The method itself reads the real file of the machine the test runs on: whatever it is, the answer
// is whole, and the architecture is Go's.
func TestGetSystemOSAnswersForThisMachine(t *testing.T) {
	got := (&systemService{}).GetSystemOS()
	if got.Arch != runtime.GOARCH {
		t.Errorf("arch = %q, want %q", got.Arch, runtime.GOARCH)
	}
	if got.Support.State == "" {
		t.Errorf("no state: %+v", got)
	}
	if got.Known && !strings.Contains("debian ubuntu", got.ID) {
		t.Errorf("known with an ID of %q", got.ID)
	}
}
