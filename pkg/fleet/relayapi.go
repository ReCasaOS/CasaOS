package fleet

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// The errors of the relay API, mapped from its status codes (see recasaos-relay,
// README "API"). None carries a secret, a key, a signature or anything the relay
// sent back: a response body is never quoted.
var (
	// ErrRelayBusy: 429, 502 or 503. Retry later; the error is a *RelayBusyError carrying
	// the relay's Retry-After, when it gave one.
	ErrRelayBusy = errors.New("the relay is busy, try again later")
	// ErrRelayQuota: 409. The fleet is at its node quota, or holds too many unused join keys.
	ErrRelayQuota = errors.New("the relay refused: this fleet has reached its limit of boxes or unused join keys")
	// ErrRelayAuth: 401. The relay rejected the fleet secret (or, for CreateFleet, the box's signature,
	// which includes a clock too far off).
	ErrRelayAuth = errors.New("the relay rejected this box's credentials")
	// ErrRelayFleetGone: 404 on JoinKey or RemoveNode. The relay no longer knows the fleet, for
	// instance after it idled out.
	ErrRelayFleetGone = errors.New("the relay no longer knows this fleet")
	// ErrRelayFailed: another 5xx, the relay's own failure; the error names the status.
	ErrRelayFailed = errors.New("the relay failed to answer")
	// ErrRelayUnreachable wraps every transport failure (and the caller's cancelled context);
	// the cause stays reachable with errors.Is.
	ErrRelayUnreachable = errors.New("the relay cannot be reached")
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
func (c *RelayClient) do(ctx context.Context, method, relay, path string, hdr http.Header) (relayReply, error) {
	if err := ValidMasterURL(relay); err != nil {
		return relayReply{}, fmt.Errorf("the relay address is not valid: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(relay, "/")+path, nil)
	if err != nil {
		return relayReply{}, errors.New("cannot build the relay request")
	}
	req.Header = hdr
	resp, err := c.client().Do(req)
	if err == nil {
		defer resp.Body.Close()
	}
	var body []byte
	if err == nil {
		body, err = io.ReadAll(io.LimitReader(resp.Body, relayMaxBody))
	}
	if err != nil {
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return relayReply{}, fmt.Errorf("%w: %w", ErrRelayUnreachable, err)
	}
	r := relayReply{status: resp.StatusCode, body: body}
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

func bearer(secret string) http.Header {
	return http.Header{"Authorization": {"Bearer " + secret}}
}

func fleetPath(fleetID string) string { return "/v1/fleets/" + url.PathEscape(fleetID) }

var errNoFleet = errors.New("the fleet id and its secret are required")

// CreateFleet asks the relay for a new fleet, signing with the box's key (made on first use in
// c.Dir): POST /v1/fleets, answered 201. Errors: ErrRelayAuth (signature or clock refused),
// ErrRelayBusy (rate limit, relay full: 429, 503), ErrRelayUnreachable, or an error naming the
// status or saying the answer was not valid. The login server it returns is checked as a master
// address, since boxes pass it to tailscale up.
func (c *RelayClient) CreateFleet(ctx context.Context, relay string) (FleetCreds, error) {
	if err := ValidMasterURL(relay); err != nil { // before the key file is touched
		return FleetCreds{}, fmt.Errorf("the relay address is not valid: %w", err)
	}
	key, err := loadBoxKey(c.Dir)
	if err != nil {
		return FleetCreds{}, err
	}
	ts := strconv.FormatInt(c.now().Unix(), 10)
	enc := base64.RawURLEncoding
	r, err := c.do(ctx, http.MethodPost, relay, "/v1/fleets", http.Header{
		"X-Box-Key":       {enc.EncodeToString(key.Public().(ed25519.PublicKey))},
		"X-Box-Time":      {ts},
		"X-Box-Signature": {enc.EncodeToString(ed25519.Sign(key, []byte("POST /v1/fleets\n"+ts)))},
	})
	if err != nil {
		return FleetCreds{}, err
	}
	if r.status == http.StatusUnauthorized { // the signature carries this box's clock
		return FleetCreds{}, fmt.Errorf("%w: check that this box's clock is right", ErrRelayAuth)
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
	r, err := c.do(ctx, http.MethodPost, relay, fleetPath(fleetID)+"/keys", bearer(secret))
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
	r, err := c.do(ctx, http.MethodDelete, relay, fleetPath(fleetID)+"/nodes/"+url.PathEscape(hostname), bearer(secret))
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
	r, err := c.do(ctx, http.MethodDelete, relay, fleetPath(fleetID), bearer(secret))
	switch {
	case err != nil:
		return err
	case r.status == http.StatusNoContent, r.status == http.StatusNotFound:
		return nil
	}
	return r.statusError()
}
