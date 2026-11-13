package share

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeRuntime writes a runtime directory whose files point at server, with the secret "s3cret".
func fakeRuntime(t *testing.T, urlFile string, server *httptest.Server) func() string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, urlFile), []byte(server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "internal.secret"), []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	return func() string { return dir }
}

func TestGatewayShareAddr(t *testing.T) {
	dir := t.TempDir()
	g := Gateway{RuntimePath: func() string { return dir }}
	if _, err := g.ShareAddr(); !errors.Is(err, ErrGatewayNoShare) {
		t.Fatalf("missing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "share.addr"), []byte("127.0.0.1:41234\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if addr, err := g.ShareAddr(); err != nil || addr != "127.0.0.1:41234" {
		t.Fatalf("got %q, %v", addr, err)
	}
}

func TestGatewaySetAndDeleteHost(t *testing.T) {
	var method, path, body, auth string
	status := http.StatusCreated
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, path, body, auth = r.Method, r.URL.Path, string(b), r.Header.Get("Authorization")
		w.WriteHeader(status)
		if status == http.StatusBadRequest {
			io.WriteString(w, `{"success":400,"message":"bad host"}`)
		}
	}))
	defer server.Close()
	g := Gateway{RuntimePath: fakeRuntime(t, "management.url", server)}
	ctx := context.Background()
	host := "jellyfin.mybox.box.example.org"

	if err := g.SetHost(ctx, host, "http://127.0.0.1:8096"); err != nil {
		t.Fatal(err)
	}
	if method != "PUT" || path != "/v1/gateway/hosts" || auth != "Internal s3cret" ||
		strings.TrimSpace(body) != `{"host":"jellyfin.mybox.box.example.org","target":"http://127.0.0.1:8096"}` {
		t.Fatalf("%s %s %s %q", method, path, body, auth)
	}

	status = http.StatusNoContent
	if err := g.DeleteHost(ctx, host); err != nil {
		t.Fatal(err)
	}
	if method != "DELETE" || path != "/v1/gateway/hosts/"+host || auth != "Internal s3cret" {
		t.Fatalf("%s %s %q", method, path, auth)
	}

	status = http.StatusBadRequest
	if err := g.SetHost(ctx, host, "x"); err == nil || !strings.Contains(err.Error(), "bad host") || strings.Contains(err.Error(), "s3cret") {
		t.Fatalf("400: %v", err)
	}
	for _, s := range []int{http.StatusNotFound, http.StatusMethodNotAllowed} {
		status = s
		if err := g.SetHost(ctx, host, "x"); !errors.Is(err, ErrGatewayNoShare) {
			t.Fatalf("set %d: %v", s, err)
		}
		if err := g.DeleteHost(ctx, host); !errors.Is(err, ErrGatewayNoShare) {
			t.Fatalf("delete %d: %v", s, err)
		}
	}
}

func TestGatewayGuardCalls(t *testing.T) {
	var method, uri, body string
	status := 0 // 0: the route's success
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		method, uri, body = r.Method, r.RequestURI, string(b)
		if status != 0 {
			w.WriteHeader(status)
			io.WriteString(w, `{"success":400,"message":"apps[\"x\"].access: bad"}`)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/v1/gateway/guard":
			io.WriteString(w, `{"version":1,"configured":true,"geo":{"country":true,"asn":false},"people_last_used":{"p_1":"2026-11-12T10:20:00Z"},"waves":[{"app":"jellyfin","bans":23}]}`)
		case r.Method == "GET":
			io.WriteString(w, `[{"key":"203.0.113.9"}]`)
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	g := Gateway{RuntimePath: fakeRuntime(t, "management.url", server)}
	ctx := context.Background()

	secret := make([]byte, 32)
	secret[0] = 1
	cfg := GatewayGuardConfig{Secret: secret, Protection: true, People: []GatewayPerson{},
		Apps: map[string]GatewayGuardApp{"jellyfin.mybox.box.example.org": {App: "jellyfin", Access: "code", Policy: DefaultPolicy()}}}
	if err := g.SetGuard(ctx, cfg); err != nil {
		t.Fatal(err)
	}
	var back map[string]any
	json.Unmarshal([]byte(body), &back)
	if method != "PUT" || uri != "/v1/gateway/guard" || back["secret"] != base64.StdEncoding.EncodeToString(secret) ||
		back["protection"] != true || !strings.Contains(body, `"jellyfin.mybox.box.example.org":{"app":"jellyfin","access":"code","policy":{"waf":"off"`) {
		t.Fatalf("%s %s %s", method, uri, body)
	}

	st, ok, err := g.GuardStatus(ctx)
	if err != nil || !ok || method != "GET" || st.Version != 1 || !st.Configured || !st.Geo.Country || st.Geo.ASN ||
		!st.PeopleLastUsed["p_1"].Equal(time.Date(2026, 11, 12, 10, 20, 0, 0, time.UTC)) || len(st.Waves) != 1 || st.Waves[0] != (GuardWave{App: "jellyfin", Bans: 23}) {
		t.Fatalf("%+v %v %v", st, ok, err)
	}
	if raw, err := g.GuardEvents(ctx, "jellyfin", "waf", 50); err != nil || string(raw) != `[{"key":"203.0.113.9"}]` ||
		uri != "/v1/gateway/guard/events?app=jellyfin&limit=50&reason=waf" {
		t.Fatalf("%s %v %s", raw, err, uri)
	}
	if _, err := g.GuardEvents(ctx, "", "", 0); err != nil || uri != "/v1/gateway/guard/events" {
		t.Fatalf("%v %s", err, uri)
	}
	if raw, err := g.GuardBans(ctx); err != nil || string(raw) != `[{"key":"203.0.113.9"}]` || uri != "/v1/gateway/guard/bans" {
		t.Fatalf("%s %v %s", raw, err, uri)
	}
	if err := g.Unban(ctx, "2001:db8::/64"); err != nil || method != "DELETE" || uri != "/v1/gateway/guard/bans/2001:db8::%2F64" {
		t.Fatalf("%v %s %s", err, method, uri)
	}

	status = http.StatusNotFound // a Gateway without the guard
	if err := g.SetGuard(ctx, cfg); !errors.Is(err, ErrGatewayNoGuard) {
		t.Fatalf("set 404: %v", err)
	}
	if _, ok, err := g.GuardStatus(ctx); ok || err != nil {
		t.Fatalf("status 404: %v %v", ok, err)
	}
	for _, s := range []int{http.StatusBadRequest, http.StatusInternalServerError, http.StatusMethodNotAllowed} {
		status = s
		if err := g.SetGuard(ctx, cfg); err == nil || errors.Is(err, ErrGatewayNoGuard) || errors.Is(err, ErrGatewayNoShare) {
			t.Fatalf("set %d: %v", s, err)
		}
		if _, ok, err := g.GuardStatus(ctx); !ok || err == nil {
			t.Fatalf("status %d: %v %v", s, ok, err)
		}
	}
	status = http.StatusBadRequest
	// a refused config names the field only
	if err := g.SetGuard(ctx, cfg); err == nil || err.Error() != `bad_policy:apps["x"].access` {
		t.Fatalf("400: %v", err)
	}
}
