package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sync"
	"time"
)

var (
	ErrRelayNoShare   = errors.New("this relay does not support public sharing")
	ErrWaitMaster     = errors.New("waiting for the master to register this box at the relay")
	ErrNoName         = errors.New("this box has no public name yet")
	ErrNameTaken      = errors.New("this name is already taken")
	ErrNameReserved   = errors.New("this name is reserved")
	ErrNameRevoked    = errors.New("this name was revoked and cannot be used")
	ErrNameQuarantine = errors.New("this name was released recently and is not available yet")
	// ErrRelayRejected is a 400: the relay will never accept this request as sent.
	ErrRelayRejected = errors.New("the relay refused")
)

// BusyError is the relay asking to come back later.
type BusyError struct{ RetryAfter time.Duration }

func (e BusyError) Error() string { return "the relay is busy, try again later" }

// Relay talks to the relay as this box.
type Relay struct {
	// Signed sends a request signed with this box's key (wired by main to the fleet's relay client), keeping at most
	// maxReply bytes of the answer (0: the client's 64 KiB).
	Signed func(ctx context.Context, method, path string, body []byte, maxReply int) (status int, reply []byte, retryAfter time.Duration, err error)
	// RegisterSelf links this box's key at the relay (wired to the fleet).
	RegisterSelf func(ctx context.Context) error

	mu    sync.Mutex
	gates map[string]*pathGate // per "METHOD path"
}

// pathGate keeps one method and path to one signed request a second: the signature covers the method, the path and the
// second only, and the relay refuses one it has already seen.
type pathGate struct {
	sync.Mutex
	done time.Time // when the last request returned: its signature's second is at most this one
}

// signed is Signed through the gate of method and path, waiting for the next second when needed. It uses the wall
// clock, as the signer does.
func (r *Relay) signed(ctx context.Context, method, path string, body []byte, maxReply int) (int, []byte, time.Duration, error) {
	r.mu.Lock()
	if r.gates == nil {
		r.gates = map[string]*pathGate{}
	}
	g := r.gates[method+" "+path]
	if g == nil {
		g = &pathGate{}
		r.gates[method+" "+path] = g
	}
	r.mu.Unlock()
	g.Lock()
	defer g.Unlock()
	// at most a second: a clock set back leaves done ahead of now
	if time.Now().Unix() <= g.done.Unix() && !sleep(ctx, min(time.Until(g.done.Truncate(time.Second).Add(time.Second)), time.Second), nil) {
		return 0, nil, 0, ctx.Err()
	}
	status, reply, retry, err := r.Signed(ctx, method, path, body, maxReply)
	g.done = time.Now()
	return status, reply, retry, err
}

// Info is a box's public name at the relay.
type Info struct {
	Name         string `json:"name"`
	Host         string `json:"host"`
	Domain       string `json:"domain"`
	TunnelOnline bool   `json:"tunnel_online"`
}

// ClaimName asks for name as this box's public name.
func (r *Relay) ClaimName(ctx context.Context, name string) (Info, error) {
	body, _ := json.Marshal(map[string]string{"name": name})
	return r.info(ctx, http.MethodPut, "/v1/share/name", body)
}

// ReleaseName gives the public name back (also fine when there was none).
func (r *Relay) ReleaseName(ctx context.Context) error {
	_, _, err := r.call(ctx, http.MethodDelete, "/v1/share/name", nil, 0)
	return err
}

// Status tells what the relay knows of this box's name and tunnel.
func (r *Relay) Status(ctx context.Context) (Info, error) {
	return r.info(ctx, http.MethodGet, "/v1/share", nil)
}

// SetTXT publishes value as the ACME challenge record of this box's name.
func (r *Relay) SetTXT(ctx context.Context, value string) error {
	body, _ := json.Marshal(map[string]string{"value": value})
	_, _, err := r.call(ctx, http.MethodPut, "/v1/share/acme", body, 0)
	return err
}

// ClearTXT removes the ACME challenge record.
func (r *Relay) ClearTXT(ctx context.Context) error {
	_, _, err := r.call(ctx, http.MethodDelete, "/v1/share/acme", nil, 0)
	return err
}

// RelayProtection is what the relay holds of this box's protection settings.
type RelayProtection struct {
	Filter           bool       `json:"relay_filter"`
	ShareBans        bool       `json:"share_bans"`
	ShareBansAt      *time.Time `json:"share_bans_at"`
	CommunitySize    int        `json:"community_size"`
	ContributedMonth int        `json:"contributed_month"`
}

// BanReport is one ban of this box's Gateway, as sent to the relay.
type BanReport struct {
	Key    string    `json:"key"`
	Reason string    `json:"reason"`
	App    string    `json:"app"`
	Since  time.Time `json:"since"`
}

// boxCall is call for the protection routes: any 404 there means the relay is too old.
func (r *Relay) boxCall(ctx context.Context, method, path string, body []byte, maxReply int) ([]byte, error) {
	_, reply, err := r.call(ctx, method, path, body, maxReply)
	if errors.Is(err, ErrNoName) {
		err = ErrRelayNoShare
	}
	return reply, err
}

// SetProtection pushes the two protection settings to the relay.
func (r *Relay) SetProtection(ctx context.Context, filter, shareBans bool) (RelayProtection, error) {
	body, _ := json.Marshal(map[string]bool{"relay_filter": filter, "share_bans": shareBans})
	reply, err := r.boxCall(ctx, http.MethodPut, "/v1/share/protection", body, 0)
	if err != nil {
		return RelayProtection{}, err
	}
	var p RelayProtection
	if err := json.Unmarshal(reply, &p); err != nil {
		return RelayProtection{}, fmt.Errorf("the relay answered something unreadable: %w", err)
	}
	return p, nil
}

// ReportBans sends the bans this box's Gateway started; ErrRelayRejected means drop the batch.
func (r *Relay) ReportBans(ctx context.Context, bans []BanReport) error {
	out := make([]BanReport, len(bans))
	for i, b := range bans {
		b.Since = b.Since.UTC().Truncate(time.Second)
		out[i] = b
	}
	body, _ := json.Marshal(map[string]any{"bans": out})
	_, err := r.boxCall(ctx, http.MethodPost, "/v1/share/bans", body, 0)
	return err
}

// refusalsMax holds the relay's longest answer: 2 000 rows of about 110 bytes.
const refusalsMax = 512 << 10

// Refusals returns the relay's refusals of this box's name over the last 7 days, as the relay wrote them.
func (r *Relay) Refusals(ctx context.Context) (json.RawMessage, error) {
	reply, err := r.boxCall(ctx, http.MethodGet, "/v1/share/refusals", nil, refusalsMax)
	if err != nil {
		return nil, err
	}
	if !json.Valid(reply) { // also an answer cut at refusalsMax
		return nil, errors.New("the relay answered something unreadable")
	}
	return json.RawMessage(reply), nil
}

func (r *Relay) info(ctx context.Context, method, path string, body []byte) (Info, error) {
	_, reply, err := r.call(ctx, method, path, body, 0)
	if err != nil {
		return Info{}, err
	}
	var info Info
	if err := json.Unmarshal(reply, &info); err != nil {
		return Info{}, fmt.Errorf("the relay answered something unreadable: %w", err)
	}
	return info, nil
}

// errText is the relay's {"error": "..."} text, or "" when the body has none.
func errText(body []byte) string {
	var e struct {
		Error string `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	return e.Error
}

// call sends the request, linking this box's key at the relay once if the relay does not know it yet.
// It returns the status and body of a 2xx; every other answer is an error.
func (r *Relay) call(ctx context.Context, method, path string, body []byte, maxReply int) (int, []byte, error) {
	registered := false
	for {
		status, reply, retry, err := r.signed(ctx, method, path, body, maxReply)
		if err != nil {
			return 0, nil, err
		}
		text := errText(reply)
		switch {
		case status >= 200 && status < 300:
			return status, reply, nil
		case status == http.StatusForbidden && text == "box key not linked":
			if registered {
				return 0, nil, ErrWaitMaster
			}
			if r.RegisterSelf == nil {
				return 0, nil, ErrWaitMaster
			}
			if err := r.RegisterSelf(ctx); err != nil {
				if errors.Is(err, ErrWaitMaster) {
					return 0, nil, ErrWaitMaster
				}
				return 0, nil, err
			}
			registered = true // the retry waits for the next second: the relay recorded the refused signature
			continue
		case status == http.StatusTooManyRequests:
			return 0, nil, BusyError{RetryAfter: retry}
		case status == http.StatusUnauthorized:
			return 0, nil, errors.New("the relay rejected this box's signature: check the clock")
		case status == http.StatusNotFound:
			if text == "no name" {
				return 0, nil, ErrNoName
			}
			return 0, nil, ErrRelayNoShare
		case status == http.StatusServiceUnavailable && text == "sharing not enabled":
			return 0, nil, ErrRelayNoShare
		case status >= 500:
			return 0, nil, fmt.Errorf("the relay failed (status %d)", status)
		case status == http.StatusConflict:
			for msg, e := range map[string]error{"name taken": ErrNameTaken, "name reserved": ErrNameReserved,
				"name revoked": ErrNameRevoked, "name in quarantine": ErrNameQuarantine} {
				if text == msg {
					return 0, nil, e
				}
			}
		}
		if status == http.StatusBadRequest && text == "" {
			return 0, nil, fmt.Errorf("%w (status 400)", ErrRelayRejected)
		}
		if text != "" {
			if status == http.StatusBadRequest {
				return 0, nil, fmt.Errorf("%w: %s", ErrRelayRejected, truncate(text, 200))
			}
			return 0, nil, fmt.Errorf("the relay refused: %s", truncate(text, 200))
		}
		return 0, nil, fmt.Errorf("the relay answered status %d", status)
	}
}

func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n])
	}
	return s
}
