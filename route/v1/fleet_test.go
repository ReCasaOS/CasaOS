package v1

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fleet"
	"github.com/labstack/echo/v4"
)

// writeSelfSignedCert writes a self-signed certificate PEM and returns its path.
func writeSelfSignedCert(t *testing.T) string {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "gateway.crt")
	if err := os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// useFleetFixture swaps fleet.Default for a fresh one rooted in a temp dir,
// with a default gateway layout and a self-signed certificate, and restores it.
func useFleetFixture(t *testing.T) *fleet.Fleet {
	t.Helper()
	f := fleet.New(t.TempDir())
	cert := writeSelfSignedCert(t)
	f.Gateway = func() (fleet.GatewayInfo, error) {
		return fleet.GatewayInfo{Public: mustParse("http://127.0.0.1:80"), HTTPS: mustParse("https://127.0.0.1:443"), CertFile: cert}, nil
	}
	f.Hostname = func() (string, error) { return "box", nil }
	f.Version = func() string { return "v1.0.0" }
	original := fleet.Default
	fleet.Default = f
	t.Cleanup(func() { fleet.Default = original })
	return f
}

func mustParse(s string) *url.URL {
	u, _ := url.Parse(s)
	return u
}

// call runs a fleet handler with the method, target and body, setting any path
// params the route carries, and returns the status and the decoded envelope.
func callFleet(t *testing.T, handler echo.HandlerFunc, method, target, body string, params map[string]string) (int, model.Result) {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	names, values := []string{}, []string{}
	for k, v := range params {
		names = append(names, k)
		values = append(values, v)
	}
	ctx.SetParamNames(names...)
	ctx.SetParamValues(values...)
	if err := handler(ctx); err != nil {
		t.Fatalf("%s %s: %v", method, target, err)
	}
	var result model.Result
	json.Unmarshal(rec.Body.Bytes(), &result)
	return rec.Code, result
}

func TestFleetStatusRoles(t *testing.T) {
	useFleetFixture(t)
	if _, r := callFleet(t, GetFleet, http.MethodGet, "/v1/fleet", "", nil); r.Data.(map[string]any)["role"] != "none" {
		t.Fatalf("fresh role = %v", r.Data)
	}
	if code, _ := callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"address":"https://m.example"}`, nil); code != http.StatusOK {
		t.Fatalf("enable master = %d", code)
	}
	if _, r := callFleet(t, GetFleet, http.MethodGet, "/v1/fleet", "", nil); r.Data.(map[string]any)["role"] != "master" {
		t.Fatalf("role after enable = %v", r.Data)
	}
	if code, r := callFleet(t, PostFleetBoxes, http.MethodPost, "/v1/fleet/boxes", `{"name":"this box","self":true}`, nil); code != http.StatusOK || r.Data.(map[string]any)["code"] == "" {
		t.Fatalf("self-enrol = %d %v", code, r.Data)
	}
	_, r := callFleet(t, GetFleet, http.MethodGet, "/v1/fleet", "", nil)
	data := r.Data.(map[string]any)
	if data["role"] != "master" || data["master"] == nil || data["member"] == nil {
		t.Fatalf("after self-enrol the member block is missing: %v", data)
	}
}

func TestFleetBoxesCRUDAndCodeOnce(t *testing.T) {
	useFleetFixture(t)
	callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"address":"https://m.example"}`, nil)
	code, r := callFleet(t, PostFleetBoxes, http.MethodPost, "/v1/fleet/boxes", `{"name":"Maison"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("add box = %d", code)
	}
	box := r.Data.(map[string]any)["box"].(map[string]any)
	id := box["id"].(string)
	if r.Data.(map[string]any)["code"].(string) == "" {
		t.Fatal("no code")
	}
	// GET /v1/fleet never leaks a code or a key
	_, status := callFleet(t, GetFleet, http.MethodGet, "/v1/fleet", "", nil)
	raw, _ := json.Marshal(status)
	if strings.Contains(string(raw), "code") || strings.Contains(string(raw), "\"key\"") || strings.Contains(string(raw), "key_sha256") {
		t.Fatalf("GET /v1/fleet leaks a secret: %s", raw)
	}
	name := "Bureau"
	if code, _ := callFleet(t, PatchFleetBox, http.MethodPatch, "/v1/fleet/boxes/"+id, `{"name":"Bureau"}`, map[string]string{"id": id}); code != http.StatusOK {
		t.Fatalf("rename = %d", code)
	}
	_ = name
	if code, _ := callFleet(t, PostFleetBoxCode, http.MethodPost, "/v1/fleet/boxes/"+id+"/code", "", map[string]string{"id": id}); code != http.StatusOK {
		t.Fatalf("rotate = %d", code)
	}
	if code, _ := callFleet(t, DeleteFleetBox, http.MethodDelete, "/v1/fleet/boxes/"+id, "", map[string]string{"id": id}); code != http.StatusOK {
		t.Fatalf("delete = %d", code)
	}
	if code, _ := callFleet(t, DeleteFleetBox, http.MethodDelete, "/v1/fleet/boxes/"+id, "", map[string]string{"id": id}); code != http.StatusNotFound {
		t.Fatalf("delete of a gone box = %d, want 404", code)
	}
}

func TestFleetMemberJoinAndLeave(t *testing.T) {
	f := useFleetFixture(t)
	if code, _ := callFleet(t, PutFleetMember, http.MethodPut, "/v1/fleet/member", `{"code":"!!!"}`, nil); code != http.StatusBadRequest {
		t.Fatalf("bad code = %d, want 400", code)
	}
	c := fleet.Code{MasterURL: "https://m.example", BoxID: "b", Key: "k"}
	if code, r := callFleet(t, PutFleetMember, http.MethodPut, "/v1/fleet/member", `{"code":"`+c.String()+`"}`, nil); code != http.StatusOK || r.Data.(map[string]any)["role"] != "member" {
		t.Fatalf("join = %d %v", code, r.Data)
	}
	if code, r := callFleet(t, DeleteFleetMember, http.MethodDelete, "/v1/fleet/member", "", nil); code != http.StatusOK || r.Data.(map[string]any)["role"] != "none" {
		t.Fatalf("leave = %d %v", code, r.Data)
	}
	_ = f
}

func TestFleetRelayMapsErrorsToStatuses(t *testing.T) {
	useFleetFixture(t)
	// role off: no box
	if code, _ := callFleet(t, FleetRelay, http.MethodGet, "/v1/fleet/boxes/x/relay/v1/sys/os", "", map[string]string{"id": "x", "*": "v1/sys/os"}); code != http.StatusNotFound {
		t.Fatalf("relay role off = %d, want 404", code)
	}
	callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"address":"https://m.example"}`, nil)
	_, r := callFleet(t, PostFleetBoxes, http.MethodPost, "/v1/fleet/boxes", `{"name":"Maison"}`, nil)
	id := r.Data.(map[string]any)["box"].(map[string]any)["id"].(string)
	// enrolled but offline
	if code, res := callFleet(t, FleetRelay, http.MethodGet, "/v1/fleet/boxes/"+id+"/relay/v1/sys/os", "", map[string]string{"id": id, "*": "v1/sys/os"}); code != http.StatusServiceUnavailable || res.Message != fleet.ErrOffline.Error() {
		t.Fatalf("relay offline = %d %q", code, res.Message)
	}
	// a route outside the table
	if code, res := callFleet(t, FleetRelay, http.MethodGet, "/v1/fleet/boxes/"+id+"/relay/v1/file/content", "", map[string]string{"id": id, "*": "v1/file/content"}); code != http.StatusForbidden || res.Message != fleet.ErrNotAllowed.Error() {
		t.Fatalf("relay forbidden = %d %q", code, res.Message)
	}
	// an unknown box
	if code, _ := callFleet(t, FleetRelay, http.MethodGet, "/v1/fleet/boxes/nobody/relay/v1/sys/os", "", map[string]string{"id": "nobody", "*": "v1/sys/os"}); code != http.StatusNotFound {
		t.Fatalf("relay unknown box = %d, want 404", code)
	}
}

func TestFleetRelayBodyLimit(t *testing.T) {
	useFleetFixture(t)
	callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"address":"https://m.example"}`, nil)
	_, r := callFleet(t, PostFleetBoxes, http.MethodPost, "/v1/fleet/boxes", `{"name":"Maison"}`, nil)
	id := r.Data.(map[string]any)["box"].(map[string]any)["id"].(string)
	big := strings.Repeat("x", fleet.MaxBody+1)
	req := httptest.NewRequest(http.MethodPost, "/v1/fleet/boxes/"+id+"/relay/v2/app_management/image-updates", strings.NewReader(big))
	rec := httptest.NewRecorder()
	ctx := echo.New().NewContext(req, rec)
	ctx.SetParamNames("id", "*")
	ctx.SetParamValues(id, "v2/app_management/image-updates")
	if err := FleetRelay(ctx); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body = %d, want 413", rec.Code)
	}
}

// fakeGateway records requests and answers a fixed response.
type fakeGateway struct {
	mu   sync.Mutex
	got  int
	seen string
}

func (g *fakeGateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	g.mu.Lock()
	g.got++
	g.seen = r.URL.RequestURI()
	g.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"ok":true}`))
}

func TestFleetRelayPassesTheAnswerThrough(t *testing.T) {
	f := useFleetFixture(t)
	gw := &fakeGateway{}
	cert := writeSelfSignedCert(t)
	mux := http.NewServeMux()
	var handlers sync.WaitGroup
	mux.HandleFunc("/v1/fleet/tunnel", func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		f.HandleTunnel(w, r)
	})
	mux.Handle("/", gw)
	srv := httptest.NewServer(mux)
	f.Gateway = func() (fleet.GatewayInfo, error) {
		return fleet.GatewayInfo{Public: mustParse(srv.URL), CertFile: cert}, nil
	}
	runtime := t.TempDir()
	if err := os.WriteFile(filepath.Join(runtime, external.InternalSecretFilename), []byte("s3cret"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.RuntimePath = func() string { return runtime }
	if err := f.EnableMaster(srv.URL); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.Run(ctx); close(done) }()
	// Run before the temp dirs go: stop the member, stop new dials, close the
	// tunnels and wait for their handlers, so none writes into a deleted dir.
	t.Cleanup(func() {
		cancel()
		<-done
		srv.Close()
		f.DisableMaster()
		handlers.Wait()
	})
	box, _, err := f.AddBox("this box", "", true)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		st, _ := f.MasterStatus()
		if len(st.Boxes) > 0 && st.Boxes[0].Online {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the self box never came online")
		}
		time.Sleep(10 * time.Millisecond)
	}
	code, res := callFleet(t, FleetRelay, http.MethodGet, "/v1/fleet/boxes/"+box.ID+"/relay/v1/sys/os?token=x", "", map[string]string{"id": box.ID, "*": "v1/sys/os"})
	if code != http.StatusOK {
		t.Fatalf("relay = %d %q", code, res.Message)
	}
	gw.mu.Lock()
	got, seen := gw.got, gw.seen
	gw.mu.Unlock()
	if got != 1 || seen != "/v1/sys/os" {
		t.Fatalf("the gateway saw %d requests, last %q", got, seen)
	}
}
