package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/common"
	"go.uber.org/zap/zapcore"
)

var testNow = time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

// capture stands for PostHog: it records every request and answers a status.
type capture struct {
	mu       sync.Mutex
	requests []captured
}

type captured struct {
	method, contentType string
	body                map[string]any
}

func newCapture(t *testing.T, status int) (*capture, string) {
	t.Helper()
	c := &capture{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("the request body is not JSON: %v", err)
		}
		c.mu.Lock()
		c.requests = append(c.requests, captured{r.Method, r.Header.Get("Content-Type"), body})
		c.mu.Unlock()
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return c, server.URL
}

func (c *capture) all() []captured {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]captured(nil), c.requests...)
}

// events is the event name of every request, in order.
func (c *capture) events() []string {
	var names []string
	for _, r := range c.all() {
		name, _ := r.body["event"].(string)
		names = append(names, name)
	}
	return names
}

// fixture is a box with a release marker and an os-release, sending to
// endpoint at testNow.
func fixture(t *testing.T, endpoint string) (*Telemetry, string) {
	t.Helper()
	root := t.TempDir()
	write(t, root, common.FORK_RELEASE_FILE, "v0.5.0\n")
	write(t, root, "/etc/os-release", "ID=debian\nVERSION_ID=\"12\"\n")
	tel := New(root)
	tel.Endpoint = endpoint
	tel.Now = func() time.Time { return testNow }
	tel.Command = virt("none\n", nil)
	return tel, root
}

// asJSON is v as the capture server decodes it: numbers become float64.
func asJSON(t *testing.T, v any) any {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	return decoded
}

func TestTheRequestIsWhatPostHogExpects(t *testing.T) {
	c, endpoint := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, endpoint)
	properties := tel.Properties()

	if err := tel.send(context.Background(), "heartbeat", "0b0f4d8e-6d7a-4f55-9a53-5b8c1f3e2a10", properties); err != nil {
		t.Fatal(err)
	}

	requests := c.all()
	if len(requests) != 1 {
		t.Fatalf("%d requests, want 1", len(requests))
	}
	r := requests[0]
	if r.method != http.MethodPost || r.contentType != "application/json" {
		t.Fatalf("request = %s with Content-Type %q, want POST application/json", r.method, r.contentType)
	}
	if keys := slices.Sorted(maps.Keys(r.body)); !reflect.DeepEqual(keys, []string{"api_key", "distinct_id", "event", "properties", "timestamp"}) {
		t.Fatalf("body keys = %v", keys)
	}
	if r.body["api_key"] != "phc_C5T8QbrhBttzgkWuKm2Xr9S4TLGh3sCVAZai2oaFEuhD" {
		t.Fatalf("api_key = %v", r.body["api_key"])
	}
	if r.body["event"] != "heartbeat" || r.body["distinct_id"] != "0b0f4d8e-6d7a-4f55-9a53-5b8c1f3e2a10" || r.body["timestamp"] != "2026-09-22T12:00:00Z" {
		t.Fatalf("event, distinct_id, timestamp = %v, %v, %v", r.body["event"], r.body["distinct_id"], r.body["timestamp"])
	}
	sent, _ := r.body["properties"].(map[string]any)
	if sent["$process_person_profile"] != false || sent["$lib"] != "recasaos-core" {
		t.Fatalf("$process_person_profile, $lib = %v, %v", sent["$process_person_profile"], sent["$lib"])
	}
	if _, ok := sent["$geoip_disable"]; ok {
		t.Fatal("$geoip_disable is sent: PostHog could not derive the country")
	}
	if want := asJSON(t, properties); !reflect.DeepEqual(any(sent), want) {
		t.Fatalf("properties sent =\n%v\nwant\n%v", sent, want)
	}
}

func TestAnAnswerOutside2xxIsAFailure(t *testing.T) {
	cases := []struct {
		status int
		fails  bool
	}{
		{http.StatusOK, false},
		{http.StatusNoContent, false},
		{http.StatusBadRequest, true},
		{http.StatusUnauthorized, true},
		{http.StatusInternalServerError, true},
		{http.StatusServiceUnavailable, true},
	}
	for _, tc := range cases {
		_, endpoint := newCapture(t, tc.status)
		tel, _ := fixture(t, endpoint)
		err := tel.send(context.Background(), "heartbeat", "id", map[string]any{})
		if (err != nil) != tc.fails {
			t.Errorf("status %d: send() error = %v, want a failure: %v", tc.status, err, tc.fails)
		}
	}
}

func TestATransportErrorIsAFailure(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	server.Close() // nothing listens there any more
	tel, _ := fixture(t, server.URL)
	if err := tel.send(context.Background(), "heartbeat", "id", map[string]any{}); err == nil {
		t.Fatal("send() to a closed server succeeded")
	}
}

func TestTheEndpointIsPostHogEUUnlessTheInstallCheckSetsOne(t *testing.T) {
	t.Setenv("CASAOS_TELEMETRY_ENDPOINT", "")
	if got := New("/").Endpoint; got != "https://eu.i.posthog.com/i/v0/e/" {
		t.Fatalf("Endpoint = %q, want PostHog EU", got)
	}
	t.Setenv("CASAOS_TELEMETRY_ENDPOINT", "http://127.0.0.1:8999/capture")
	if got := New("/").Endpoint; got != "http://127.0.0.1:8999/capture" {
		t.Fatalf("Endpoint = %q, want the test setting", got)
	}
}

func TestTheClientWaitsTenSecondsThroughTheEnvironmentProxy(t *testing.T) {
	client := New("/").client
	if client.Timeout != 10*time.Second {
		t.Fatalf("Timeout = %v, want 10s", client.Timeout)
	}
	// A nil Transport is http.DefaultTransport, whose Proxy is http.ProxyFromEnvironment.
	if client.Transport != nil {
		t.Fatalf("Transport = %T, want nil (http.DefaultTransport)", client.Transport)
	}
}

// syncBuffer is a log the test reads while the logger writes to it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestASuccessfulSendIsLoggedAndAFailedOneIsNot(t *testing.T) {
	var out syncBuffer
	logger.LogInitWithWriterSyncers(zapcore.AddSync(&out))
	t.Cleanup(logger.LogInitConsoleOnly)

	_, ok := newCapture(t, http.StatusOK)
	tel, _ := fixture(t, ok)
	if err := tel.send(context.Background(), "heartbeat", "kept-id", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	_, refused := newCapture(t, http.StatusInternalServerError)
	tel.Endpoint = refused
	if err := tel.send(context.Background(), "version_changed", "kept-id", map[string]any{}); err == nil {
		t.Fatal("a 500 was taken for a success")
	}

	log := out.String()
	if !bytes.Contains([]byte(log), []byte("telemetry: sent")) || !bytes.Contains([]byte(log), []byte(`"event": "heartbeat"`)) {
		t.Fatalf("the log does not say the heartbeat was sent:\n%s", log)
	}
	if bytes.Contains([]byte(log), []byte(`"event": "version_changed"`)) {
		t.Fatalf("a failed send was logged as sent:\n%s", log)
	}
}
