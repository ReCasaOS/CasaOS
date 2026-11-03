# Fleet (core) Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** A box can be the master of a fleet, every other box joins it through an outbound tunnel, and the master relays an allow-listed set of each box's own API routes so one dashboard can see and act on all of them.

**Architecture:** One package `pkg/fleet` in the core holds both roles. A member keeps a WebSocket open to `wss://<master>/v1/fleet/tunnel` and executes the requests the master sends over it against its own local gateway with the box's internal-service identity, after checking them against a fixed route table. The master exposes `/v1/fleet/...` to the dashboard: the box list, enrolment, and `/boxes/{id}/relay/*` which forwards into the tunnel. State lives in two JSON files under `/var/lib/casaos/fleet/`. The only autonomous behaviour is one "box offline" alert in the existing hub.

**Tech Stack:** Go 1.26 (`go.mod`), Echo v4, `gorilla/websocket` (already a dependency), `go-ini` (already a dependency), the core's `pkg/utils/file.WriteFileAtomic`, CasaOS-Common `external` (internal secret), `pkg/alerts`.

**Spec:** `docs/superpowers/specs/2026-11-02-fleet-design.md` (French). The dashboard side is a separate plan, `2026-11-02-fleet-ui.md`, which consumes the API this plan builds.

## Global Constraints

- No new Go dependency; no new component; nothing in the installer except the install-check step and a README line.
- State files: `/var/lib/casaos/fleet/master.json` and `/var/lib/casaos/fleet/member.json`, root, 0600, written with `file.WriteFileAtomic`; a missing file is "role off"; an unreadable or unparsable file is "role off" plus one `logger.Error` line.
- Never a key, a code or the internal secret in a log line, an error message, a URL or a response other than the one that hands the code over.
- The route table is the only thing that passes through the relay, checked on the master **and** on the member; everything else is 403. The query parameter `token` is always dropped.
- Timeouts: 60 s per relayed request, 300 s for the routes marked slow; 16 requests in flight per box (503 beyond); request and response bodies at most `4 << 20` bytes.
- Tunnel auth header: `Authorization: Fleet <box_id>:<key>`; keys are 32 random bytes, hex; the master stores `sha256(key)` hex only; comparison with `crypto/subtle.ConstantTimeCompare`.
- Master URL in a code must be `https://…`, or `http://` with a loopback host (tests and the install check only), the same rule as `pkg/autoupdate`'s URL overrides.
- API envelope `model.Result{Success, Message, Data}` with `common_err` codes like `route/v1/autoupdate.go`; every `/v1/fleet` route is in the JWT group except `GET /v1/fleet/tunnel`; routes that change something take `headerTokenOnly`.
- Commit messages: conventional (`feat(fleet): …`), **no `Co-Authored-By` trailer** (owner's rule).
- Work on a branch `feat/fleet` cut from `inkly/main` of `D:\clients\casaos\CasaOS`; the install-check task works in `D:\clients\casaos\get` on a branch of the same name.

## Review Focus

1. A member that is also the master (self-enrolled) must keep serving its own tunnel while the master role is disabled and re-enabled: `TestDisableClosesTunnelsAndKeepsMemberIdle` (Task 7).
2. A relayed `GET` whose path has a trailing `/`, a doubled `/`, `..` or percent-encoded slashes must not slip past the table: `TestAllowedNormalisesNothing` (Task 1).
3. A box that answers the hello frame with a hostname of 10 000 characters, or a response frame bigger than the limit, must not take the master down: `TestOversizedFramesAreDroppedWithTheConnection` (Task 6).
4. The master's address changed in `master.json` by hand to `http://evil` must not be dialled by members: codes are what members trust, and a member re-validates the URL rule on load: `TestLoadRefusesANonHTTPSMasterOutsideLoopback` (Task 3).
5. A member whose gateway listens on a non-loopback `address` must say so in its status instead of looping on connection errors: `TestGatewayURLRefusesANonLoopbackAddress` (Task 5).

---

### Task 1: Branch, package and the route table

**Files:**
- Create: `pkg/fleet/fleet.go` (package doc only for now), `pkg/fleet/allow.go`, `pkg/fleet/allow_test.go`

**Interfaces:**
- Produces: `func Allowed(method, path string) (slow, ok bool)` — `path` is the request path only (no query); `slow` is true for `GET /v1/sys/packages`, `GET /v2/app_management/compose/updates`, `POST /v2/app_management/image-updates`. `const MaxBody = 4 << 20`. `func Timeout(slow bool) time.Duration` → 300 s or 60 s.

- [ ] **Step 1: Cut the branch**

```bash
cd D:\clients\casaos\CasaOS && git fetch inkly && git checkout -b feat/fleet inkly/main
```

- [ ] **Step 2: Write the failing tests** in `pkg/fleet/allow_test.go`

```go
func TestAllowedTable(t *testing.T) {
	for _, c := range []struct{ method, path string; slow, ok bool }{
		{"GET", "/v1/sys/version/check", false, true},
		{"GET", "/v1/sys/os", false, true},
		{"GET", "/v1/sys/utilization", false, true},
		{"GET", "/v1/sys/modules", false, true},
		{"GET", "/v1/sys/autoupdate", false, true},
		{"GET", "/v1/sys/packages", true, true},
		{"GET", "/v1/sys/packages/update/status", false, true},
		{"GET", "/v1/sys/docker/update/status", false, true},
		{"GET", "/v2/casaos/health/services", false, true},
		{"GET", "/v2/app_management/compose", false, true},
		{"GET", "/v2/app_management/web/appgrid", false, true},
		{"GET", "/v2/app_management/operations", false, true},
		{"GET", "/v2/app_management/compose/updates", true, true},
		{"GET", "/v2/app_management/compose/updates/run", false, true},
		{"GET", "/v2/virt_management/vms", false, true},
		{"POST", "/v1/sys/update", false, true},
		{"POST", "/v1/sys/packages/update", false, true},
		{"PUT", "/v2/app_management/compose/plex/status", false, true},
		{"PATCH", "/v2/app_management/compose/plex", false, true},
		{"POST", "/v2/app_management/compose/updates", false, true},
		{"POST", "/v2/app_management/image-updates", true, true},
		{"POST", "/v2/virt_management/vms/0f1e-2d3c/start", false, true},
		{"POST", "/v2/virt_management/vms/0f1e-2d3c/force-off", false, true},
		// refused
		{"GET", "/v1/sys/update", false, false},
		{"DELETE", "/v2/app_management/compose/plex", false, false},
		{"GET", "/v1/file/content", false, false},
		{"GET", "/v1/users/current", false, false},
		{"POST", "/v2/virt_management/vms/0f1e-2d3c/reset", false, false},
		{"POST", "/v2/virt_management/vms/a/b/start", false, false},
		{"GET", "/v2/app_management/compose/plex/logs", false, false},
		{"POST", "/v1/sys/modules/virt/install", false, false},
	} {
		slow, ok := Allowed(c.method, c.path)
		if slow != c.slow || ok != c.ok { t.Errorf("%s %s: slow %v ok %v, want %v %v", c.method, c.path, slow, ok, c.slow, c.ok) }
	}
}

// Nothing is normalised: a path is matched as it is, so the table cannot be reached around.
func TestAllowedNormalisesNothing(t *testing.T) {
	for _, path := range []string{"/v1/sys/os/", "//v1/sys/os", "/v1/sys/../sys/os", "/v1/sys%2Fos", "/V1/sys/os", "/v1/sys/os?x=1"} {
		if _, ok := Allowed("GET", path); ok { t.Errorf("%q passed", path) }
	}
}

func TestTimeout(t *testing.T) {
	if Timeout(false) != 60*time.Second || Timeout(true) != 300*time.Second { t.Fatal("timeouts") }
}
```

- [ ] **Step 3: Run to see them fail**

Run: `go test ./pkg/fleet/ -run 'TestAllowed|TestTimeout' -v`
Expected: FAIL, `undefined: Allowed`

- [ ] **Step 4: Implement `allow.go`**

A table of `{method, pattern}` where a pattern is split on `/` and a segment `{id}` matches exactly one non-empty segment without `/`; `{action}` matches one of `start`, `shutdown`, `reboot`, `force-off`. Compare segments literally (case-sensitive, no decoding, no trimming). Mark the three slow rows.

- [ ] **Step 5: Run to see them pass**

Run: `go test ./pkg/fleet/ -v`
Expected: PASS

- [ ] **Step 6: Commit**

```bash
git add docs/superpowers/specs/2026-11-02-fleet-design.md docs/superpowers/plans/2026-11-02-fleet-core.md docs/superpowers/plans/2026-11-02-fleet-ui.md && git commit -m "docs(fleet): design and plans"
git add pkg/fleet && git commit -m "feat(fleet): the table of the routes a master may relay"
```

---

### Task 2: The enrolment code

**Files:**
- Create: `pkg/fleet/code.go`, `pkg/fleet/code_test.go`

**Interfaces:**
- Produces: `type Code struct { MasterURL string `json:"u"`; BoxID string `json:"b"`; Key string `json:"k"`; CertSHA256 string `json:"f"` }`; `func (c Code) String() string` (base64 RawURLEncoding of the JSON); `func ParseCode(s string) (Code, error)`; `func ValidMasterURL(raw string) error` (https, or http with a loopback host; no user info; no query or fragment); `func NewKey() (key, keyHash string, err error)` (32 random bytes → hex, and `sha256` hex of the key string); `func KeyHash(key string) string`.

- [ ] **Step 1: Write the failing tests**

```go
func TestCodeRoundTrip(t *testing.T) {
	c := Code{MasterURL: "https://master.example", BoxID: "6f1c…", Key: strings.Repeat("ab", 32), CertSHA256: strings.Repeat("cd", 32)}
	got, err := ParseCode(c.String())
	if err != nil || got != c { t.Fatalf("got %+v, %v", got, err) }
}

func TestParseCodeRefuses(t *testing.T) {
	for name, s := range map[string]string{
		"not base64":   "!!!",
		"not json":     base64.RawURLEncoding.EncodeToString([]byte("x")),
		"no key":       Code{MasterURL: "https://m", BoxID: "b"}.String(),
		"no box":       Code{MasterURL: "https://m", Key: "k"}.String(),
		"http remote":  Code{MasterURL: "http://m.example", BoxID: "b", Key: "k"}.String(),
		"user info":    Code{MasterURL: "https://u:p@m.example", BoxID: "b", Key: "k"}.String(),
		"bad fp":       Code{MasterURL: "https://m", BoxID: "b", Key: "k", CertSHA256: "zz"}.String(),
	} {
		if _, err := ParseCode(s); err == nil { t.Errorf("%s: accepted", name) }
	}
}

func TestValidMasterURL(t *testing.T) {
	for raw, ok := range map[string]bool{"https://m.example": true, "https://100.64.0.1:8443": true, "http://127.0.0.1:80": true, "http://[::1]:443": true, "http://10.0.0.2": false, "ftp://m": false, "https://m/?x": false, "": false} {
		if (ValidMasterURL(raw) == nil) != ok { t.Errorf("%q", raw) }
	}
}

func TestNewKeyIsRandomAndHashed(t *testing.T) {
	k1, h1, _ := NewKey(); k2, _, _ := NewKey()
	if len(k1) != 64 || k1 == k2 || h1 != KeyHash(k1) { t.Fatal("key") }
}
```

- [ ] **Step 2: Run to see them fail** — `go test ./pkg/fleet/ -run 'Code|MasterURL|NewKey' -v` → FAIL, undefined.
- [ ] **Step 3: Implement `code.go`**: `ParseCode` decodes, unmarshals, requires `BoxID` and `Key` non-empty, `ValidMasterURL` nil, and `CertSHA256` empty or 64 hex characters.
- [ ] **Step 4: Run to see them pass** — `go test ./pkg/fleet/ -v` → PASS.
- [ ] **Step 5: Commit** — `git commit -am "feat(fleet): the enrolment code and the key"`.

---

### Task 3: The two state files

**Files:**
- Create: `pkg/fleet/state.go`, `pkg/fleet/state_test.go`

**Interfaces:**
- Produces:
  ```go
  const StateDir = "/var/lib/casaos/fleet"
  type Box struct { ID, Name, URL, KeyHash string `json:"key_sha256"`; Self bool; CreatedAt, LastSeen time.Time; Hostname, Version string }
  type MasterState struct { Enabled bool; Address string; Boxes []Box }
  type MemberState struct { MasterURL string `json:"master_url"`; BoxID string `json:"box_id"`; Key string; MasterCertSHA256 string `json:"master_cert_sha256"` }
  func (f *Fleet) loadMaster() (MasterState, bool)   // ok=false when absent or unreadable (unreadable also logs once)
  func (f *Fleet) saveMaster(MasterState) error
  func (f *Fleet) loadMember() (MemberState, bool)
  func (f *Fleet) saveMember(MemberState) error
  func (f *Fleet) clearMember() error                 // removes the file
  ```
  and in `fleet.go`: `type Fleet struct { Root string; RuntimePath func() string; Now func() time.Time; Log func(msg string, err error); … }`, `func New(root string) *Fleet`, `var Default = New("/")`. Paths are `filepath.Join(f.Root, StateDir, "master.json")` and `"member.json"`.

- [ ] **Step 1: Write the failing tests**

```go
func TestMasterStateRoundTripAnd0600(t *testing.T) {
	f := New(t.TempDir())
	want := MasterState{Enabled: true, Address: "https://m", Boxes: []Box{{ID: "b1", Name: "Maison", KeyHash: "h"}}}
	if err := f.saveMaster(want); err != nil { t.Fatal(err) }
	got, ok := f.loadMaster()
	info, _ := os.Stat(filepath.Join(f.Root, StateDir, "master.json"))
	if !ok || !reflect.DeepEqual(got, want) || info.Mode().Perm() != 0o600 { t.Fatalf("%+v %v %v", got, ok, info.Mode()) }
}

func TestMissingOrBrokenFilesAreRoleOff(t *testing.T) {
	f := New(t.TempDir())
	if _, ok := f.loadMaster(); ok { t.Fatal("missing master.json read as on") }
	if _, ok := f.loadMember(); ok { t.Fatal("missing member.json read as on") }
	os.MkdirAll(filepath.Join(f.Root, StateDir), 0o700)
	os.WriteFile(filepath.Join(f.Root, StateDir, "member.json"), []byte("{"), 0o600)
	logged := 0
	f.Log = func(string, error) { logged++ }
	if _, ok := f.loadMember(); ok || logged != 1 { t.Fatalf("broken member.json: ok %v logged %d", ok, logged) }
}

// A master.json or member.json edited by hand to a plain-HTTP remote master is a file the code rule refused: role off.
func TestLoadRefusesANonHTTPSMasterOutsideLoopback(t *testing.T) {
	f := New(t.TempDir())
	f.saveMember(MemberState{MasterURL: "http://10.0.0.2", BoxID: "b", Key: "k"})
	if _, ok := f.loadMember(); ok { t.Fatal("accepted") }
}

func TestClearMemberRemovesTheFile(t *testing.T) { /* saveMember, clearMember, loadMember → ok false, file gone */ }
```

- [ ] **Step 2: Run to see them fail** — `go test ./pkg/fleet/ -run 'State|RoleOff|Refuses|ClearMember' -v` → FAIL.
- [ ] **Step 3: Implement `fleet.go` (struct, `New`, `Default`, `Log` defaulting to `logger.Error`) and `state.go`** following `pkg/telemetry/state.go`: `os.ReadFile`, `errors.Is(err, fs.ErrNotExist)`, `file.WriteFileAtomic`.
- [ ] **Step 4: Run to see them pass** — `go test ./pkg/fleet/ -v` → PASS.
- [ ] **Step 5: Commit** — `git add pkg/fleet && git commit -m "feat(fleet): master.json and member.json"`.

---

### Task 4: The TLS decision of a member

**Files:**
- Create: `pkg/fleet/tls.go`, `pkg/fleet/tls_test.go`

**Interfaces:**
- Produces: `func tlsConfig(serverName, pinSHA256 string) *tls.Config` — `InsecureSkipVerify: true` plus a `VerifyConnection` that first runs the standard verification (`x509.Certificate.Verify` with system roots, `DNSName: serverName`, the intermediates of the chain) and, when that fails and `pinSHA256` is not empty, accepts iff `sha256(leaf.Raw)` hex equals `pinSHA256`. `MinVersion: tls.VersionTLS12`. `func Fingerprint(cert *x509.Certificate) string`.

- [ ] **Step 1: Write the failing tests** — build in the test a self-signed CA and a leaf for `master.example` (`crypto/x509` + `ecdsa`), and a second self-signed leaf; wrap each in `tls.ConnectionState{PeerCertificates: …}`:

```go
func TestPinnedLeafIsAcceptedWhenTheChainIsNot(t *testing.T)   // self-signed leaf, pin = Fingerprint(leaf) → nil
func TestWrongPinIsRefused(t *testing.T)                        // self-signed leaf, pin of the other leaf → error
func TestNoPinAndNoChainIsRefused(t *testing.T)                 // self-signed leaf, pin "" → error
func TestHostnameIsIgnoredWhenThePinDecides(t *testing.T)       // leaf for "localhost", serverName "100.64.0.1", pin matches → nil
func TestAValidChainNeedsNoPin(t *testing.T)                    // leaf signed by a CA added to cfg.RootCAs (the test injects roots through a package var `rootCAs func() *x509.CertPool`), pin "" → nil
```

- [ ] **Step 2: Run to see them fail** — `go test ./pkg/fleet/ -run 'Pin|Chain|Hostname' -v` → FAIL.
- [ ] **Step 3: Implement `tls.go`** with `var systemRoots = func() (*x509.CertPool, error) { return x509.SystemCertPool() }` for the test to replace.
- [ ] **Step 4: Run to see them pass** — PASS.
- [ ] **Step 5: Commit** — `git commit -am "feat(fleet): a member pins the master's certificate when the chain does not verify"`.

---

### Task 5: Where this box's gateway is, and its certificate

**Files:**
- Create: `pkg/fleet/gateway.go`, `pkg/fleet/gateway_test.go`

**Interfaces:**
- Produces: `type GatewayInfo struct { Public *url.URL /* http(s)://127.0.0.1:<port> */; HTTPS *url.URL /* https://127.0.0.1:<httpsport or 443>, nil when a certificate is configured */; CertFile string }`; `func ReadGatewayINI(path, dataPath string) (GatewayInfo, error)` — reads `[gateway] port`, `address`, `tlscert`, `tlskey`, `httpsport`; `CertFile` is `tlscert` when both `tlscert` and `tlskey` are set, else `filepath.Join(dataPath, "tls", "gateway.crt")`; an `address` that is set and not loopback → error `the gateway listens on <address>, not on this box's loopback`. `func CertFingerprint(certFile string) (string, error)` (PEM → x509 → `Fingerprint`). On `Fleet`: `Gateway func() (GatewayInfo, error)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestGatewayURLFromINI(t *testing.T) {
	// "[gateway]\nport=80\naddress=\n" → Public http://127.0.0.1:80, HTTPS https://127.0.0.1:443, CertFile <data>/tls/gateway.crt
	// "[gateway]\nport=8443\ntlscert=/c\ntlskey=/k\nhttpsport=\n" → Public https://127.0.0.1:8443, HTTPS nil, CertFile /c
	// "[gateway]\nport=80\nhttpsport=8443\n" → HTTPS https://127.0.0.1:8443
}
func TestGatewayURLRefusesANonLoopbackAddress(t *testing.T) { /* address=192.168.1.5 → error mentioning 192.168.1.5 */ }
func TestGatewayINIWithoutAPortIsAnError(t *testing.T)
func TestCertFingerprintOfAPEMFile(t *testing.T) { /* write a self-signed cert PEM, expect sha256 hex of its Raw */ }
```

- [ ] **Step 2: Run to see them fail** → FAIL.
- [ ] **Step 3: Implement `gateway.go`** with `github.com/go-ini/ini` (`ini.LoadOptions{Insensitive: true}` like `pkg/modulecmd/gateway.go`).
- [ ] **Step 4: Run to see them pass** → PASS.
- [ ] **Step 5: Commit** — `git commit -am "feat(fleet): read where the gateway listens and which certificate it serves"`.

---

### Task 6: The master: tunnels, relay, boxes

**Files:**
- Create: `pkg/fleet/frame.go`, `pkg/fleet/master.go`, `pkg/fleet/master_test.go`

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces (all on `*Fleet`, safe for concurrent use):
  ```go
  // frame.go
  type frame struct {
      T string `json:"t"` // "hello" | "req" | "res"
      ID string `json:"id,omitempty"`
      Hostname string `json:"hostname,omitempty"`; Version string `json:"version,omitempty"`     // hello
      Method string `json:"method,omitempty"`; Path string `json:"path,omitempty"`; Query string `json:"query,omitempty"` // req
      Headers map[string]string `json:"headers,omitempty"`; Body []byte `json:"body,omitempty"`  // req and res (Body is base64 by encoding/json)
      Status int `json:"status,omitempty"`                                                        // res
  }
  const maxFrame = MaxBody + 64<<10
  // master.go
  type Response struct { Status int; ContentType string; Body []byte }
  var ErrOffline = errors.New("box offline"); var ErrBusy = errors.New("too many requests in flight for this box"); var ErrTimeout = errors.New("the box did not answer in time"); var ErrNotAllowed = errors.New("this route is not relayed"); var ErrNoBox = errors.New("no such box")
  func (f *Fleet) HandleTunnel(w http.ResponseWriter, r *http.Request)   // 401 unless Authorization is "Fleet <id>:<key>" of an enrolled box; upgrades; reads hello; replaces an older tunnel of the same box; serves until closed
  func (f *Fleet) Relay(ctx context.Context, boxID, method, path, query, contentType string, body []byte) (Response, error)
  func (f *Fleet) EnableMaster(address string) error                     // ValidMasterURL; keeps existing boxes
  func (f *Fleet) DisableMaster() error                                   // closes every tunnel, deletes master.json
  func (f *Fleet) AddBox(name, url string, self bool) (Box, Code, error)  // the code's MasterURL is Address, or for self the gateway's HTTPS (or Public when HTTPS is nil) URL; CertSHA256 from Gateway().CertFile; self also saves member.json and reconnects the member
  func (f *Fleet) RenameBox(id, name, url string) error
  func (f *Fleet) RemoveBox(id string) error                               // closes its tunnel
  func (f *Fleet) RotateCode(id string) (Code, error)                      // new key, closes its tunnel
  func (f *Fleet) MasterStatus() (MasterStatus, bool)                      // ok=false when the role is off
  type MasterStatus struct { Address string `json:"address"`; Boxes []BoxStatus `json:"boxes"` }
  type BoxStatus struct { ID, Name, URL string; Self bool; Online bool; LastSeen time.Time `json:"last_seen,omitzero"`; Hostname, Version string }
  ```
  Headers forwarded in a `req` frame: `Content-Type` and `Accept` only; `Query` never contains `token`. `Relay` refuses with `ErrNotAllowed` before touching the tunnel; `ErrOffline` without a tunnel; `ErrBusy` at 16 in flight; `ErrTimeout` after `Timeout(slow)`. WebSocket ping every 30 s, read deadline 60 s, `SetReadLimit(maxFrame)`.

- [ ] **Step 1: Write the failing tests** with a test helper `fakeMember(t, server *httptest.Server, code Code, answer func(frame) frame)` that dials `HandleTunnel` with `gorilla/websocket` (header `Authorization: Fleet id:key`), sends a hello, and answers each `req` frame with `answer(req)`:

```go
func TestTunnelRefusesUnknownKeyAndRevokedBox(t *testing.T)        // wrong key → 401 (handshake fails); RemoveBox then dial → 401
func TestRelayRoundTrip(t *testing.T)                              // fake member answers 200 {"ok":true} with Content-Type application/json: Relay returns Status 200, ContentType, Body; the frame it received has Method, Path, Query "a=1" (token=x dropped), Headers Content-Type and Accept only
func TestRelayRefusesOutsideTheTable(t *testing.T)                 // Relay(GET /v1/file/content) → ErrNotAllowed, no frame sent
func TestRelayWhenOffline(t *testing.T)                            // no tunnel → ErrOffline
func TestRelayTimeout(t *testing.T)                                // fake member never answers; timeouts shortened through a package var `timeouts = Timeout` → ErrTimeout
func TestRelayCapsRequestsInFlight(t *testing.T)                   // 16 unanswered → the 17th is ErrBusy
func TestASecondTunnelReplacesTheFirst(t *testing.T)               // dial twice: the first conn gets a close; Relay goes to the second
func TestOversizedFramesAreDroppedWithTheConnection(t *testing.T) // a res frame of maxFrame+1 bytes → that conn is closed, Relay gets ErrOffline, the master server still answers a new dial
func TestAddBoxSelfWritesMemberJSON(t *testing.T)                  // Gateway stub returns HTTPS https://127.0.0.1:443 and a cert file → the code's MasterURL is that, CertSHA256 is the file's fingerprint, member.json exists with the box id
func TestRemoveBoxAndDisableCloseTunnels(t *testing.T)             // RemoveBox closes that box's live tunnel (Relay → ErrOffline); DisableMaster closes every tunnel and deletes master.json
func TestTheMasterNeverExecutesAFrameFromAMember(t *testing.T)     // the fake member sends a req frame: the master answers nothing, closes nothing it should keep, and no handler ran (the tunnel is asymmetric)
func TestMasterStatus(t *testing.T)                                 // Online true while a tunnel is up, LastSeen/Hostname/Version from the hello, Self flag
```

- [ ] **Step 2: Run to see them fail** — `go test ./pkg/fleet/ -run 'Tunnel|Relay|AddBox|Disable|MasterStatus' -v` → FAIL.
- [ ] **Step 3: Implement `frame.go` and `master.go`**. Tunnels: `map[boxID]*tunnel` under a mutex; a `tunnel` has the conn, a write mutex, `pending map[id]chan frame`, and a `done` channel. `Relay`: generate the id with `crypto/rand`, register the channel, write the frame, wait with the context. The read loop dispatches `res` frames to `pending[id]` and closes the tunnel on any error (which fails every pending request with `ErrOffline`). Key check: look the box up by id, `subtle.ConstantTimeCompare([]byte(KeyHash(key)), []byte(box.KeyHash))`.
- [ ] **Step 4: Run to see them pass** — `go test ./pkg/fleet/ -race -v` → PASS.
- [ ] **Step 5: Commit** — `git add pkg/fleet && git commit -m "feat(fleet): the master's tunnels, boxes and relay"`.

---

### Task 7: The member: dial loop, serving requests, status

**Files:**
- Create: `pkg/fleet/member.go`, `pkg/fleet/member_test.go`

**Interfaces:**
- Consumes: Tasks 1–6; `external.InternalAuthorization(runtimePath)` from CasaOS-Common.
- Produces:
  ```go
  func (f *Fleet) Join(code string) error            // ParseCode, saveMember, (re)start the dial loop at once
  func (f *Fleet) Leave() error                       // closes the tunnel, clearMember
  func (f *Fleet) Run(ctx context.Context)            // main calls it once: runs the member loop while member.json exists, and the master's offline watch (Task 8)
  func (f *Fleet) MemberStatus() (MemberStatus, bool) // ok=false when the role is off
  type MemberStatus struct { MasterURL string `json:"master_url"`; Connected bool `json:"connected"`; Since time.Time `json:"since,omitzero"`; LastError string `json:"last_error,omitempty"`; LastTry time.Time `json:"last_try,omitzero"` }
  // on Fleet, set by main: Hostname func() (string, error); Version func() string
  ```
  Dial: `wss://` (or `ws://` for a loopback `http://` master), header `Authorization: Fleet <box_id>:<key>`, `tlsConfig(host, MasterCertSHA256)`; then send hello; then serve: for each `req` frame, in its own goroutine (at most 16 at a time), check `Allowed` (403 `{"message":"this route is not relayed"}` otherwise), build the local request to `Gateway().Public` + path + query with `Authorization: external.InternalAuthorization(RuntimePath())`, `Content-Type`/`Accept` from the frame, body, timeout `Timeout(slow)`, `InsecureSkipVerify` when `Public` is https (own box, loopback); answer with the status, the `Content-Type` and the body (truncated to `MaxBody` → 502 instead). Backoff: 5 s doubling to 5 min with ±20 % jitter; after a 401 from the master: `LastError = "refused by the master"` and 10 min between tries.

- [ ] **Step 1: Write the failing tests** — an end-to-end fixture in `member_test.go`: `master := New(dirA)` served by `httptest.NewServer` at `/v1/fleet/tunnel` → `master.HandleTunnel`; `member := New(dirB)` with `member.Gateway` returning `Public` = the URL of a second `httptest.Server` whose handler records the request and answers what the test says; `member.RuntimePath` → a temp dir holding `internal.secret` = `s3cret`.

```go
func TestMemberServesARelayedRequestWithItsInternalIdentity(t *testing.T)
// master.AddBox("b", "", false) → code; member.Join(code.String()); wait until master.MasterStatus shows Online;
// master.Relay(GET /v1/sys/version/check?token=x&y=1) → the local gateway saw GET /v1/sys/version/check?y=1 with Authorization "Internal s3cret" and Accept; the answer's status 200, Content-Type and body are returned as is
func TestMemberRefusesOutsideTheTableEvenIfTheMasterAsks(t *testing.T)
// send a raw req frame for GET /v1/file/content through the master's tunnel (test hook `master.rawRelay`) → res 403 and the local gateway saw nothing
func TestMemberReconnectsAfterTheMasterRestarts(t *testing.T)
// backoff shortened through package vars `firstWait`, `maxWait`; close the master server, start a new one on the same address (httptest with a reused listener) → Online again within 2 s
func TestRevokedMemberStopsHammering(t *testing.T)
// master.RemoveBox → the member's next dial is 401; MemberStatus LastError "refused by the master"; the dial count does not grow for `refusedWait` (shortened)
func TestLeaveClosesAndForgets(t *testing.T)
func TestDisableClosesTunnelsAndKeepsMemberIdle(t *testing.T)      // one Fleet self-enrolled (master and member in the same struct, served by httptest); DisableMaster: master.json gone, member.json still there, MemberStatus says not connected with LastError "refused by the master"
func TestMemberHelloCarriesHostnameAndVersion(t *testing.T)
func TestBodyLimitOnTheMemberSide(t *testing.T)                   // local gateway answers MaxBody+1 bytes → res 502
```

- [ ] **Step 2: Run to see them fail** → FAIL.
- [ ] **Step 3: Implement `member.go`**; keep the loop's state (conn, since, lastError, lastTry) under a mutex; `Run` watches a `restart chan struct{}` that `Join`, `Leave` and `AddBox(self)` poke.
- [ ] **Step 4: Run to see them pass** — `go test ./pkg/fleet/ -race -v` → PASS.
- [ ] **Step 5: Commit** — `git add pkg/fleet && git commit -m "feat(fleet): a member keeps its tunnel to the master and serves the table through its gateway"`.

---

### Task 8: The "box offline" alert

**Files:**
- Create: `pkg/alerts/fleet.go`, `pkg/alerts/fleet_test.go`
- Modify: `pkg/alerts/alerts.go:58-67` (add `Fleet = "fleet"` to the constants and to `categories`), `pkg/alerts/config_test.go` (the defaults now list six categories), `pkg/fleet/master.go` (the watch)

**Interfaces:**
- Produces: `func (h *Hub) FleetOffline(boxID, name string, offline bool)` — `offline` true raises `alert{key: "fleet:" + boxID + ":offline", category: Fleet, sentence: "The box " + name + " has not reached the master for 10 minutes."}`; false resolves it. On `Fleet`: `Offline func(boxID, name string, offline bool)`; `var offlineAfter = 10 * time.Minute` and `var startGrace = 10 * time.Minute` (package vars for tests); the watch runs every minute inside `Run`.

- [ ] **Step 1: Write the failing tests**

```go
// pkg/alerts/fleet_test.go
func TestFleetOfflineAlertAndResolution(t *testing.T) {
	h, o, _ := newHub(t)
	h.FleetOffline("b1", "Maison", true); h.sending.Wait()
	if got := o.texts(phone); len(got) != 1 || got[0] != "The box Maison has not reached the master for 10 minutes.\nhttp://192.168.1.20" { t.Fatalf("%q", got) }
	h.FleetOffline("b1", "Maison", false); h.sending.Wait()
	if got := o.texts(phone); len(got) != 2 || !strings.Contains(got[1], "resolved") { t.Fatalf("%q", got) } // match the wording resolve() uses today
}
func TestFleetIsACategoryOnByDefault(t *testing.T) { if !defaults().Categories[Fleet] { t.Fatal("off") } }

// pkg/fleet/master_test.go
func TestOfflineWatch(t *testing.T)
// Now is a fake clock; a box seen then its tunnel closed: after offlineAfter-1min no call; after offlineAfter → Offline(id, name, true) once; a new tunnel → Offline(..., false); nothing for a box that has never connected during startGrace after New
```

- [ ] **Step 2: Run to see them fail** → FAIL.
- [ ] **Step 3: Implement** `pkg/alerts/fleet.go` (mirror `updates.go`), the constant and category list, and `(*Fleet).watchOffline(ctx)` called from `Run` (a `time.Ticker` of one minute; `tick()` exported to tests through a package-level var or a method `checkOffline(now time.Time)`).
- [ ] **Step 4: Run to see them pass** — `go test ./pkg/alerts/ ./pkg/fleet/ -race` → PASS.
- [ ] **Step 5: Commit** — `git add pkg/alerts pkg/fleet && git commit -m "feat(fleet): one alert when a box has not reached the master for ten minutes"`.

---

### Task 9: The `/v1/fleet` routes

**Files:**
- Create: `route/v1/fleet.go`, `route/v1/fleet_test.go`
- Modify: `route/v1.go` (a `/v1/fleet` group inside `v1Group`, after the `/sys` group; `e.GET("/v1/fleet/tunnel", v1.FleetTunnel)` next to `e.GET("/v1/sys/version/check", …)` at `route/v1.go:66`), `route/token_test.go` (add `/v1/fleet/boxes/x/relay/v1/sys/os` and `PUT /v1/fleet/member` to the cases: a `GET` relay takes `?token=`, a `PUT` member refuses it and refuses the refresh token)

**Interfaces:**
- Consumes: Tasks 6–8 (`fleet.Default`).
- Produces the API of the spec:
  - `GET /v1/fleet` → `Data: fleet.Status{Role string; Master *MasterStatus; Member *MemberStatus}` (`func (f *Fleet) Status() Status`: role `master` when master is on, else `member` when member.json exists, else `none`).
  - `PUT /v1/fleet/master` `{enabled bool, address string}` → 400 on a bad address, else `Status` — `headerTokenOnly`.
  - `POST /v1/fleet/boxes` `{name, url, self}` → `{box: BoxStatus, code: string}` (400 on empty name or bad url; 409 when the role is off) — `headerTokenOnly`.
  - `PATCH /v1/fleet/boxes/:id` `{name, url}`; `DELETE /v1/fleet/boxes/:id`; `POST /v1/fleet/boxes/:id/code` → `{code}` — the last two `headerTokenOnly`; 404 on `ErrNoBox`.
  - `GET /v1/fleet/boxes/:id/relay/*` and `Match([]string{POST, PUT, PATCH, DELETE}, "/boxes/:id/relay/*", …, headerTokenOnly)` → the box's status, `Content-Type` and body verbatim (`ctx.Blob`), `Cache-Control: no-store`; `ErrNotAllowed` → 403, `ErrNoBox` → 404, `ErrOffline` → 503, `ErrBusy` → 503, `ErrTimeout` → 504, each as `model.Result{Message: err.Error()}`; the body read with `http.MaxBytesReader(…, fleet.MaxBody)` → 413 beyond.
  - `PUT /v1/fleet/member` `{code}` → 400 on a bad code, else `Status`; `DELETE /v1/fleet/member` → `Status` — both `headerTokenOnly`.
  - `GET /v1/fleet/tunnel` → `fleet.Default.HandleTunnel`.

- [ ] **Step 1: Write the failing tests** in `route/v1/fleet_test.go` with a `useFleetFixture(t)` like `useAutoUpdateFixture` (swap `fleet.Default` for `fleet.New(t.TempDir())` with stubbed `Gateway`, `Hostname`, `Version`, `Offline`), and in `route/token_test.go`:

```go
func TestFleetStatusRoles(t *testing.T)                 // none → master after PUT master → member block present after POST boxes self
func TestFleetRelayMapsErrorsToStatuses(t *testing.T)   // 403 table, 404 box, 503 offline; the Message is the error text
func TestFleetRelayPassesTheAnswerThrough(t *testing.T) // with the Task 7 fixture inside the route test: status, Content-Type, body, no-store
func TestFleetRelayBodyLimit(t *testing.T)              // MaxBody+1 → 413
func TestFleetBoxesCRUDAndCodeOnce(t *testing.T)        // POST returns a code once; GET /v1/fleet never contains "code" or "key"
func TestFleetMemberJoinAndLeave(t *testing.T)          // PUT bad code → 400; good code → role member; DELETE → none
// route/token_test.go additions: a GET relay with ?token=<access> → 200 (or 503 offline, not 401); PUT /v1/fleet/member with ?token= → 401; with the refresh token in the header → 401; with "Internal <secret>" from loopback → not 401
```

- [ ] **Step 2: Run to see them fail** — `go test ./route/... -run Fleet -v` → FAIL.
- [ ] **Step 3: Implement `route/v1/fleet.go` and wire `route/v1.go`**.
- [ ] **Step 4: Run to see them pass** — `go test ./route/... ./pkg/fleet/ -race` → PASS; `go vet ./...` clean.
- [ ] **Step 5: Commit** — `git add route && git commit -m "feat(fleet): the /v1/fleet routes: status, boxes, relay, member, tunnel"`.

---

### Task 10: Wiring in `main.go`, the changelog

**Files:**
- Modify: `main.go:276-293` (add `"/v1/fleet"` to `routers`), `main.go:208-235` (wire `fleet.Default` after `alerts.Default` and start it), `CHANGELOG.md` (Unreleased → Added), `README.md` ("What ReCasaOS adds" gets a **Fleet** bullet: one sentence, and the 2FA recommendation for a master)

**Interfaces:**
- Consumes: `fleet.Default` fields: `RuntimePath = func() string { return config.CommonInfo.RuntimePath }`, `Gateway = func() (fleet.GatewayInfo, error) { return fleet.ReadGatewayINI("/etc/casaos/gateway.ini", constants.DefaultDataPath) }`, `Hostname = os.Hostname`, `Version = version.CurrentVersion`, `Offline = alerts.Default.FleetOffline`; `go fleet.Default.Run(context.Background())`.

- [ ] **Step 1: Extend `route/gateway_wiring_test.go`** (it parses `main.go`) so that `"/v1/fleet"` must be in the `routers` list; run it to see it fail.
- [ ] **Step 2: Wire `main.go`**, add the CHANGELOG entry ("**Fleet.** A box can be the master of your other boxes: …", the alert category, the two files, the route table in one line) and the README bullet.
- [ ] **Step 3: Build and test everything**

Run: `go build ./... && go test ./... -race`
Expected: builds; PASS.

- [ ] **Step 4: Commit** — `git add main.go CHANGELOG.md README.md route/gateway_wiring_test.go && git commit -m "feat(fleet): wire the fleet into the core"`.

---

### Task 11: The install-check step

**Files:**
- Modify: `D:\clients\casaos\get\.github\workflows\install-check.yml` (a step `Fleet` after the step `Alerts` at line ~2245, same shape: `curl` against the box with the token the earlier steps already hold)

**Interfaces:**
- Consumes: the API of Task 9 on a freshly installed box.

- [ ] **Step 1: Read the `Alerts` and `Automatic update` steps** (`install-check.yml` lines ~2112–2300) for the token variable, the base URL and the assertion helpers they use.
- [ ] **Step 2: Add the step `Fleet`**, in that style, asserting in order:
  1. `PUT /v1/fleet/master {"enabled":true,"address":"https://127.0.0.1:443"}` → 200, `data.role == "master"`;
  2. `POST /v1/fleet/boxes {"name":"this box","self":true}` → 200 with a non-empty `data.code`;
  3. within 30 s, `GET /v1/fleet` shows the box `online: true`;
  4. `GET /v1/fleet/boxes/<id>/relay/v1/sys/version/check` body equals `GET /v1/sys/version/check`;
  5. `PUT …/relay/v2/app_management/compose/<test app>/status` with body `"stop"` → 200, then the app's status reads `exited` through `…/relay/v2/app_management/compose`;
  6. `GET …/relay/v1/file/content?path=/etc/hostname` → 403;
  7. `DELETE /v1/fleet/boxes/<id>` → 200, then within 60 s `GET /v1/fleet` has `member.last_error == "refused by the master"`;
  8. `DELETE /v1/fleet/member` and `PUT /v1/fleet/master {"enabled":false}` → `data.role == "none"`.
- [ ] **Step 3: Run the workflow's YAML through the repository's usual lint** (whatever `release.yml`/`install-check.yml` are checked with in `get`; at least `python -c "import yaml,sys; yaml.safe_load(open(sys.argv[1]))" .github/workflows/install-check.yml`).
- [ ] **Step 4: Commit in `get`** — `git checkout -b feat/fleet && git add .github/workflows/install-check.yml && git commit -m "ci(install-check): exercise the fleet on one box"`.

---

## Self-review notes

- Spec coverage: roles and enrolment (Tasks 2, 3, 6, 7, 9), tunnel (6, 7), relay and table (1, 6, 7, 9), state files (3), alert (8), routes (9), self-enrolment (6, 9, 11), gateway cases (5), wiring (10), install check (11). The dashboard is the other plan.
- Names used across tasks: `Allowed`, `Timeout`, `MaxBody`, `Code`, `ParseCode`, `ValidMasterURL`, `NewKey`, `KeyHash`, `Box`, `MasterState`, `MemberState`, `GatewayInfo`, `ReadGatewayINI`, `CertFingerprint`, `tlsConfig`, `Fingerprint`, `frame`, `Response`, `HandleTunnel`, `Relay`, `EnableMaster`, `DisableMaster`, `AddBox`, `RenameBox`, `RemoveBox`, `RotateCode`, `MasterStatus`, `BoxStatus`, `Join`, `Leave`, `Run`, `MemberStatus`, `Status`, `Offline`, `FleetOffline`, the errors `ErrOffline`, `ErrBusy`, `ErrTimeout`, `ErrNotAllowed`, `ErrNoBox`.
