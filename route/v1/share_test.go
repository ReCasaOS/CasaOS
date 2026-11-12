package v1

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/pkg/share"
	"github.com/labstack/echo/v4"
)

// relayReply is what the fake relay answers to the signed call that claims a name.
type relayReply struct {
	status int
	body   string
	retry  time.Duration
}

// guardSeen is the guard calls the fake Gateway of useShare received, as "METHOD escaped-path".
var guardSeen struct {
	sync.Mutex
	calls []string
	put   int // when set, the answer to every guard write
}

func guardCalls() []string {
	guardSeen.Lock()
	defer guardSeen.Unlock()
	return append([]string(nil), guardSeen.calls...)
}

// useShare swaps Share for a service of its own: the relay is a function, AppManagement and the Gateway are one local
// server that knows the app "jellyfin" (port 8096).
func useShare(t *testing.T, claim *relayReply, remote bool) *share.Service {
	t.Helper()
	runtimePath := t.TempDir()
	guardSeen.Lock()
	guardSeen.calls, guardSeen.put = nil, 0
	guardSeen.Unlock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v1/gateway/guard"):
			guardSeen.Lock()
			guardSeen.calls = append(guardSeen.calls, r.Method+" "+r.URL.EscapedPath())
			guardSeen.Unlock()
			switch {
			case r.Method != http.MethodGet:
				guardSeen.Lock()
				status := guardSeen.put
				guardSeen.Unlock()
				if status == 0 {
					status = http.StatusNoContent
				}
				w.WriteHeader(status)
			case r.URL.Path == "/v1/gateway/guard":
				_, _ = w.Write([]byte(`{"version":1,"configured":true,"geo":{},"people_last_used":{},"waves":[]}`))
			default:
				_, _ = w.Write([]byte(`[]`))
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v2/app_management/compose":
			_, _ = w.Write([]byte(`{"data":{"jellyfin":{"store_info":{"port_map":"8096","title":{"en_us":"Jellyfin"}}},"nothing":{"store_info":{}}}}`))
		case r.Method == http.MethodPut && r.URL.Path == "/v1/gateway/hosts":
			w.WriteHeader(http.StatusCreated)
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)
	for _, file := range []string{external.AppManageURLFilename, external.ManagementURLFilename} {
		if err := os.WriteFile(filepath.Join(runtimePath, file), []byte(server.URL), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	s := share.New(t.TempDir())
	s.RuntimePath = func() string { return runtimePath }
	if remote {
		s.RelayURL = func() (string, bool) { return "https://relay.example", true }
	}
	s.Relay.Signed = func(ctx context.Context, method, path string, body []byte) (int, []byte, time.Duration, error) {
		if method == http.MethodPut && path == "/v1/share/name" {
			return claim.status, []byte(claim.body), claim.retry, nil
		}
		return http.StatusOK, []byte(`{"name":"maison","host":"maison.example","domain":"example"}`), 0, nil
	}
	original := Share
	Share = s
	t.Cleanup(func() { Share = original })
	return s
}

var claimed = &relayReply{status: http.StatusOK, body: `{"name":"maison","host":"maison.example","domain":"example"}`}

func TestGetShareStatusShape(t *testing.T) {
	useShare(t, claimed, true)
	code, result := callFleet(t, GetShare, http.MethodGet, "/v1/share", "", nil)
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	data, isMap := result.Data.(map[string]any)
	if !isMap {
		t.Fatalf("data = %#v", result.Data)
	}
	for _, key := range []string{"supported", "reason", "name", "host", "domain", "tos_url", "apps", "cert", "tunnel"} {
		if _, there := data[key]; !there {
			t.Errorf("status has no %q: %v", key, data)
		}
	}
}

func TestPutShareNameMapsErrors(t *testing.T) {
	cases := []struct {
		name   string
		remote bool
		body   string
		reply  relayReply
		status int
		reason string
		retry  string
	}{
		{"bad name", true, `{"name":"A!","accept_tos":true}`, *claimed, 400, "", ""},
		{"terms not accepted", true, `{"name":"maison","accept_tos":false}`, *claimed, 400, "", ""},
		{"unreadable body", true, `{"name":`, *claimed, 400, "", ""},
		{"name taken", true, `{"name":"maison","accept_tos":true}`, relayReply{status: 409, body: `{"error":"name taken"}`}, 409, "", ""},
		{"relay without sharing", true, `{"name":"maison","accept_tos":true}`, relayReply{status: 404, body: `{}`}, 503, "relay", ""},
		{"remote access off", false, `{"name":"maison","accept_tos":true}`, *claimed, 503, "remote-access", ""},
		{"relay busy", true, `{"name":"maison","accept_tos":true}`, relayReply{status: 429, retry: 7 * time.Second}, 429, "", "7"},
		{"relay busy 1.5 s", true, `{"name":"maison","accept_tos":true}`, relayReply{status: 429, retry: 1500 * time.Millisecond}, 429, "", "2"},
		{"relay busy 0.3 s", true, `{"name":"maison","accept_tos":true}`, relayReply{status: 429, retry: 300 * time.Millisecond}, 429, "", "1"},
		{"relay failing", true, `{"name":"maison","accept_tos":true}`, relayReply{status: 500, body: `{"error":"secret detail"}`}, 500, "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			reply := c.reply
			useShare(t, &reply, c.remote)
			rec := callFleetRec(t, PutShareName, http.MethodPut, "/v1/share/name", c.body, nil)
			if rec.Code != c.status {
				t.Fatalf("status = %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			if got := rec.Header().Get("Retry-After"); got != c.retry {
				t.Errorf("Retry-After = %q, want %q", got, c.retry)
			}
			if c.reason != "" && !strings.Contains(rec.Body.String(), `"reason":"`+c.reason+`"`) {
				t.Errorf("no reason %q in %s", c.reason, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "secret detail") || strings.Contains(rec.Body.String(), "status 500") {
				t.Errorf("the relay's own words reach the answer: %s", rec.Body)
			}
		})
	}
	t.Run("claimed", func(t *testing.T) {
		useShare(t, claimed, true)
		code, result := callFleet(t, PutShareName, http.MethodPut, "/v1/share/name", `{"name":"maison","accept_tos":true}`, nil)
		if data, _ := result.Data.(map[string]any); code != http.StatusOK || data["name"] != "maison" {
			t.Fatalf("status = %d, data = %#v", code, result.Data)
		}
		if code, _ := callFleet(t, PutShareName, http.MethodPut, "/v1/share/name", `{"name":"autre","accept_tos":true}`, nil); code != http.StatusConflict {
			t.Errorf("a second name = %d, want 409", code)
		}
	})
}

func TestShareAndUnshareApp(t *testing.T) {
	useShare(t, claimed, true)
	app := map[string]string{"name": "jellyfin"}
	if code, _ := callFleet(t, PutShareApp, http.MethodPut, "/v1/share/apps/jellyfin", "", app); code != http.StatusConflict {
		t.Fatalf("sharing before a name = %d, want 409", code)
	}
	callFleet(t, PutShareName, http.MethodPut, "/v1/share/name", `{"name":"maison","accept_tos":true}`, nil)

	code, result := callFleet(t, PutShareApp, http.MethodPut, "/v1/share/apps/jellyfin", "", app)
	if code != http.StatusOK {
		t.Fatalf("share = %d %s", code, result.Message)
	}
	apps, _ := result.Data.(map[string]any)["apps"].([]any)
	var shared []string
	for _, a := range apps {
		if a := a.(map[string]any); a["shared"] == true {
			shared = append(shared, a["name"].(string))
		}
	}
	if len(shared) != 1 || shared[0] != "jellyfin" {
		t.Errorf("shared apps = %v, want [jellyfin]", shared)
	}
	if code, _ := callFleet(t, PutShareApp, http.MethodPut, "/v1/share/apps/nope", "", map[string]string{"name": "nope"}); code != http.StatusNotFound {
		t.Errorf("an unknown app = %d, want 404", code)
	}
	if code, _ := callFleet(t, PutShareApp, http.MethodPut, "/v1/share/apps/nothing", "", map[string]string{"name": "nothing"}); code != http.StatusConflict {
		t.Errorf("an app with no port = %d, want 409", code)
	}
	if code, _ := callFleet(t, DeleteShareApp, http.MethodDelete, "/v1/share/apps/jellyfin", "", app); code != http.StatusOK {
		t.Errorf("unshare = %d", code)
	}
	callFleet(t, PutShareApp, http.MethodPut, "/v1/share/apps/jellyfin", "", app)
	if code, _ := callFleet(t, DeleteShareApps, http.MethodDelete, "/v1/share/apps", "", nil); code != http.StatusOK {
		t.Errorf("stop all = %d", code)
	}
}

// sharedJelly is a box named and sharing jellyfin, for the guard routes.
func sharedJelly(t *testing.T) {
	t.Helper()
	useShare(t, claimed, true)
	callFleet(t, PutShareName, http.MethodPut, "/v1/share/name", `{"name":"maison","accept_tos":true}`, nil)
	if code, _ := callFleet(t, PutShareApp, http.MethodPut, "/v1/share/apps/jellyfin", "", map[string]string{"name": "jellyfin"}); code != http.StatusOK {
		t.Fatalf("share = %d", code)
	}
}

func TestShareGuardPeopleAnswers(t *testing.T) {
	sharedJelly(t)
	code, result := callFleet(t, PostShareGuardPerson, http.MethodPost, "/v1/share/guard/people", `{"name":"Maman","apps":["jellyfin"]}`, nil)
	data, _ := result.Data.(map[string]any)
	if code != http.StatusOK || data["code"] == "" || data["code"] == nil {
		t.Fatalf("add = %d %#v", code, result)
	}
	if code, result := callFleet(t, PostShareGuardPerson, http.MethodPost, "/v1/share/guard/people", `{"name":"","apps":[]}`, nil); code != http.StatusBadRequest || result.Message != "bad_name" {
		t.Fatalf("bad name = %d %q", code, result.Message)
	}

	rec := callFleetRec(t, GetShareGuard, http.MethodGet, "/v1/share/guard", "", nil)
	var view struct {
		Data struct {
			Supported bool                      `json:"supported"`
			People    []map[string]any          `json:"people"`
			Apps      map[string]map[string]any `json:"apps"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil || rec.Code != http.StatusOK || !view.Data.Supported || len(view.Data.People) != 1 {
		t.Fatalf("view = %d %s", rec.Code, rec.Body)
	}
	if _, has := view.Data.Apps["jellyfin"]; !has {
		t.Errorf("no jellyfin in %s", rec.Body)
	}
	for _, leak := range []string{"code", "code_hash", "secret"} {
		if _, has := view.Data.People[0][leak]; has || strings.Contains(rec.Body.String(), `"`+leak+`"`) {
			t.Errorf("%q in the view: %s", leak, rec.Body)
		}
	}
	if strings.Contains(rec.Body.String(), data["code"].(string)) {
		t.Error("the code is in the view")
	}
	if code, _ := callFleet(t, PutShareGuardProtection, http.MethodPut, "/v1/share/guard/protection", `{}`, nil); code != http.StatusBadRequest {
		t.Errorf("protection without on = %d, want 400", code)
	}
	if code, _ := callFleet(t, PutShareGuardProtection, http.MethodPut, "/v1/share/guard/protection", `{"on":true}`, nil); code != http.StatusOK {
		t.Errorf("protection = %d", code)
	}
	id := view.Data.People[0]["id"].(string)
	if code, _ := callFleet(t, DeleteShareGuardPerson, http.MethodDelete, "/v1/share/guard/people/"+id, "", map[string]string{"id": id}); code != http.StatusNoContent {
		t.Errorf("delete = %d", code)
	}
}

// Through a real Echo router: it routes on the raw path and hands the parameter escaped, so the handler unescapes it
// once and the Gateway sees it escaped once.
func TestShareGuardUnbanKeyReachesGatewayEscapedOnce(t *testing.T) {
	useShare(t, claimed, true)
	e := echo.New()
	e.DELETE("/v1/share/guard/bans/:key", DeleteShareGuardBan)
	rec := httptest.NewRecorder()
	e.ServeHTTP(rec, httptest.NewRequest(http.MethodDelete, "/v1/share/guard/bans/2001:db8::%2F64", nil))
	if got := guardCalls(); rec.Code != http.StatusNoContent || len(got) != 1 || got[0] != "DELETE /v1/gateway/guard/bans/2001:db8::%2F64" {
		t.Fatalf("status %d, gateway %v", rec.Code, got)
	}
}

func TestShareGuardPushFailureAnswers(t *testing.T) {
	sharedJelly(t)
	guardSeen.Lock()
	guardSeen.put = http.StatusInternalServerError
	guardSeen.Unlock()
	code, result := callFleet(t, PutShareGuardProtection, http.MethodPut, "/v1/share/guard/protection", `{"on":true}`, nil)
	if code != http.StatusInternalServerError || !strings.Contains(result.Message, "saved") {
		t.Fatalf("protection = %d %q", code, result.Message)
	}
	// a code is never lost: the change is saved, the resync sends it
	code, result = callFleet(t, PostShareGuardPerson, http.MethodPost, "/v1/share/guard/people", `{"name":"Maman","apps":["jellyfin"]}`, nil)
	if data, _ := result.Data.(map[string]any); code != http.StatusOK || data["code"] == nil {
		t.Fatalf("add = %d %#v", code, result)
	}
}
