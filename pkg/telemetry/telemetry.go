// Package telemetry sends ReCasaOS's anonymous usage statistics to PostHog's EU
// cloud: a heartbeat at most once a day, which counts the boxes, and a
// version_changed event once per install or upgrade, which measures how fast
// they update. What is sent, what never is, and the three ways to turn it off:
// https://github.com/ReCasaOS/CasaOS-Install#anonymous-statistics
//
// Two environment variables are for the install check only, never for a box:
// CASAOS_TELEMETRY_ENDPOINT replaces the capture URL, and
// CASAOS_TELEMETRY_START_DELAY (a Go duration, e.g. 0s) the random start delay.
package telemetry

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

const (
	// apiKey is the PostHog project's key: public and write-only, made to be
	// embedded in clients.
	apiKey          = "phc_C5T8QbrhBttzgkWuKm2Xr9S4TLGh3sCVAZai2oaFEuhD"
	defaultEndpoint = "https://eu.i.posthog.com/i/v0/e/"

	endpointEnv   = "CASAOS_TELEMETRY_ENDPOINT"
	startDelayEnv = "CASAOS_TELEMETRY_START_DELAY"

	// StateFile holds the choice, the id and the last heartbeat (root, 0600).
	StateFile = "/var/lib/casaos/telemetry.json"
	// offMarkerFile is left by the installer's --no-telemetry.
	offMarkerFile = "/var/lib/casaos/telemetry-off"
	// upgradedFromFile is written by the installer on every run: the previous
	// release's tag, "upstream" or "new".
	upgradedFromFile = "/var/lib/casaos/upgraded-from"
)

// Telemetry is the statistics of one box.
type Telemetry struct {
	// Root is the filesystem everything is read from and written to: "/" on a
	// box, a fixture directory in tests.
	Root string
	// Endpoint is PostHog's capture URL.
	Endpoint string
	Now      func() time.Time
	// Command runs a program and returns its standard output: systemd-detect-virt.
	Command func(name string, args ...string) ([]byte, error)

	client *http.Client
	mu     sync.Mutex // serialises the read-modify-writes of telemetry.json
}

// Default is the box's own: the API answers from it and main runs its checks.
var Default = New("/")

// New reads the box under root and sends to PostHog, or to
// CASAOS_TELEMETRY_ENDPOINT when the install check sets it.
func New(root string) *Telemetry {
	endpoint := os.Getenv(endpointEnv)
	if endpoint == "" {
		endpoint = defaultEndpoint
	}
	return &Telemetry{
		Root:     root,
		Endpoint: endpoint,
		Now:      time.Now,
		Command: func(name string, args ...string) ([]byte, error) {
			return exec.Command(name, args...).Output()
		},
		// No Transport: http.DefaultTransport, whose proxy comes from the
		// environment (HTTPS_PROXY, NO_PROXY).
		client: &http.Client{Timeout: 10 * time.Second},
	}
}

// path is name, an absolute path on a box, under Root.
func (t *Telemetry) path(name string) string {
	return filepath.Join(t.Root, name)
}
