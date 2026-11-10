package share

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
