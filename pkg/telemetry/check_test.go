package telemetry

import (
	"context"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/google/uuid"
)

func TestMain(m *testing.M) {
	// Check logs its failures through CasaOS-Common's logger, nil until initialised.
	logger.LogInitConsoleOnly()
	m.Run()
}

func saveState(t *testing.T, tel *Telemetry, s State) {
	t.Helper()
	if err := tel.save(s); err != nil {
		t.Fatal(err)
	}
}

func TestNothingIsBuiltOrSentWhenStatisticsAreOff(t *testing.T) {
	for _, tc := range []struct{ name, state string }{
		{"turned off", `{"enabled":false,"id":"","notice_seen":true}`},
		{"a malformed state", "{"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, endpoint := newCapture(t, http.StatusOK)
			tel, root := fixture(t, endpoint)
			write(t, root, StateFile, tc.state)
			write(t, root, upgradedFromFile, "v0.4.99\n")
			commands := 0
			tel.Command = func(string, ...string) ([]byte, error) {
				commands++
				return []byte("none\n"), nil
			}

			tel.Check(context.Background())

			if n := len(c.all()); n != 0 {
				t.Fatalf("%d requests, want none", n)
			}
			if commands != 0 {
				t.Fatal("the properties were built")
			}
			if exists(t, root, upgradedFromFile) {
				t.Fatal("upgraded-from was kept")
			}
			if data, _ := os.ReadFile(filepath.Join(root, StateFile)); string(data) != tc.state {
				t.Fatalf("telemetry.json = %s, want it untouched", data)
			}
		})
	}
}

func TestNoHeartbeatWithinADay(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)
	lastSent := testNow.Add(-23 * time.Hour)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: lastSent})

	tel.Check(context.Background())

	if events := c.events(); len(events) != 0 {
		t.Fatalf("sent %v, want nothing", events)
	}
	if got := tel.load().LastSent; !got.Equal(lastSent) {
		t.Fatalf("last_sent = %v, want %v", got, lastSent)
	}
}

func TestAHeartbeatOnceLastSentIsADayOld(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: testNow.Add(-24 * time.Hour)})

	tel.Check(context.Background())

	requests := c.all()
	if len(requests) != 1 || requests[0].body["event"] != "heartbeat" || requests[0].body["distinct_id"] != "kept-id" {
		t.Fatalf("requests = %v, want one heartbeat from kept-id", requests)
	}
	if got := tel.load().LastSent; !got.Equal(testNow) {
		t.Fatalf("last_sent = %v, want %v", got, testNow)
	}
}

func TestTheFirstCheckMakesAndKeepsTheID(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, root := fixture(t, endpoint)

	tel.Check(context.Background()) // no telemetry.json: enabled, a heartbeat is due

	state := tel.load()
	if id, err := uuid.Parse(state.ID); err != nil || id.Version() != 4 {
		t.Fatalf("id = %q, want a UUID v4", state.ID)
	}
	if !state.Enabled || !state.LastSent.Equal(testNow) {
		t.Fatalf("state = %+v, want enabled and last_sent %v", state, testNow)
	}
	requests := c.all()
	if len(requests) != 1 || requests[0].body["distinct_id"] != state.ID {
		t.Fatalf("requests = %v, want one from %s", requests, state.ID)
	}
	info, err := os.Stat(filepath.Join(root, StateFile))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("telemetry.json mode = %v, want 0600", info.Mode().Perm())
	}
}

func TestAFailedSendKeepsLastSentAndUpgradedFrom(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusInternalServerError)
	tel, root := fixture(t, endpoint)
	lastSent := testNow.Add(-48 * time.Hour)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: lastSent})
	write(t, root, upgradedFromFile, "v0.4.99\n")

	tel.Check(context.Background())

	if events := c.events(); !reflect.DeepEqual(events, []string{"version_changed", "heartbeat"}) {
		t.Fatalf("tried %v, want version_changed then heartbeat", events)
	}
	if got := tel.load().LastSent; !got.Equal(lastSent) {
		t.Fatalf("last_sent = %v, want it unchanged at %v", got, lastSent)
	}
	if data, err := os.ReadFile(filepath.Join(root, upgradedFromFile)); err != nil || string(data) != "v0.4.99\n" {
		t.Fatalf("upgraded-from = %q, %v; want it kept", data, err)
	}
}

func TestASentVersionChangedDeletesUpgradedFrom(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, root := fixture(t, endpoint)
	saveState(t, tel, State{Enabled: true, ID: "kept-id", LastSent: testNow.Add(-48 * time.Hour)})
	write(t, root, upgradedFromFile, "v0.4.99\n")

	tel.Check(context.Background())

	requests := c.all()
	if len(requests) != 2 {
		t.Fatalf("%d requests, want version_changed and heartbeat", len(requests))
	}
	changed, _ := requests[0].body["properties"].(map[string]any)
	if requests[0].body["event"] != "version_changed" || changed["previous_distribution"] != "v0.4.99" || changed["distribution"] != "v0.5.0" {
		t.Fatalf("first request = %v, want version_changed from v0.4.99 to v0.5.0", requests[0].body)
	}
	heartbeat, _ := requests[1].body["properties"].(map[string]any)
	if _, ok := heartbeat["previous_distribution"]; requests[1].body["event"] != "heartbeat" || ok {
		t.Fatalf("second request = %v, want a heartbeat without previous_distribution", requests[1].body)
	}
	if exists(t, root, upgradedFromFile) {
		t.Fatal("upgraded-from was kept after a successful send")
	}
	if got := tel.load().LastSent; !got.Equal(testNow) {
		t.Fatalf("last_sent = %v, want %v", got, testNow)
	}
}

func TestStartDelay(t *testing.T) {
	t.Setenv("CASAOS_TELEMETRY_START_DELAY", "0s")
	if got := startDelay(); got != 0 {
		t.Fatalf("startDelay() with 0s = %v, want 0", got)
	}
	t.Setenv("CASAOS_TELEMETRY_START_DELAY", "90s")
	if got := startDelay(); got != 90*time.Second {
		t.Fatalf("startDelay() with 90s = %v, want 90s", got)
	}
	for _, value := range []string{"", "soon", "-5s"} {
		t.Setenv("CASAOS_TELEMETRY_START_DELAY", value)
		for range 100 {
			if got := startDelay(); got < 0 || got >= time.Hour {
				t.Fatalf("startDelay() with %q = %v, want 0 to 60 minutes", value, got)
			}
		}
	}
}

func TestRunChecksOnceTheStartDelayIsOver(t *testing.T) {
	t.Setenv("CASAOS_TELEMETRY_START_DELAY", "0s")
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		tel.Run(ctx)
		close(done)
	}()

	deadline := time.Now().Add(10 * time.Second)
	for len(c.events()) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	<-done

	if events := c.events(); !reflect.DeepEqual(events, []string{"heartbeat"}) {
		t.Fatalf("sent %v, want one heartbeat", events)
	}
}
