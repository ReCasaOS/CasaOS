package service

import (
	"encoding/json"
	"errors"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
)

func TestSystemOSReadsTheReleaseAndTheDay(t *testing.T) {
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	read := func() (map[string]string, error) {
		return map[string]string{"ID": "debian", "VERSION_ID": "11", "VERSION_CODENAME": "bullseye", "PRETTY_NAME": "Debian GNU/Linux 11 (bullseye)"}, nil
	}

	got := systemOS(read, "amd64", now)

	if !got.Known || got.ID != "debian" || got.VersionID != "11" || got.Arch != "amd64" || got.Support.State != ossupport.SecurityEnded || got.Next == nil || got.Next.VersionID != "12" {
		t.Fatalf("systemOS = %+v", got)
	}
	// the day is the one it was given, and a clock from before the table was written is not believed
	if got := systemOS(read, "amd64", time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC)); got.Support.State != ossupport.Unknown {
		t.Errorf("before the table was written: %s", got.Support.State)
	}
}

func TestSystemOSOfAFileThatCannotBeReadIsNotKnown(t *testing.T) {
	read := func() (map[string]string, error) {
		// a reader that fails may still hand back a half-read map: it is not to be believed
		return map[string]string{"ID": "debian", "VERSION_ID": "11"}, errors.New("read /etc/os-release: permission denied")
	}

	got := systemOS(read, "amd64", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))

	body, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"arch":"amd64","known":false,"support":{"state":"unknown"}}`; string(body) != want {
		t.Fatalf("systemOS = %s, want %s", body, want)
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
