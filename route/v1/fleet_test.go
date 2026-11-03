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
	"errors"
	"fmt"
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
	rec := callFleetRec(t, handler, method, target, body, params)
	var result model.Result
	json.Unmarshal(rec.Body.Bytes(), &result)
	return rec.Code, result
}

// callFleetRec is callFleet's recorder: its headers and raw body.
func callFleetRec(t *testing.T, handler echo.HandlerFunc, method, target, body string, params map[string]string) *httptest.ResponseRecorder {
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
	return rec
}

// Through the real Echo router, a crafted {id} must not turn an allowed row
// into another route. Echo decodes ctx.Param("*"), which the hand-built route
// tests never exercise: an allowed path reaches Relay (503, the box is offline),
// an escape is refused before the tunnel (403).
func TestFleetRelayDoesNotLetTheRouterReshapeThePath(t *testing.T) {
	f := useFleetFixture(t)
	if err := f.EnableMaster("https://m.example"); err != nil {
		t.Fatal(err)
	}
	box, _, err := f.AddBox("Maison", "", false)
	if err != nil {
		t.Fatal(err)
	}
	e := echo.New()
	g := e.Group("/v1/fleet")
	g.GET("/boxes/:id/relay/*", FleetRelay)
	g.Match([]string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete}, "/boxes/:id/relay/*", FleetRelay)
	status := func(method, rawURL, body string) int {
		req := httptest.NewRequest(method, rawURL, strings.NewReader(body))
		if body != "" {
			req.Header.Set(echo.HeaderContentType, "application/yaml")
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec.Code
	}
	base := "/v1/fleet/boxes/" + box.ID + "/relay"
	// an allowed row reaches Relay: the box is offline
	if code := status(http.MethodGet, base+"/v1/sys/os", ""); code != http.StatusServiceUnavailable {
		t.Fatalf("an allowed route = %d, want 503 (reached Relay, box offline)", code)
	}
	// escapes are refused before anything is relayed
	for _, esc := range []struct{ method, path string }{
		{http.MethodPut, "/v2/app_management/compose/x%3F/status"},
		{http.MethodPut, "/v2/app_management/compose/x%23/status"},
		{http.MethodPatch, "/v2/app_management/compose/..%2F..%2F..%2Fv1%2Ffile%2Fcontent"},
		{http.MethodPut, "/v2/app_management/compose/../status"},
		{http.MethodPost, "/v2/virt_management/vms/x%3F/start"},
	} {
		if code := status(esc.method, base+esc.path, "image: evil"); code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403", esc.method, esc.path, code)
		}
	}
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
	deadline := time.Now().Add(15 * time.Second) // a bound: CI runs every package at once under -race
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

const (
	relaySecret  = "fleet-secret-5d1e"
	relayJoinKey = "join-key-a90b"
)

// fakeRelay answers every relay request in process, whatever its host (the
// official relay's included), and records the hosts asked. Its login server is
// the host it is asked on. Its refusals quote the secret and the join key,
// which no route may pass on.
type fakeRelay struct {
	mu           sync.Mutex
	hosts        []string
	createStatus int    // when set, what a fleet creation is answered
	keyStatus    int    // when set, what a join key request is answered
	retryAfter   string // the Retry-After of a refusal
	down         bool   // every request fails in transport
}

func (r *fakeRelay) RoundTrip(req *http.Request) (*http.Response, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.hosts = append(r.hosts, req.URL.Host)
	if r.down {
		return nil, errors.New("connection refused")
	}
	rec := httptest.NewRecorder()
	refuse := func(status int) {
		if r.retryAfter != "" {
			rec.Header().Set("Retry-After", r.retryAfter)
		}
		rec.WriteHeader(status)
		rec.WriteString(`{"error":"` + relaySecret + " " + relayJoinKey + `"}`)
	}
	authed := req.Header.Get("Authorization") == "Bearer "+relaySecret
	switch {
	case req.Method == http.MethodPost && req.URL.Path == "/v1/fleets" && r.createStatus != 0:
		refuse(r.createStatus)
	case req.Method == http.MethodPost && req.URL.Path == "/v1/fleets":
		rec.WriteHeader(http.StatusCreated)
		json.NewEncoder(rec).Encode(map[string]any{"fleet_id": "fl1", "secret": relaySecret, "login_server": "https://" + req.URL.Host})
	case req.Method == http.MethodPost && req.URL.Path == "/v1/fleets/fl1/keys" && authed && r.keyStatus != 0:
		refuse(r.keyStatus)
	case req.Method == http.MethodPost && req.URL.Path == "/v1/fleets/fl1/keys" && authed:
		rec.WriteHeader(http.StatusCreated)
		json.NewEncoder(rec).Encode(map[string]any{"key": relayJoinKey, "expires_at": time.Now().Add(time.Hour).UTC().Format(time.RFC3339)})
	case req.Method == http.MethodDelete && authed:
		rec.WriteHeader(http.StatusNoContent)
	default:
		refuse(http.StatusUnauthorized)
	}
	return rec.Result(), nil
}

func (r *fakeRelay) set(change func(*fakeRelay)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	change(r)
}

func (r *fakeRelay) seen() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.hosts...)
}

// fakeNet is the private network: joined to other (another network) when set,
// else to what Up last joined, with the address 100.64.0.7.
type fakeNet struct {
	mu    sync.Mutex
	other string
	upErr error
	login string
	calls []string
}

func (n *fakeNet) State(context.Context) (fleet.NetState, error) {
	n.mu.Lock()
	defer n.mu.Unlock()
	switch {
	case n.other != "":
		return fleet.NetState{Connected: true, Addr: "100.100.1.1", LoginServer: n.other}, nil
	case n.login != "":
		return fleet.NetState{Connected: true, Addr: "100.64.0.7", LoginServer: n.login}, nil
	}
	return fleet.NetState{}, nil
}

func (n *fakeNet) Up(_ context.Context, loginServer, _, _ string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, "up "+loginServer)
	switch {
	case n.other != "":
		return fleet.ErrOtherNetwork
	case n.upErr != nil:
		return n.upErr
	}
	n.login = loginServer
	return nil
}

func (n *fakeNet) Down(context.Context) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.calls = append(n.calls, "down")
	n.login = ""
	return nil
}

func (n *fakeNet) seen() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.calls...)
}

// relayFixture is useFleetFixture with a fake private network and a fake
// relay; a log line that carries the secret or the join key fails the test.
func relayFixture(t *testing.T) (*fleet.Fleet, *fakeNet, *fakeRelay) {
	t.Helper()
	f := useFleetFixture(t)
	n, r := &fakeNet{}, &fakeRelay{}
	f.Net = n
	f.RelayAPI.HTTP = &http.Client{Transport: r}
	f.Log = func(msg string, err error) {
		if line := fmt.Sprint(msg, err); strings.Contains(line, relaySecret) || strings.Contains(line, relayJoinKey) {
			t.Errorf("a log line carries a secret: %s", line)
		}
	}
	return f, n, r
}

func relayCode() string {
	return fleet.Code{MasterURL: "https://100.64.0.9", BoxID: "b", Key: "k", Relay: "https://relay.example", JoinKey: relayJoinKey}.String()
}

func TestPutMasterDefaultsToOfficialRelay(t *testing.T) {
	_, n, r := relayFixture(t)
	code, res := callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true}`, nil)
	if code != http.StatusOK {
		t.Fatalf("enable = %d %q", code, res.Message)
	}
	if hosts := r.seen(); len(hosts) == 0 || hosts[0] != mustParse(fleet.DefaultRelayURL).Host {
		t.Fatalf("the relay asked: %v, want %s", hosts, fleet.DefaultRelayURL)
	}
	if calls := n.seen(); len(calls) != 1 || calls[0] != "up "+fleet.DefaultRelayURL {
		t.Fatalf("network calls %v", calls)
	}
	data := res.Data.(map[string]any)
	if data["relay"] != fleet.DefaultRelayURL || data["master"].(map[string]any)["address"] != "https://100.64.0.7" {
		t.Fatalf("status %v", data)
	}
}

func TestPutMasterAdvancedAddressSkipsRelay(t *testing.T) {
	_, n, r := relayFixture(t)
	code, res := callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"address":"https://m.example"}`, nil)
	if code != http.StatusOK {
		t.Fatalf("enable = %d %q", code, res.Message)
	}
	if len(r.seen()) != 0 || len(n.seen()) != 0 {
		t.Fatalf("advanced mode touched the relay %v or the network %v", r.seen(), n.seen())
	}
	data := res.Data.(map[string]any)
	if data["master"].(map[string]any)["address"] != "https://m.example" {
		t.Fatalf("status %v", data)
	}
	for _, field := range []string{"relay", "net"} {
		if _, has := data[field]; has {
			t.Errorf("advanced mode reports %q: %v", field, data)
		}
	}
}

func TestPutMasterBothAddressAndRelayIs400(t *testing.T) {
	f, n, r := relayFixture(t)
	code, res := callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"address":"https://m.example","relay":"https://relay.example"}`, nil)
	if code != http.StatusBadRequest || res.Message == "" {
		t.Fatalf("both = %d %q, want 400 and a message", code, res.Message)
	}
	if _, on := f.MasterStatus(); on || len(r.seen()) != 0 || len(n.seen()) != 0 {
		t.Fatalf("a refused body changed something: master %v, relay %v, network %v", on, r.seen(), n.seen())
	}
}

func TestOtherNetworkIs409(t *testing.T) {
	f, n, r := relayFixture(t)
	n.other = "https://controlplane.tailscale.com"
	code, res := callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"relay":"https://relay.example"}`, nil)
	if code != http.StatusConflict || res.Message != fleet.ErrOtherNetwork.Error() {
		t.Fatalf("master in another network = %d %q", code, res.Message)
	}
	if _, on := f.MasterStatus(); on || len(r.seen()) != 0 {
		t.Fatalf("master %v, relay asked %v", on, r.seen())
	}
	code, res = callFleet(t, PutFleetMember, http.MethodPut, "/v1/fleet/member", `{"code":"`+relayCode()+`"}`, nil)
	if code != http.StatusConflict || res.Message != fleet.ErrOtherNetwork.Error() {
		t.Fatalf("member in another network = %d %q", code, res.Message)
	}
	if _, on := f.MemberStatus(); on {
		t.Fatal("a refused join was saved")
	}
	// no driver at all: the box cannot do it, readably
	f.Net = nil
	code, res = callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true}`, nil)
	if code != http.StatusServiceUnavailable || !strings.Contains(res.Message, "tailscale is not installed") {
		t.Fatalf("no private network = %d %q", code, res.Message)
	}
}

func TestRelayBusyIs429WithRetryAfter(t *testing.T) {
	_, _, r := relayFixture(t)
	r.set(func(r *fakeRelay) { r.createStatus, r.retryAfter = http.StatusTooManyRequests, "30" })
	rec := callFleetRec(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"relay":"https://relay.example"}`, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "30" {
		t.Fatalf("busy = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
	// a relay that does not say how long: no header
	r.set(func(r *fakeRelay) { r.createStatus, r.retryAfter = http.StatusServiceUnavailable, "" })
	rec = callFleetRec(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"relay":"https://relay.example"}`, nil)
	if rec.Code != http.StatusTooManyRequests || rec.Header().Get("Retry-After") != "" {
		t.Fatalf("busy, no delay = %d, Retry-After %q", rec.Code, rec.Header().Get("Retry-After"))
	}
}

func TestPutRelayChangesRelay(t *testing.T) {
	_, n, r := relayFixture(t)
	if code, res := callFleet(t, PutFleetRelay, http.MethodPut, "/v1/fleet/relay", `{"relay":"https://relay2.example"}`, nil); code != http.StatusBadRequest || res.Message == "" {
		t.Fatalf("change with the master off = %d %q", code, res.Message)
	}
	if code, res := callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"relay":"https://relay.example"}`, nil); code != http.StatusOK {
		t.Fatalf("enable = %d %q", code, res.Message)
	}
	asked := len(r.seen())
	if code, res := callFleet(t, PutFleetRelay, http.MethodPut, "/v1/fleet/relay", `{}`, nil); code != http.StatusBadRequest || res.Message == "" || len(r.seen()) != asked {
		t.Fatalf("no relay = %d %q, relay asked %v", code, res.Message, r.seen())
	}
	code, res := callFleet(t, PutFleetRelay, http.MethodPut, "/v1/fleet/relay", `{"relay":"https://relay2.example"}`, nil)
	if code != http.StatusOK || res.Data.(map[string]any)["relay"] != "https://relay2.example" {
		t.Fatalf("change = %d %q %v", code, res.Message, res.Data)
	}
	if calls := n.seen(); calls[len(calls)-1] != "up https://relay2.example" {
		t.Fatalf("network calls %v", calls)
	}
}

func TestGetFleetReportsRelayAndNeedsRecode(t *testing.T) {
	relayFixture(t)
	callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"relay":"https://relay.example"}`, nil)
	if code, res := callFleet(t, PostFleetBoxes, http.MethodPost, "/v1/fleet/boxes", `{"name":"Maison"}`, nil); code != http.StatusOK {
		t.Fatalf("add box = %d %q", code, res.Message)
	}
	if code, res := callFleet(t, PutFleetRelay, http.MethodPut, "/v1/fleet/relay", `{"relay":"https://relay2.example"}`, nil); code != http.StatusOK {
		t.Fatalf("change = %d %q", code, res.Message)
	}
	_, res := callFleet(t, GetFleet, http.MethodGet, "/v1/fleet", "", nil)
	data := res.Data.(map[string]any)
	net, _ := data["net"].(map[string]any)
	if data["relay"] != "https://relay2.example" || net["connected"] != true || net["addr"] != "100.64.0.7" {
		t.Fatalf("master over a relay: %v", data)
	}
	if box := data["master"].(map[string]any)["boxes"].([]any)[0].(map[string]any); box["needs_recode"] != true {
		t.Fatalf("the box does not ask for a new code: %v", box)
	}
	// a member over a relay
	relayFixture(t)
	if code, res := callFleet(t, PutFleetMember, http.MethodPut, "/v1/fleet/member", `{"code":"`+relayCode()+`"}`, nil); code != http.StatusOK {
		t.Fatalf("join = %d %q", code, res.Message)
	}
	_, res = callFleet(t, GetFleet, http.MethodGet, "/v1/fleet", "", nil)
	data = res.Data.(map[string]any)
	net, _ = data["net"].(map[string]any)
	if data["relay"] != "https://relay.example" || net["connected"] != true {
		t.Fatalf("member over a relay: %v", data)
	}
}

// A dashboard tells a core that knows the relay from an older one by
// default_relay, whatever role the box has.
func TestGetFleetAlwaysReportsDefaultRelay(t *testing.T) {
	check := func(name string) {
		t.Helper()
		_, res := callFleet(t, GetFleet, http.MethodGet, "/v1/fleet", "", nil)
		if got := res.Data.(map[string]any)["default_relay"]; got != fleet.DefaultRelayURL {
			t.Errorf("%s: default_relay = %v, want %s", name, got, fleet.DefaultRelayURL)
		}
	}
	relayFixture(t)
	check("role none")
	if code, res := callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"address":"https://m.example"}`, nil); code != http.StatusOK {
		t.Fatalf("advanced = %d %q", code, res.Message)
	} else if res.Data.(map[string]any)["default_relay"] != fleet.DefaultRelayURL {
		t.Errorf("PUT answer without default_relay: %v", res.Data)
	}
	check("master advanced")
	relayFixture(t)
	callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"relay":"https://relay.example"}`, nil)
	check("master relay")
	relayFixture(t)
	callFleet(t, PutFleetMember, http.MethodPut, "/v1/fleet/member", `{"code":"`+relayCode()+`"}`, nil)
	check("member")
}

// Every refusal of the relay, and a network error that quotes the join key,
// answers a readable status and nothing of the secret or the key.
func TestRouteErrorsCarryNoSecret(t *testing.T) {
	_, _, r := relayFixture(t)
	if code, res := callFleet(t, PutFleetMaster, http.MethodPut, "/v1/fleet/master", `{"enabled":true,"relay":"https://relay.example"}`, nil); code != http.StatusOK {
		t.Fatalf("enable = %d %q", code, res.Message)
	}
	leaks := func(name string, rec *httptest.ResponseRecorder) {
		t.Helper()
		if body := rec.Body.String(); strings.Contains(body, relaySecret) || strings.Contains(body, relayJoinKey) {
			t.Errorf("%s: the answer carries a secret: %s", name, body)
		}
	}
	for _, c := range []struct {
		name      string
		keyStatus int
		down      bool
		want      int
	}{
		{"fleet full", http.StatusConflict, false, http.StatusConflict},
		{"secret refused", http.StatusUnauthorized, false, http.StatusConflict},
		{"fleet gone", http.StatusNotFound, false, http.StatusConflict},
		{"busy", http.StatusTooManyRequests, false, http.StatusTooManyRequests},
		{"another status", http.StatusInternalServerError, false, http.StatusBadRequest},
		{"unreachable", 0, true, http.StatusBadGateway},
	} {
		r.set(func(r *fakeRelay) { r.keyStatus, r.down = c.keyStatus, c.down })
		rec := callFleetRec(t, PostFleetBoxes, http.MethodPost, "/v1/fleet/boxes", `{"name":"Maison"}`, nil)
		if rec.Code != c.want {
			t.Errorf("%s = %d, want %d: %s", c.name, rec.Code, c.want, rec.Body)
		}
		leaks(c.name, rec)
	}
	rec := callFleetRec(t, PutFleetRelay, http.MethodPut, "/v1/fleet/relay", `{"relay":"https://relay2.example"}`, nil)
	if rec.Code != http.StatusBadGateway {
		t.Errorf("change to an unreachable relay = %d, want 502", rec.Code)
	}
	leaks("change relay", rec)
	// a member whose network quotes the join key in its error
	_, n, _ := relayFixture(t)
	n.upErr = errors.New("tailscale up --authkey=" + relayJoinKey + ": refused")
	rec = callFleetRec(t, PutFleetMember, http.MethodPut, "/v1/fleet/member", `{"code":"`+relayCode()+`"}`, nil)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("join, network refused = %d, want 400", rec.Code)
	}
	leaks("join", rec)
}
