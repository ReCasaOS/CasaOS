package fleet

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// The errors of the relay API, mapped from its status codes (see recasaos-relay,
// README "API"). None carries a secret, a key, a signature or anything the relay
// sent back: a response body is never quoted.
var (
	// ErrRelayBusy: 429, 502 or 503. Retry later; the error is a *RelayBusyError carrying
	// the relay's Retry-After, when it gave one. A 503 "relay full" on CreateFleet is ErrRelayFull.
	ErrRelayBusy = errors.New("the relay is busy, try again later")
	// ErrRelayQuota: 409. The fleet is at its node quota, or holds too many unused join keys.
	ErrRelayQuota = errors.New("the relay refused: this fleet has reached its limit of boxes or unused join keys")
	// ErrRelayAuth: 401. The relay rejected the fleet secret (or, for CreateFleet, the box's signature,
	// which includes a clock too far off).
	ErrRelayAuth = errors.New("the relay rejected this box's credentials")
	// ErrRelayFleetGone: 404 on JoinKey, RemoveNode or FleetNodes, and the relay's JSON "fleet not
	// found" 404 on DeviceCode, Devices and RemoveDevice. The relay no longer knows the fleet, for
	// instance after it idled out.
	ErrRelayFleetGone = errors.New("the relay no longer knows this fleet")
	// ErrRelayFailed: another 5xx, the relay's own failure; the error names the status.
	ErrRelayFailed = errors.New("the relay failed to answer")
	// ErrRelayUnreachable wraps every transport failure (and the caller's cancelled context);
	// the cause stays reachable with errors.Is.
	ErrRelayUnreachable = errors.New("the relay cannot be reached")
	// ErrRelayNoDevices: 404 without the relay's JSON error on a device call. A relay older than
	// remote access answers an unknown path with Go's plain "404 page not found".
	ErrRelayNoDevices = errors.New("this relay does not offer remote access yet: its operator must update it")
	// ErrRelayFull: 503 "relay full, retry later" on CreateFleet. Unlike ErrRelayBusy, waiting a few
	// seconds does not help.
	ErrRelayFull = errors.New("the relay is full and takes no new network for now: try again later, or use another relay")
	// ErrTooManyDeviceCodes: 409 on DeviceCode.
	ErrTooManyDeviceCodes = errors.New("too many codes not used yet: use one, or wait until they expire (10 minutes)")
	// ErrRelayNoShare: 404 without the relay's JSON error, or 503 "sharing not enabled".
	ErrRelayNoShare = errors.New("this relay does not support public sharing")
	// ErrNodeUnknown: 404 {"error":"node not found"} on SetBoxKey.
	ErrNodeUnknown = errors.New("the relay does not know this box's node")
	// ErrBoxKeyLinked: 409 on SetBoxKey.
	ErrBoxKeyLinked = errors.New("a different key is already linked to this box at the relay")
)

// RelayBusyError is the ErrRelayBusy a client gets, with how long the relay asked to wait
// (0 when it did not say). errors.Is(err, ErrRelayBusy) holds.
type RelayBusyError struct{ RetryAfter time.Duration }

func (e *RelayBusyError) Error() string {
	if e.RetryAfter > 0 {
		return fmt.Sprintf("%s (in %s)", ErrRelayBusy, e.RetryAfter)
	}
	return ErrRelayBusy.Error()
}

func (e *RelayBusyError) Is(target error) bool { return target == ErrRelayBusy }

const (
	relayTimeout = 15 * time.Second
	relayMaxBody = 64 << 10
)

// FleetCreds is what the relay hands out once, when a fleet is created.
type FleetCreds struct {
	FleetID     string
	Secret      string
	LoginServer string // the coordination server boxes join; valid by the ValidMasterURL rule
}

// RelayClient talks to a recasaos-relay. The zero value works except for Dir.
type RelayClient struct {
	HTTP *http.Client     // nil: a client with a 15 s timeout. Redirects are never followed.
	Now  func() time.Time // nil: time.Now. Stamps X-Box-Time.
	Dir  string           // where boxkey.json lives (StateDir under Root on a box); required by CreateFleet
}

func (c *RelayClient) now() time.Time {
	if c.Now != nil {
		return c.Now()
	}
	return time.Now()
}

// client is a copy of c.HTTP, so a caller's client is not changed by the redirect rule.
func (c *RelayClient) client() *http.Client {
	hc := http.Client{Timeout: relayTimeout}
	if c.HTTP != nil {
		hc = *c.HTTP
	}
	hc.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return &hc
}

type relayReply struct {
	status     int
	body       []byte
	retryAfter time.Duration
}

// do sends one request. relay must pass ValidMasterURL (https, or http on loopback) before anything
// leaves. A transport error is returned without its URL, so nothing but the cause is kept.
func (c *RelayClient) do(ctx context.Context, method, relay, path string, hdr http.Header, body []byte) (relayReply, error) {
	return c.doMax(ctx, method, relay, path, hdr, body, relayMaxBody)
}

// doMax is do keeping at most maxBody bytes of the answer.
func (c *RelayClient) doMax(ctx context.Context, method, relay, path string, hdr http.Header, body []byte, maxBody int) (relayReply, error) {
	if err := ValidMasterURL(relay); err != nil {
		return relayReply{}, fmt.Errorf("the relay address is not valid: %w", err)
	}
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(relay, "/")+path, rd)
	if err != nil {
		return relayReply{}, errors.New("cannot build the relay request")
	}
	req.Header = hdr
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.client().Do(req)
	if err == nil {
		defer resp.Body.Close()
	}
	var out []byte
	if err == nil {
		out, err = io.ReadAll(io.LimitReader(resp.Body, int64(maxBody)))
	}
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return relayReply{}, fmt.Errorf("%w: %w", ErrRelayUnreachable, err)
	}
	r := relayReply{status: resp.StatusCode, body: out}
	// shortcut: only the delay-seconds form of Retry-After, the one the relay sends; an HTTP date reads as no delay.
	if secs, err := strconv.Atoi(resp.Header.Get("Retry-After")); err == nil && secs > 0 {
		r.retryAfter = time.Duration(secs) * time.Second
	}
	return r, nil
}

// statusError maps a status the caller did not expect. 404 is the caller's to read: it means
// "fleet gone" for some calls and "already deleted" for another.
func (r relayReply) statusError() error {
	switch r.status {
	case http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable:
		return &RelayBusyError{RetryAfter: r.retryAfter}
	case http.StatusConflict:
		return ErrRelayQuota
	case http.StatusUnauthorized:
		return ErrRelayAuth
	}
	if r.status >= 500 {
		return fmt.Errorf("%w (status %d)", ErrRelayFailed, r.status)
	}
	return fmt.Errorf("the relay answered with status %d", r.status)
}

// errorText is the "error" field of a JSON body, "" when the body is anything else. It is only
// compared, never quoted.
func (r relayReply) errorText() string {
	var v struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(r.body, &v) != nil {
		return ""
	}
	return v.Error
}

// fleetGone reads a 404 on a call a relay older than remote access does not have: the relay's own
// JSON "fleet not found" is ErrRelayFleetGone, anything else (Go's plain 404) is ErrRelayNoDevices.
func fleetGone(r relayReply) error {
	if r.errorText() == "fleet not found" {
		return ErrRelayFleetGone
	}
	return ErrRelayNoDevices
}

func bearer(secret string) http.Header {
	return http.Header{"Authorization": {"Bearer " + secret}}
}

// boxHeaders are the three X-Box-* headers: the box's public key and a signature over "<METHOD> <PATH>\n"+ts.
func boxHeaders(key ed25519.PrivateKey, method, path, ts string) http.Header {
	enc := base64.RawURLEncoding
	return http.Header{
		"X-Box-Key":       {enc.EncodeToString(key.Public().(ed25519.PublicKey))},
		"X-Box-Time":      {ts},
		"X-Box-Signature": {enc.EncodeToString(ed25519.Sign(key, []byte(method+" "+path+"\n"+ts)))},
	}
}

func fleetPath(fleetID string) string { return "/v1/fleets/" + url.PathEscape(fleetID) }

var errNoFleet = errors.New("the fleet id and its secret are required")

// CreateFleet asks the relay for a new fleet, signing with the box's key (made on first use in
// c.Dir): POST /v1/fleets, answered 201. Errors: ErrRelayAuth (signature or clock refused),
// ErrRelayFull (503 "relay full"), ErrRelayBusy (429, another 503), ErrRelayUnreachable, or an
// error naming the status or saying the answer was not valid. The login server it returns is
// checked as a master address, since boxes pass it to tailscale up.
func (c *RelayClient) CreateFleet(ctx context.Context, relay string) (FleetCreds, error) {
	if err := ValidMasterURL(relay); err != nil { // before the key file is touched
		return FleetCreds{}, fmt.Errorf("the relay address is not valid: %w", err)
	}
	key, err := loadBoxKey(c.Dir)
	if err != nil {
		return FleetCreds{}, err
	}
	r, err := c.do(ctx, http.MethodPost, relay, "/v1/fleets", boxHeaders(key, http.MethodPost, "/v1/fleets", strconv.FormatInt(c.now().Unix(), 10)), nil)
	if err != nil {
		return FleetCreds{}, err
	}
	if r.status == http.StatusUnauthorized { // the signature carries this box's clock
		return FleetCreds{}, fmt.Errorf("%w: check that this box's clock is right", ErrRelayAuth)
	}
	if r.status == http.StatusServiceUnavailable && r.errorText() == "relay full, retry later" {
		return FleetCreds{}, ErrRelayFull
	}
	if r.status != http.StatusCreated {
		return FleetCreds{}, r.statusError()
	}
	var v struct {
		FleetID     string `json:"fleet_id"`
		Secret      string `json:"secret"`
		LoginServer string `json:"login_server"`
	}
	if json.Unmarshal(r.body, &v) != nil || v.FleetID == "" || v.Secret == "" || ValidMasterURL(v.LoginServer) != nil {
		return FleetCreds{}, errors.New("the relay's answer to a fleet creation is not valid")
	}
	return FleetCreds{FleetID: v.FleetID, Secret: v.Secret, LoginServer: v.LoginServer}, nil
}

// JoinKey asks for a single-use key for one more box: POST /v1/fleets/{id}/keys with the fleet
// secret, answered 201. Errors: ErrRelayAuth, ErrRelayFleetGone (404), ErrRelayQuota (409),
// ErrRelayBusy (429, 502, 503), ErrRelayUnreachable.
func (c *RelayClient) JoinKey(ctx context.Context, relay, fleetID, secret string) (key string, expires time.Time, err error) {
	if fleetID == "" || secret == "" {
		return "", time.Time{}, errNoFleet
	}
	r, err := c.do(ctx, http.MethodPost, relay, fleetPath(fleetID)+"/keys", bearer(secret), nil)
	if err != nil {
		return "", time.Time{}, err
	}
	switch r.status {
	case http.StatusCreated:
	case http.StatusNotFound:
		return "", time.Time{}, ErrRelayFleetGone
	default:
		return "", time.Time{}, r.statusError()
	}
	var v struct {
		Key       string `json:"key"`
		ExpiresAt string `json:"expires_at"`
	}
	if json.Unmarshal(r.body, &v) != nil || validJoinKey(v.Key) != nil {
		return "", time.Time{}, errors.New("the relay's answer to a join key request is not valid")
	}
	if expires, err = time.Parse(time.RFC3339, v.ExpiresAt); err != nil {
		return "", time.Time{}, errors.New("the relay's answer to a join key request is not valid")
	}
	return v.Key, expires, nil
}

// RemoveNode removes one box from the fleet: DELETE /v1/fleets/{id}/nodes/{hostname}, answered
// 204, also for a hostname the fleet does not have. Errors as JoinKey, minus ErrRelayQuota.
func (c *RelayClient) RemoveNode(ctx context.Context, relay, fleetID, secret, hostname string) error {
	if fleetID == "" || secret == "" || hostname == "" {
		return errNoFleet
	}
	r, err := c.do(ctx, http.MethodDelete, relay, fleetPath(fleetID)+"/nodes/"+url.PathEscape(hostname), bearer(secret), nil)
	switch {
	case err != nil:
		return err
	case r.status == http.StatusNoContent:
		return nil
	case r.status == http.StatusNotFound:
		return ErrRelayFleetGone
	}
	return r.statusError()
}

// DeleteFleet deletes the fleet: DELETE /v1/fleets/{id}, answered 204. A 404 means the relay
// already deleted it (the secret authenticates nothing once the fleet is gone), which is what the
// caller wanted: nil. Errors: ErrRelayAuth, ErrRelayBusy, ErrRelayUnreachable; after one the
// same call can be repeated.
func (c *RelayClient) DeleteFleet(ctx context.Context, relay, fleetID, secret string) error {
	if fleetID == "" || secret == "" {
		return errNoFleet
	}
	r, err := c.do(ctx, http.MethodDelete, relay, fleetPath(fleetID), bearer(secret), nil)
	switch {
	case err != nil:
		return err
	case r.status == http.StatusNoContent, r.status == http.StatusNotFound:
		return nil
	}
	return r.statusError()
}

// DeviceCode is a one-time code a phone or PC types to join the fleet's network.
type DeviceCode struct {
	Code      string    `json:"code"`
	ExpiresAt time.Time `json:"expires_at"`
}

// Device is a phone or PC that joined with a code.
type Device struct {
	ID       string    `json:"id"`
	Hostname string    `json:"hostname"`
	IP       string    `json:"ip"`
	Online   bool      `json:"online"`
	LastSeen time.Time `json:"last_seen,omitzero"`
}

// FleetNode is a box of the fleet as the relay sees it.
type FleetNode struct {
	Hostname, IP string
	Online       bool
}

var (
	deviceCodeRe = regexp.MustCompile(`^[2-9A-HJ-NP-Z]{4}-[2-9A-HJ-NP-Z]{4}$`)
	deviceIDRe   = regexp.MustCompile(`^[0-9]+$`)
)

// DeviceCode asks for a device code: POST /v1/fleets/{id}/device-codes, answered 201. Errors:
// ErrRelayAuth, ErrRelayFleetGone, ErrRelayNoDevices (old relay), ErrTooManyDeviceCodes (409),
// ErrRelayBusy, ErrRelayUnreachable.
func (c *RelayClient) DeviceCode(ctx context.Context, relay, fleetID, secret string) (DeviceCode, error) {
	if fleetID == "" || secret == "" {
		return DeviceCode{}, errNoFleet
	}
	r, err := c.do(ctx, http.MethodPost, relay, fleetPath(fleetID)+"/device-codes", bearer(secret), nil)
	if err != nil {
		return DeviceCode{}, err
	}
	switch r.status {
	case http.StatusCreated:
	case http.StatusNotFound:
		return DeviceCode{}, fleetGone(r)
	case http.StatusConflict:
		return DeviceCode{}, ErrTooManyDeviceCodes
	default:
		return DeviceCode{}, r.statusError()
	}
	var v DeviceCode
	if json.Unmarshal(r.body, &v) != nil || !deviceCodeRe.MatchString(v.Code) || v.ExpiresAt.IsZero() {
		return DeviceCode{}, errors.New("the relay's answer to a device code request is not valid")
	}
	return v, nil
}

// Devices lists the fleet's devices: GET /v1/fleets/{id}/devices, answered 200. Errors as DeviceCode,
// minus ErrTooManyDeviceCodes. The slice is never nil on success.
func (c *RelayClient) Devices(ctx context.Context, relay, fleetID, secret string) ([]Device, error) {
	if fleetID == "" || secret == "" {
		return nil, errNoFleet
	}
	r, err := c.do(ctx, http.MethodGet, relay, fleetPath(fleetID)+"/devices", bearer(secret), nil)
	if err != nil {
		return nil, err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fleetGone(r)
	default:
		return nil, r.statusError()
	}
	var v struct {
		Devices []struct {
			Device
			LastSeen string `json:"last_seen"` // "" when never seen, which time.Time does not decode
		} `json:"devices"`
	}
	bad := errors.New("the relay's answer to a device list is not valid")
	if json.Unmarshal(r.body, &v) != nil {
		return nil, bad
	}
	devs := make([]Device, 0, len(v.Devices))
	for _, d := range v.Devices {
		if d.LastSeen != "" {
			t, err := time.Parse(time.RFC3339, d.LastSeen)
			if err != nil {
				return nil, bad
			}
			d.Device.LastSeen = t
		}
		devs = append(devs, d.Device)
	}
	return devs, nil
}

// RemoveDevice removes one device: DELETE /v1/fleets/{id}/devices/{id}, answered 204. The id is the
// relay's node number, digits only; anything else is refused before a request leaves.
func (c *RelayClient) RemoveDevice(ctx context.Context, relay, fleetID, secret, id string) error {
	if fleetID == "" || secret == "" {
		return errNoFleet
	}
	if !deviceIDRe.MatchString(id) {
		return errors.New("the device id is not valid")
	}
	r, err := c.do(ctx, http.MethodDelete, relay, fleetPath(fleetID)+"/devices/"+id, bearer(secret), nil)
	switch {
	case err != nil:
		return err
	case r.status == http.StatusNoContent:
		return nil
	case r.status == http.StatusNotFound:
		return fleetGone(r)
	}
	return r.statusError()
}

// FleetNodes lists the fleet's boxes: GET /v1/fleets/{id}, answered 200. Errors as Devices, but
// any 404 is ErrRelayFleetGone: that path exists on every relay version.
func (c *RelayClient) FleetNodes(ctx context.Context, relay, fleetID, secret string) ([]FleetNode, error) {
	if fleetID == "" || secret == "" {
		return nil, errNoFleet
	}
	r, err := c.do(ctx, http.MethodGet, relay, fleetPath(fleetID), bearer(secret), nil)
	if err != nil {
		return nil, err
	}
	switch r.status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrRelayFleetGone
	default:
		return nil, r.statusError()
	}
	var v struct {
		Nodes []struct {
			Hostname string `json:"hostname"`
			IP       string `json:"ip"`
			Online   bool   `json:"online"`
		} `json:"nodes"`
	}
	if json.Unmarshal(r.body, &v) != nil {
		return nil, errors.New("the relay's answer to a fleet request is not valid")
	}
	nodes := make([]FleetNode, 0, len(v.Nodes))
	for _, n := range v.Nodes {
		nodes = append(nodes, FleetNode{n.Hostname, n.IP, n.Online})
	}
	return nodes, nil
}

// SetBoxKey links the box's public key to its node: PUT /v1/fleets/{id}/nodes/{node}/box-key with the
// fleet secret, answered 204. Errors: ErrNodeUnknown, ErrRelayNoShare, ErrBoxKeyLinked, ErrRelayAuth,
// ErrRelayBusy, ErrRelayUnreachable.
func (c *RelayClient) SetBoxKey(ctx context.Context, relay, fleetID, secret, nodeID string, pub ed25519.PublicKey) error {
	if fleetID == "" || secret == "" || nodeID == "" {
		return errNoFleet
	}
	body, _ := json.Marshal(map[string]string{"box_key": base64.RawURLEncoding.EncodeToString(pub)})
	r, err := c.do(ctx, http.MethodPut, relay, fleetPath(fleetID)+"/nodes/"+url.PathEscape(nodeID)+"/box-key", bearer(secret), body)
	switch {
	case err != nil:
		return err
	case r.status == http.StatusNoContent:
		return nil
	case r.status == http.StatusNotFound:
		if r.errorText() == "node not found" {
			return ErrNodeUnknown
		}
		return ErrRelayNoShare
	case r.status == http.StatusServiceUnavailable && strings.Contains(string(r.body), "sharing not enabled"):
		return ErrRelayNoShare
	case r.status == http.StatusConflict:
		return ErrBoxKeyLinked
	}
	return r.statusError()
}

// Signed calls the relay as this box: the X-Box-* headers over "<METHOD> <PATH>\n"+time, a JSON body
// when body != nil. It returns the status, the body (cut at maxReply bytes, 64 KiB when 0) and a 429's Retry-After
// (0 otherwise) whatever the status; only a transport failure is an error (ErrRelayUnreachable).
func (c *RelayClient) Signed(ctx context.Context, relay, method, path string, body []byte, maxReply int) (status int, reply []byte, retryAfter time.Duration, err error) {
	key, err := loadBoxKey(c.Dir)
	if err != nil {
		return 0, nil, 0, err
	}
	if maxReply <= 0 {
		maxReply = relayMaxBody
	}
	r, err := c.doMax(ctx, method, relay, path, boxHeaders(key, method, path, strconv.FormatInt(c.now().Unix(), 10)), body, maxReply)
	if err != nil {
		return 0, nil, 0, err
	}
	if r.status != http.StatusTooManyRequests {
		r.retryAfter = 0
	}
	return r.status, r.body, r.retryAfter, nil
}
