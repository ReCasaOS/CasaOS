package v1

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// fakeOSService is a box with the os-release, the files and the clock of the test, the rest of the
// service being the real answer of ossupport.
type fakeOSService struct {
	service.SystemService
	release map[string]string
	files   []string // the paths that exist on the box, besides os-release
	arch    string
	now     time.Time
}

func (f *fakeOSService) GetSystemOS() ossupport.Report {
	return ossupport.Describe(f.release, f.arch, f.now, func(path string) bool { return slices.Contains(f.files, path) })
}

// The envelope is the other handlers', and the data is exactly what the spec shows, for the boxes
// that matter: the owner's Debian 11, the same with a table that has gone stale, a Raspberry Pi OS
// 64-bit image (Debian's own os-release, told by /etc/rpi-issue: no claim and no next release), an
// Ubuntu that is ending (to the month), and a Mint, which is not known.
func TestGetSystemOSAnswersWithTheEnvelopeAndTheExactData(t *testing.T) {
	original := service.MyService
	t.Cleanup(func() { service.MyService = original })

	for _, tc := range []struct {
		name string
		box  *fakeOSService
		want string
	}{
		{
			"Debian 11",
			&fakeOSService{
				release: map[string]string{"ID": "debian", "VERSION_ID": "11", "VERSION_CODENAME": "bullseye", "PRETTY_NAME": "Debian GNU/Linux 11 (bullseye)"},
				arch:    "amd64", now: time.Date(2026, 10, 10, 9, 30, 0, 0, time.UTC),
			},
			`{"id":"debian","version_id":"11","codename":"bullseye","pretty_name":"Debian GNU/Linux 11 (bullseye)","arch":"amd64","known":true,` +
				`"support":{"state":"security_ended","ends":"2026-08-31","precision":"day","extended":{"name":"Debian ELTS (Freexian)","ends":"2031-06-30","precision":"day"},"source":"https://www.debian.org/News/2026/20260831"},` +
				`"next":{"id":"debian","version_id":"12","codename":"bookworm","pretty_name":"Debian 12 (bookworm)","state":"report"}}`,
		},
		{
			"Debian 11, with a table that has gone stale: it has still ended",
			&fakeOSService{
				release: map[string]string{"ID": "debian", "VERSION_ID": "11", "VERSION_CODENAME": "bullseye", "PRETTY_NAME": "Debian GNU/Linux 11 (bullseye)"},
				arch:    "amd64", now: time.Date(2028, 5, 1, 9, 30, 0, 0, time.UTC),
			},
			`{"id":"debian","version_id":"11","codename":"bullseye","pretty_name":"Debian GNU/Linux 11 (bullseye)","arch":"amd64","known":true,` +
				`"support":{"state":"security_ended","ends":"2026-08-31","precision":"day","source":"https://www.debian.org/News/2026/20260831"}}`,
		},
		{
			"Raspberry Pi OS 64-bit, which says Debian",
			&fakeOSService{
				release: map[string]string{"ID": "debian", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm", "PRETTY_NAME": "Debian GNU/Linux 12 (bookworm)"},
				files:   []string{"/etc/rpi-issue"},
				arch:    "arm64", now: time.Date(2026, 10, 10, 9, 30, 0, 0, time.UTC),
			},
			`{"id":"debian","version_id":"12","codename":"bookworm","pretty_name":"Debian GNU/Linux 12 (bookworm)","arch":"arm64","known":false,"variant":"raspberrypi","support":{"state":"unknown"}}`,
		},
		{
			"Ubuntu 22.04, ending",
			&fakeOSService{
				release: map[string]string{"ID": "ubuntu", "ID_LIKE": "debian", "VERSION_ID": "22.04", "VERSION_CODENAME": "jammy", "PRETTY_NAME": "Ubuntu 22.04.5 LTS"},
				arch:    "amd64", now: time.Date(2027, 1, 15, 9, 30, 0, 0, time.UTC),
			},
			`{"id":"ubuntu","version_id":"22.04","codename":"jammy","pretty_name":"Ubuntu 22.04.5 LTS","arch":"amd64","known":true,` +
				`"support":{"state":"ending_soon","ends":"2027-05-31","precision":"month","extended":{"name":"Ubuntu Pro (ESM)","ends":"2032-04-21","precision":"month"},"source":"https://ubuntu.com/about/release-cycle"},` +
				`"next":{"id":"ubuntu","version_id":"24.04","codename":"noble","pretty_name":"Ubuntu 24.04 LTS (noble)","state":"report"}}`,
		},
		{
			"a Linux Mint",
			&fakeOSService{
				release: map[string]string{"ID": "linuxmint", "ID_LIKE": "ubuntu debian", "VERSION_ID": "21.3", "VERSION_CODENAME": "virginia", "PRETTY_NAME": "Linux Mint 21.3"},
				arch:    "amd64", now: time.Date(2026, 10, 10, 9, 30, 0, 0, time.UTC),
			},
			`{"id":"linuxmint","version_id":"21.3","codename":"virginia","pretty_name":"Linux Mint 21.3","arch":"amd64","known":false,"support":{"state":"unknown"}}`,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service.MyService = fakeSystemPackageRepository{system: tc.box}
			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodGet, "/v1/sys/os", nil)

			if err := GetSystemOS(echo.New().NewContext(request, recorder)); err != nil {
				t.Fatalf("GetSystemOS() error = %v", err)
			}

			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d", recorder.Code)
			}
			var envelope struct {
				Success int             `json:"success"`
				Message string          `json:"message"`
				Data    json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatalf("decode %q: %v", recorder.Body.String(), err)
			}
			if envelope.Success != http.StatusOK || envelope.Message != "ok" {
				t.Fatalf("envelope = %d %q, want 200 \"ok\"", envelope.Success, envelope.Message)
			}
			if string(envelope.Data) != tc.want {
				t.Fatalf("data:\n got %s\nwant %s", envelope.Data, tc.want)
			}
		})
	}
}
