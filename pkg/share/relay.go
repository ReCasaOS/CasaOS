package share

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
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
)

// BusyError is the relay asking to come back later.
type BusyError struct{ RetryAfter time.Duration }

func (e BusyError) Error() string { return "the relay is busy, try again later" }

// Relay talks to the relay as this box.
type Relay struct {
	// Signed sends a request signed with this box's key (wired by main to the fleet's relay client).
	Signed func(ctx context.Context, method, path string, body []byte) (status int, reply []byte, retryAfter time.Duration, err error)
	// RegisterSelf links this box's key at the relay (wired to the fleet).
	RegisterSelf func(ctx context.Context) error
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
	_, _, err := r.call(ctx, http.MethodDelete, "/v1/share/name", nil)
	return err
}

// Status tells what the relay knows of this box's name and tunnel.
func (r *Relay) Status(ctx context.Context) (Info, error) {
	return r.info(ctx, http.MethodGet, "/v1/share", nil)
}

// SetTXT publishes value as the ACME challenge record of this box's name.
func (r *Relay) SetTXT(ctx context.Context, value string) error {
	body, _ := json.Marshal(map[string]string{"value": value})
	_, _, err := r.call(ctx, http.MethodPut, "/v1/share/acme", body)
	return err
}

// ClearTXT removes the ACME challenge record.
func (r *Relay) ClearTXT(ctx context.Context) error {
	_, _, err := r.call(ctx, http.MethodDelete, "/v1/share/acme", nil)
	return err
}

func (r *Relay) info(ctx context.Context, method, path string, body []byte) (Info, error) {
	_, reply, err := r.call(ctx, method, path, body)
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
func (r *Relay) call(ctx context.Context, method, path string, body []byte) (int, []byte, error) {
	registered := false
	for {
		status, reply, retry, err := r.Signed(ctx, method, path, body)
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
				return 0, nil, fmt.Errorf("%w: %v", ErrWaitMaster, err)
			}
			registered = true
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
		if text != "" {
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
