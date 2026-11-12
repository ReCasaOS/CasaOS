package share

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
)

// ErrGatewayNoShare is what the gateway of a box without public sharing gives.
var ErrGatewayNoShare = errors.New("the gateway on this box does not support public sharing yet")

// Gateway is the box's gateway, reached through the runtime directory.
type Gateway struct{ RuntimePath func() string }

// ShareAddr is the address the gateway hands the relay's connections to.
func (g Gateway) ShareAddr() (string, error) {
	data, err := os.ReadFile(filepath.Join(g.RuntimePath(), "share.addr"))
	if err != nil {
		return "", ErrGatewayNoShare
	}
	return strings.TrimSpace(string(data)), nil
}

// SetHost makes the gateway send requests for host to target.
func (g Gateway) SetHost(ctx context.Context, host, target string) error {
	body, _ := json.Marshal(map[string]string{"host": host, "target": target})
	return g.call(ctx, http.MethodPut, "/v1/gateway/hosts", body, http.StatusCreated)
}

// DeleteHost stops the gateway sending requests for host anywhere.
func (g Gateway) DeleteHost(ctx context.Context, host string) error {
	return g.call(ctx, http.MethodDelete, "/v1/gateway/hosts/"+url.PathEscape(host), nil, http.StatusNoContent)
}

func (g Gateway) call(ctx context.Context, method, path string, body []byte, ok int) error {
	runtimePath := g.RuntimePath()
	status, message, err := internalCall(ctx, runtimePath, external.ManagementURLFilename, method, path, body)
	if err != nil {
		return fmt.Errorf("gateway: %w", err)
	}
	switch {
	case status == ok:
		return nil
	case status == http.StatusNotFound || status == http.StatusMethodNotAllowed:
		return ErrGatewayNoShare // an older gateway has neither route
	}
	return fmt.Errorf("gateway answered %d: %s", status, message)
}

// internalCall asks the service whose address is in urlFile of the runtime directory, as the core.
// It returns the status and, for anything but a success, the message of the JSON answer.
func internalCall(ctx context.Context, runtimePath, urlFile, method, path string, body []byte) (int, string, error) {
	address, err := os.ReadFile(filepath.Join(runtimePath, urlFile))
	if err != nil {
		return 0, "", err
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(strings.TrimSpace(string(address)), "/")+path, bytes.NewReader(body))
	if err != nil {
		return 0, "", err
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	_ = external.InternalRequestEditor(runtimePath)(ctx, request) // never fails
	response, err := (&http.Client{}).Do(request)
	if err != nil {
		return 0, "", err
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return response.StatusCode, string(data), nil
	}
	var failure struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(data, &failure) != nil {
		failure.Message = ""
	}
	return response.StatusCode, failure.Message, nil
}

// ErrGatewayNoGuard is what a Gateway without the protection of shared apps gives: a 404 on /v1/gateway/guard.
var ErrGatewayNoGuard = errors.New("the gateway on this box cannot protect shared apps")

// GatewayGuardConfig is the body of PUT /v1/gateway/guard; apps are keyed by the host registered for them.
type GatewayGuardConfig struct {
	Secret     []byte                     `json:"secret"` // base64 std in JSON
	Protection bool                       `json:"protection"`
	Apps       map[string]GatewayGuardApp `json:"apps"`
	People     []GatewayPerson            `json:"people"`
}

type GatewayGuardApp struct {
	App    string `json:"app"`
	Access string `json:"access"`
	Policy Policy `json:"policy"` // the effective one: the Gateway never merges
}

// GatewayPerson is what the Gateway needs of a person: no name, no dates.
type GatewayPerson struct {
	ID       string   `json:"id"`
	Gen      int      `json:"gen"`
	CodeHash string   `json:"code_hash"`
	Apps     []string `json:"apps"`
}

// GuardGatewayStatus is the body of GET /v1/gateway/guard.
type GuardGatewayStatus struct {
	Version    int  `json:"version"`
	Configured bool `json:"configured"`
	Geo        struct {
		Country bool `json:"country"`
		ASN     bool `json:"asn"`
	} `json:"geo"`
	PeopleLastUsed map[string]time.Time `json:"people_last_used"`
	Waves          []GuardWave          `json:"waves"`
}

type GuardWave struct {
	App  string `json:"app"`
	Bans int    `json:"bans"`
}

// GuardStatus is the Gateway's view of the protection; supported is false only when it has no guard route.
func (g Gateway) GuardStatus(ctx context.Context) (GuardGatewayStatus, bool, error) {
	var st GuardGatewayStatus
	body, err := g.guardCall(ctx, http.MethodGet, "/v1/gateway/guard", nil, http.StatusOK)
	if errors.Is(err, ErrGatewayNoGuard) {
		return st, false, nil
	}
	if err == nil {
		err = json.Unmarshal(body, &st)
	}
	return st, true, err
}

// SetGuard replaces the Gateway's whole protection config.
func (g Gateway) SetGuard(ctx context.Context, cfg GatewayGuardConfig) error {
	body, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	_, err = g.guardCall(ctx, http.MethodPut, "/v1/gateway/guard", body, http.StatusNoContent)
	return err
}

// GuardEvents is the Gateway's journal, newest first; empty filters and limit 0 are left out.
func (g Gateway) GuardEvents(ctx context.Context, app, reason string, limit int) (json.RawMessage, error) {
	q := url.Values{}
	if app != "" {
		q.Set("app", app)
	}
	if reason != "" {
		q.Set("reason", reason)
	}
	if limit > 0 {
		q.Set("limit", strconv.Itoa(limit))
	}
	path := "/v1/gateway/guard/events"
	if len(q) > 0 {
		path += "?" + q.Encode()
	}
	return g.guardCall(ctx, http.MethodGet, path, nil, http.StatusOK)
}

// GuardBans is the Gateway's active bans.
func (g Gateway) GuardBans(ctx context.Context) (json.RawMessage, error) {
	return g.guardCall(ctx, http.MethodGet, "/v1/gateway/guard/bans", nil, http.StatusOK)
}

// Unban lifts the ban on key now.
func (g Gateway) Unban(ctx context.Context, key string) error {
	_, err := g.guardCall(ctx, http.MethodDelete, "/v1/gateway/guard/bans/"+url.PathEscape(key), nil, http.StatusNoContent)
	return err
}

// guardCall is call for the guard routes: only a 404 means the Gateway has none, and the answer's body comes back.
func (g Gateway) guardCall(ctx context.Context, method, path string, body []byte, ok int) (json.RawMessage, error) {
	status, answer, err := internalCall(ctx, g.RuntimePath(), external.ManagementURLFilename, method, path, body)
	switch {
	case err != nil:
		return nil, fmt.Errorf("gateway: %w", err)
	case status == ok:
		return json.RawMessage(answer), nil
	case status == http.StatusNotFound:
		return nil, ErrGatewayNoGuard
	}
	return nil, fmt.Errorf("gateway answered %d: %s", status, answer)
}
