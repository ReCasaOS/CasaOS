package fleet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

var testNow = time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

// verifyBoxRequest is the relay's rule for POST /v1/fleets, copied from recasaos-relay
// internal/auth (VerifyBox) without importing it: strict raw base64url, a 32-byte key and a
// 64-byte signature, a time within 120 s, and the signature over "POST /v1/fleets\n"+time as sent.
// Left out: the relay's small-order key check, which needs filippo.io/edwards25519; a key made by
// ed25519.GenerateKey is never one.
func verifyBoxRequest(r *http.Request, now time.Time) error {
	enc := base64.RawURLEncoding.Strict()
	if r.Method != http.MethodPost || r.URL.Path != "/v1/fleets" {
		return errors.New("not a create-fleet request")
	}
	pub, err := enc.DecodeString(r.Header.Get("X-Box-Key"))
	if err != nil || len(pub) != ed25519.PublicKeySize {
		return errors.New("bad X-Box-Key")
	}
	sig, err := enc.DecodeString(r.Header.Get("X-Box-Signature"))
	if err != nil || len(sig) != ed25519.SignatureSize {
		return errors.New("bad X-Box-Signature")
	}
	tsStr := r.Header.Get("X-Box-Time")
	ts, err := strconv.ParseInt(tsStr, 10, 64)
	if err != nil {
		return errors.New("bad X-Box-Time")
	}
	if d := now.Sub(time.Unix(ts, 0)); d > 120*time.Second || d < -120*time.Second {
		return errors.New("time outside the window")
	}
	if !ed25519.Verify(pub, []byte("POST /v1/fleets\n"+tsStr), sig) {
		return errors.New("bad signature")
	}
	return nil
}

func jsonReply(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

// relayFor serves h and returns a client wired to it and the server's address.
func relayFor(t *testing.T, h http.HandlerFunc) (*RelayClient, string) {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &RelayClient{HTTP: srv.Client(), Now: func() time.Time { return testNow }, Dir: t.TempDir()}, srv.URL
}

func TestCreateFleetSignsTheRequest(t *testing.T) {
	var pubs []string
	c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
		if err := verifyBoxRequest(r, testNow); err != nil {
			t.Errorf("the relay would refuse this request: %v", err)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		if r.Header.Get("Authorization") != "" {
			t.Error("create-fleet carries no Authorization")
		}
		if r.Header.Get("X-Box-Time") != strconv.FormatInt(testNow.Unix(), 10) {
			t.Errorf("X-Box-Time = %q, want the client's clock", r.Header.Get("X-Box-Time"))
		}
		for _, h := range []string{"X-Box-Key", "X-Box-Signature"} {
			if strings.ContainsAny(r.Header.Get(h), "=+/") {
				t.Errorf("%s is not raw base64url: %q", h, r.Header.Get(h))
			}
		}
		pubs = append(pubs, r.Header.Get("X-Box-Key"))
		jsonReply(w, http.StatusCreated, map[string]any{"fleet_id": "f1", "secret": "s3", "login_server": "https://relay.example.org", "expires_in_days_idle": 30})
	})
	for range 2 {
		got, err := c.CreateFleet(context.Background(), relay)
		if err != nil || got != (FleetCreds{FleetID: "f1", Secret: "s3", LoginServer: "https://relay.example.org"}) {
			t.Fatalf("CreateFleet = %+v, %v", got, err)
		}
	}
	if len(pubs) != 2 || pubs[0] != pubs[1] {
		t.Fatalf("the box signs with its one key, got %v", pubs)
	}
}

func TestCreateFleetRefusesAnInvalidAnswer(t *testing.T) {
	for name, body := range map[string]string{
		"no fleet":         `{"secret":"s","login_server":"https://r.example.org"}`,
		"no secret":        `{"fleet_id":"f","login_server":"https://r.example.org"}`,
		"no login server":  `{"fleet_id":"f","secret":"s"}`,
		"plain http login": `{"fleet_id":"f","secret":"s","login_server":"http://evil.example.org"}`,
		"not json":         `<html>captive portal</html>`,
		"too big":          `{"fleet_id":"f","secret":"s","login_server":"https://r.example.org","pad":"` + strings.Repeat("x", 70<<10) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(body))
			})
			if got, err := c.CreateFleet(context.Background(), relay); err == nil {
				t.Fatalf("accepted: %+v", got)
			}
		})
	}
}

func TestJoinKeyUsesBearerSecret(t *testing.T) {
	c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.EscapedPath() != "/v1/fleets/f%2F1/keys" || r.Header.Get("Authorization") != "Bearer s3cret" {
			t.Errorf("%s %s auth %q", r.Method, r.URL.EscapedPath(), r.Header.Get("Authorization"))
		}
		if r.Header.Get("X-Box-Signature") != "" {
			t.Error("a join key is asked with the secret, not a box signature")
		}
		jsonReply(w, http.StatusCreated, map[string]string{"key": realJoinKey, "expires_at": "2026-11-04T12:00:00Z"})
	})
	key, exp, err := c.JoinKey(context.Background(), relay, "f/1", "s3cret")
	if err != nil || key != realJoinKey || !exp.Equal(testNow.Add(24*time.Hour)) {
		t.Fatalf("JoinKey = %q %v %v", key, exp, err)
	}
}

func TestJoinKeyRefusesAnInvalidAnswer(t *testing.T) {
	for name, body := range map[string]string{
		"no key":      `{"expires_at":"2026-11-04T12:00:00Z"}`,
		"bad expires": `{"key":"k","expires_at":"tomorrow"}`,
		"file key":    `{"key":"file:/etc/shadow","expires_at":"2026-11-04T12:00:00Z"}`,
		"flag key":    `{"key":"--reset-everything-now","expires_at":"2026-11-04T12:00:00Z"}`,
		"short key":   `{"key":"k","expires_at":"2026-11-04T12:00:00Z"}`,
	} {
		t.Run(name, func(t *testing.T) {
			c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusCreated)
				w.Write([]byte(body))
			})
			if _, _, err := c.JoinKey(context.Background(), relay, "f", "s"); err == nil {
				t.Fatal("accepted")
			}
		})
	}
}

func TestRemoveNodeAndDeleteFleetRequests(t *testing.T) {
	var seen []string
	c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
		seen = append(seen, r.Method+" "+r.URL.EscapedPath()+" "+r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.RemoveNode(context.Background(), relay, "f1", "sec", "box 1"); err != nil {
		t.Fatal(err)
	}
	if err := c.DeleteFleet(context.Background(), relay, "f1", "sec"); err != nil {
		t.Fatal(err)
	}
	want := []string{"DELETE /v1/fleets/f1/nodes/box%201 Bearer sec", "DELETE /v1/fleets/f1 Bearer sec"}
	if strings.Join(seen, "|") != strings.Join(want, "|") {
		t.Fatalf("requests %q, want %q", seen, want)
	}
}

func TestRelayErrorsAreTyped(t *testing.T) {
	ctx := context.Background()
	calls := map[string]func(c *RelayClient, relay string) error{
		"create": func(c *RelayClient, relay string) error { _, err := c.CreateFleet(ctx, relay); return err },
		"key":    func(c *RelayClient, relay string) error { _, _, err := c.JoinKey(ctx, relay, "f", "s"); return err },
		"node":   func(c *RelayClient, relay string) error { return c.RemoveNode(ctx, relay, "f", "s", "h") },
		"delete": func(c *RelayClient, relay string) error { return c.DeleteFleet(ctx, relay, "f", "s") },
	}
	for _, tc := range []struct {
		status  int
		retry   string
		want    error         // nil: no sentinel expected
		after   time.Duration // RetryAfter expected on a busy error
		calls   []string
		generic bool // an error naming only the status
	}{
		{429, "7", ErrRelayBusy, 7 * time.Second, []string{"create", "key", "node", "delete"}, false},
		{429, "", ErrRelayBusy, 0, []string{"key"}, false},
		{429, "soon", ErrRelayBusy, 0, []string{"key"}, false},
		{503, "3", ErrRelayBusy, 3 * time.Second, []string{"create", "key", "node", "delete"}, false},
		{502, "", ErrRelayBusy, 0, []string{"create", "key", "node", "delete"}, false},
		{409, "", ErrRelayQuota, 0, []string{"key"}, false},
		{401, "", ErrRelayAuth, 0, []string{"create", "key", "node", "delete"}, false},
		{404, "", ErrRelayFleetGone, 0, []string{"key", "node"}, false},
		{404, "", nil, 0, []string{"create"}, true},
		{500, "", ErrRelayFailed, 0, []string{"create", "key", "node", "delete"}, true},
		{504, "", ErrRelayFailed, 0, []string{"key"}, true},
		{400, "", nil, 0, []string{"node"}, true},
		{200, "", nil, 0, []string{"key", "node", "delete"}, true}, // not the status the contract promises
	} {
		for _, name := range tc.calls {
			t.Run(strconv.Itoa(tc.status)+"/"+tc.retry+"/"+name, func(t *testing.T) {
				c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
					if tc.retry != "" {
						w.Header().Set("Retry-After", tc.retry)
					}
					w.WriteHeader(tc.status)
				})
				err := calls[name](c, relay)
				if err == nil {
					t.Fatal("no error")
				}
				if tc.want != nil && !errors.Is(err, tc.want) {
					t.Fatalf("%v is not %v", err, tc.want)
				}
				if tc.generic && !strings.Contains(err.Error(), strconv.Itoa(tc.status)) {
					t.Fatalf("%q does not name the status", err)
				}
				if tc.want == ErrRelayBusy {
					var busy *RelayBusyError
					if !errors.As(err, &busy) || busy.RetryAfter != tc.after {
						t.Fatalf("RetryAfter of %v = %v, want %v", err, busy, tc.after)
					}
				}
			})
		}
	}
}

// The relay refuses a signature stamped too far from its own clock with a 401:
// the error says to check this box's clock, and still reads as ErrRelayAuth.
func TestCreateFleet401NamesTheClock(t *testing.T) {
	c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusUnauthorized) })
	_, err := c.CreateFleet(context.Background(), relay)
	if !errors.Is(err, ErrRelayAuth) || !strings.Contains(err.Error(), "clock") {
		t.Fatalf("got %v", err)
	}
}

func TestDeleteFleet404IsSuccess(t *testing.T) {
	c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, http.StatusNotFound, map[string]string{"error": "fleet not found"})
	})
	if err := c.DeleteFleet(context.Background(), relay, "f", "s"); err != nil {
		t.Fatalf("an already deleted fleet is a success, got %v", err)
	}
}

func TestRefusesPlainHTTPRelay(t *testing.T) {
	c := &RelayClient{HTTP: &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		t.Errorf("a request left for %s", r.URL.Host)
		return nil, errors.New("must not be called")
	})}, Dir: t.TempDir()}
	ctx := context.Background()
	if _, err := c.CreateFleet(ctx, "http://relay.example.org"); err == nil {
		t.Error("CreateFleet over plain http")
	}
	if _, _, err := c.JoinKey(ctx, "http://relay.example.org", "f", "s"); err == nil {
		t.Error("JoinKey over plain http")
	}
	if err := c.RemoveNode(ctx, "http://relay.example.org", "f", "s", "h"); err == nil {
		t.Error("RemoveNode over plain http")
	}
	if err := c.DeleteFleet(ctx, "http://relay.example.org", "f", "s"); err == nil {
		t.Error("DeleteFleet over plain http")
	}
	if _, err := os.Stat(filepath.Join(c.Dir, boxKeyFile)); err == nil {
		t.Error("a refused relay still made the box key")
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestRedirectIsNotFollowed(t *testing.T) {
	hit := false
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer other.Close()
	c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL, http.StatusTemporaryRedirect)
	})
	err := c.DeleteFleet(context.Background(), relay, "f", "s")
	if err == nil || !strings.Contains(err.Error(), "307") || hit {
		t.Fatalf("err %v, redirect target hit %v", err, hit)
	}
	c.HTTP = nil // the default client must not follow either
	if err := c.DeleteFleet(context.Background(), relay, "f", "s"); err == nil || hit {
		t.Fatalf("default client: err %v, hit %v", err, hit)
	}
}

func TestUnreachableRelay(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	c := &RelayClient{HTTP: srv.Client(), Dir: t.TempDir()}
	url := srv.URL
	srv.Close()
	err := c.DeleteFleet(context.Background(), url, "f1", "s")
	if !errors.Is(err, ErrRelayUnreachable) {
		t.Fatalf("got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := c.JoinKey(ctx, url, "f1", "s"); !errors.Is(err, ErrRelayUnreachable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("the cause stays reachable with errors.Is, got %v", err)
	}
}

func TestNoSecretInErrors(t *testing.T) {
	const secret, joinKey = "S3CRET-fleet-secret", "JOINKEY-hskey-value"
	var echoed []string // everything the relay saw, to be echoed back
	handler := func(status int, body func() string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			for _, h := range []string{"Authorization", "X-Box-Signature", "X-Box-Key"} {
				w.Header().Set("X-Echo-"+h, r.Header.Get(h))
				echoed = append(echoed, r.Header.Get(h))
			}
			w.Header().Set("Retry-After", secret)
			w.WriteHeader(status)
			w.Write([]byte(body()))
		}
	}
	everything := func() string { return secret + " " + joinKey + " " + strings.Join(echoed, " ") }
	servers := map[string]http.HandlerFunc{}
	for _, st := range []int{400, 401, 404, 409, 429, 500, 502, 503} {
		servers["status "+strconv.Itoa(st)] = handler(st, func() string { return `{"error":"` + everything() + `"}` })
	}
	servers["201 invalid login server"] = handler(201, func() string {
		return `{"fleet_id":"` + secret + `","secret":"` + secret + `","login_server":"http://` + secret + `"}`
	})
	servers["201 invalid key"] = handler(201, func() string { return `{"key":"` + joinKey + `","expires_at":"` + secret + `"}` })
	servers["201 garbage"] = handler(201, everything)
	servers["204 echo"] = handler(204, everything)

	ctx := context.Background()
	for name, h := range servers {
		t.Run(name, func(t *testing.T) {
			c, relay := relayFor(t, h)
			echoed = nil
			var errs []error
			_, err := c.CreateFleet(ctx, relay)
			errs = append(errs, err)
			_, _, err = c.JoinKey(ctx, relay, "f1", secret)
			errs = append(errs, err)
			errs = append(errs, c.RemoveNode(ctx, relay, "f1", secret, "h"))
			errs = append(errs, c.DeleteFleet(ctx, relay, "f1", secret))

			forbidden := append([]string{secret, joinKey, base64.RawURLEncoding.EncodeToString([]byte(secret))}, echoed...)
			key, kerr := loadBoxKey(c.Dir)
			if kerr != nil {
				t.Fatal(kerr)
			}
			forbidden = append(forbidden,
				base64.RawURLEncoding.EncodeToString(key), base64.RawURLEncoding.EncodeToString(key.Seed()),
				base64.StdEncoding.EncodeToString(key), base64.StdEncoding.EncodeToString(key.Seed()))
			for _, err := range errs {
				if err == nil {
					continue
				}
				for _, bad := range forbidden {
					if bad != "" && strings.Contains(err.Error(), bad) {
						t.Errorf("error %q contains %q", err, bad)
					}
				}
			}
		})
	}
	t.Run("connection dropped", func(t *testing.T) {
		c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
			conn, _, _ := w.(http.Hijacker).Hijack()
			conn.Close()
		})
		for _, err := range []error{c.DeleteFleet(ctx, relay, "f1", secret), c.RemoveNode(ctx, relay, "f1", secret, "h")} {
			if !errors.Is(err, ErrRelayUnreachable) || strings.Contains(err.Error(), secret) {
				t.Errorf("got %v", err)
			}
		}
	})
}

func TestCreateFleetNeedsADirectory(t *testing.T) {
	c := &RelayClient{HTTP: &http.Client{}}
	if _, err := c.CreateFleet(context.Background(), "https://relay.example.org"); err == nil {
		t.Fatal("no Dir, no box key")
	}
}

func plain404(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)
	w.Write([]byte("404 page not found\n"))
}

func TestDeviceCode(t *testing.T) {
	var gotPath, gotAuth, gotMethod string
	reply := func(status int, v any) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			gotPath, gotAuth, gotMethod = r.URL.Path, r.Header.Get("Authorization"), r.Method
			jsonReply(w, status, v)
		}
	}
	c, relay := relayFor(t, reply(201, map[string]string{"code": "ABCD-EFGH", "expires_at": "2026-11-03T12:10:00Z"}))
	got, err := c.DeviceCode(context.Background(), relay, "f1", "s3")
	if err != nil || got.Code != "ABCD-EFGH" || !got.ExpiresAt.Equal(testNow.Add(10*time.Minute)) {
		t.Fatalf("DeviceCode = %+v, %v", got, err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v1/fleets/f1/device-codes" || gotAuth != "Bearer s3" {
		t.Fatalf("request = %s %s %q", gotMethod, gotPath, gotAuth)
	}
	for _, code := range []string{"abcd-efgh", "ABCD-EFG", "ABCD-EFGO", "ABCD-EFG1", "ABCDEFGH", ""} {
		c, relay := relayFor(t, reply(201, map[string]string{"code": code, "expires_at": "2026-11-03T12:10:00Z"}))
		if _, err := c.DeviceCode(context.Background(), relay, "f1", "s3"); err == nil || !strings.Contains(err.Error(), "not valid") {
			t.Errorf("code %q: err = %v, want a 'not valid' error", code, err)
		}
	}
	for status, want := range map[int]error{409: ErrTooManyDeviceCodes, 401: ErrRelayAuth, 429: ErrRelayBusy} {
		c, relay := relayFor(t, reply(status, map[string]string{"error": "x"}))
		if _, err := c.DeviceCode(context.Background(), relay, "f1", "s3"); !errors.Is(err, want) {
			t.Errorf("status %d: err = %v, want %v", status, err, want)
		}
	}
}

func TestDeviceCodeOldRelay(t *testing.T) {
	calls := map[string]func(c *RelayClient, relay string) error{
		"DeviceCode": func(c *RelayClient, relay string) error {
			_, err := c.DeviceCode(context.Background(), relay, "f1", "s3")
			return err
		},
		"Devices": func(c *RelayClient, relay string) error {
			_, err := c.Devices(context.Background(), relay, "f1", "s3")
			return err
		},
		"RemoveDevice": func(c *RelayClient, relay string) error {
			return c.RemoveDevice(context.Background(), relay, "f1", "s3", "12")
		},
		"FleetNodes": func(c *RelayClient, relay string) error {
			_, err := c.FleetNodes(context.Background(), relay, "f1", "s3")
			return err
		},
	}
	for name, call := range calls {
		t.Run(name, func(t *testing.T) {
			want := ErrRelayNoDevices
			if name == "FleetNodes" { // its path exists on every relay version: any 404 is a gone fleet
				want = ErrRelayFleetGone
			}
			c, relay := relayFor(t, plain404)
			if err := call(c, relay); !errors.Is(err, want) {
				t.Errorf("plain 404: err = %v, want %v", err, want)
			}
			c, relay = relayFor(t, func(w http.ResponseWriter, r *http.Request) {
				jsonReply(w, 404, map[string]string{"error": "fleet not found"})
			})
			if err := call(c, relay); !errors.Is(err, ErrRelayFleetGone) {
				t.Errorf("fleet not found: err = %v, want ErrRelayFleetGone", err)
			}
		})
	}
}

func TestDevices(t *testing.T) {
	var gotPath, gotMethod string
	c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		jsonReply(w, 200, map[string]any{"devices": []map[string]any{
			{"id": "12", "hostname": "iphone", "ip": "100.64.0.9", "online": true, "last_seen": "2026-11-03T11:00:00Z"},
			{"id": "13", "hostname": "pc", "ip": "100.64.0.10", "online": false, "last_seen": ""},
		}})
	})
	got, err := c.Devices(context.Background(), relay, "f1", "s3")
	if err != nil || len(got) != 2 {
		t.Fatalf("Devices = %+v, %v", got, err)
	}
	if gotMethod != http.MethodGet || gotPath != "/v1/fleets/f1/devices" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
	if got[0].ID != "12" || got[0].Hostname != "iphone" || got[0].IP != "100.64.0.9" || !got[0].Online || !got[0].LastSeen.Equal(testNow.Add(-time.Hour)) {
		t.Errorf("device 0 = %+v", got[0])
	}
	if got[1].ID != "13" || got[1].Online || !got[1].LastSeen.IsZero() {
		t.Errorf("device 1 = %+v", got[1])
	}

	c, relay = relayFor(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"devices":null}`))
	})
	got, err = c.Devices(context.Background(), relay, "f1", "s3")
	if err != nil || got == nil || len(got) != 0 {
		t.Fatalf("null devices = %#v, %v, want an empty non-nil slice", got, err)
	}
}

func TestRemoveDevice(t *testing.T) {
	var reqs []string
	c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
		reqs = append(reqs, r.Method+" "+r.URL.Path+" "+r.Header.Get("Authorization"))
		w.WriteHeader(http.StatusNoContent)
	})
	if err := c.RemoveDevice(context.Background(), relay, "f1", "s3", "12"); err != nil {
		t.Fatal(err)
	}
	if len(reqs) != 1 || reqs[0] != "DELETE /v1/fleets/f1/devices/12 Bearer s3" {
		t.Fatalf("requests = %q", reqs)
	}
	for _, id := range []string{"1/2", "a", "", "../x"} {
		if err := c.RemoveDevice(context.Background(), relay, "f1", "s3", id); err == nil {
			t.Errorf("id %q accepted", id)
		}
	}
	if len(reqs) != 1 {
		t.Fatalf("a bad id reached the relay: %q", reqs)
	}
}

func TestFleetNodes(t *testing.T) {
	var gotPath string
	c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		jsonReply(w, 200, map[string]any{"nodes": []map[string]any{
			{"hostname": "box1", "ip": "100.64.0.1", "online": true},
			{"hostname": "box2", "ip": "100.64.0.2", "online": false},
		}, "nodes_max": 50, "state": "ok"})
	})
	got, err := c.FleetNodes(context.Background(), relay, "f1", "s3")
	want := []FleetNode{{"box1", "100.64.0.1", true}, {"box2", "100.64.0.2", false}}
	if err != nil || len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("FleetNodes = %+v, %v", got, err)
	}
	if gotPath != "/v1/fleets/f1" {
		t.Fatalf("path = %q", gotPath)
	}
}

func TestCreateFleetRelayFull(t *testing.T) {
	for body, wantFull := range map[string]bool{
		`{"error":"relay full, retry later"}`: true,
		`{"error":"overloaded"}`:              false,
		`busy`:                                false,
	} {
		c, relay := relayFor(t, func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			w.Write([]byte(body))
		})
		_, err := c.CreateFleet(context.Background(), relay)
		if wantFull && (!errors.Is(err, ErrRelayFull) || errors.Is(err, ErrRelayBusy)) {
			t.Errorf("%s: err = %v, want ErrRelayFull only", body, err)
		}
		if !wantFull && !errors.Is(err, ErrRelayBusy) {
			t.Errorf("%s: err = %v, want ErrRelayBusy", body, err)
		}
	}
}
