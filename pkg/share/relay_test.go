package share

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

type reply struct {
	status int
	body   string
	retry  time.Duration
	err    error
}

type sent struct{ method, path, body string }

// fakeRelay answers the scripted replies in order and records what was sent.
func fakeRelay(replies ...reply) (*Relay, *[]sent) {
	var calls []sent
	r := &Relay{Signed: func(_ context.Context, method, path string, body []byte, _ int) (int, []byte, time.Duration, error) {
		calls = append(calls, sent{method, path, string(body)})
		if len(calls) > len(replies) { // not scripted: a relay that does not know the route
			return 404, []byte("404 page not found\n"), 0, nil
		}
		rp := replies[len(calls)-1]
		return rp.status, []byte(rp.body), rp.retry, rp.err
	}}
	return r, &calls
}

const infoBody = `{"name":"mybox","host":"mybox.box.example.org","domain":"box.example.org"}`

func TestClaimNameParsesTheAnswer(t *testing.T) {
	for _, status := range []int{201, 200} {
		r, calls := fakeRelay(reply{status: status, body: infoBody})
		info, err := r.ClaimName(context.Background(), "mybox")
		if err != nil || info != (Info{Name: "mybox", Host: "mybox.box.example.org", Domain: "box.example.org"}) {
			t.Fatalf("%d: %+v, %v", status, info, err)
		}
		if c := (*calls)[0]; c != (sent{"PUT", "/v1/share/name", `{"name":"mybox"}`}) {
			t.Fatalf("sent %+v", c)
		}
	}
}

func TestClaimNameErrors(t *testing.T) {
	cases := []struct {
		rp   reply
		want error
		text string
	}{
		{reply{status: 409, body: `{"error":"name taken"}`}, ErrNameTaken, ""},
		{reply{status: 409, body: `{"error":"name reserved"}`}, ErrNameReserved, ""},
		{reply{status: 409, body: `{"error":"name revoked"}`}, ErrNameRevoked, ""},
		{reply{status: 409, body: `{"error":"name in quarantine"}`}, ErrNameQuarantine, ""},
		{reply{status: 400, body: `{"error":"invalid name"}`}, nil, "invalid name"},
		{reply{status: 400, body: `<html>`}, nil, "status 400"},
		{reply{status: 404, body: "404 page not found\n"}, ErrRelayNoShare, ""},
		{reply{status: 503, body: `{"error":"sharing not enabled"}`}, ErrRelayNoShare, ""},
		{reply{status: 401}, nil, "check the clock"},
		{reply{status: 502}, nil, "status 502"},
	}
	for _, c := range cases {
		r, _ := fakeRelay(c.rp)
		_, err := r.ClaimName(context.Background(), "mybox")
		if err == nil || (c.want != nil && !errors.Is(err, c.want)) || !strings.Contains(err.Error(), c.text) {
			t.Fatalf("%d %s: %v", c.rp.status, c.rp.body, err)
		}
	}
	r, _ := fakeRelay(reply{status: 429, retry: 7 * time.Second})
	var b BusyError
	if _, err := r.ClaimName(context.Background(), "mybox"); !errors.As(err, &b) || b.RetryAfter != 7*time.Second {
		t.Fatalf("429: %v", err)
	}
}

func TestSelfRegistersOnFirstForbidden(t *testing.T) {
	forbidden := reply{status: 403, body: `{"error":"box key not linked"}`}
	r, calls := fakeRelay(forbidden, reply{status: 201, body: infoBody})
	registered := 0
	r.RegisterSelf = func(context.Context) error { registered++; return nil }
	if info, err := r.ClaimName(context.Background(), "mybox"); err != nil || info.Name != "mybox" || registered != 1 || len(*calls) != 2 {
		t.Fatalf("%+v, %v, registered %d, calls %d", info, err, registered, len(*calls))
	}

	// still not linked after a successful registration: one retry only
	r, calls = fakeRelay(forbidden, forbidden)
	r.RegisterSelf = func(context.Context) error { return nil }
	if _, err := r.ClaimName(context.Background(), "mybox"); !errors.Is(err, ErrWaitMaster) || len(*calls) != 2 {
		t.Fatalf("second 403: %v, calls %d", err, len(*calls))
	}

	boom := errors.New("boom")
	for _, c := range []struct {
		reg  func(context.Context) error
		want error
	}{
		{func(context.Context) error { return boom }, boom}, // an unreachable relay is not "waiting for the master"
		{func(context.Context) error { return ErrWaitMaster }, ErrWaitMaster},
		{nil, ErrWaitMaster},
	} {
		r, calls = fakeRelay(forbidden)
		r.RegisterSelf = c.reg
		_, err := r.ClaimName(context.Background(), "mybox")
		if err != c.want || len(*calls) != 1 {
			t.Fatalf("%v, calls %d", err, len(*calls))
		}
	}
}

// The relay records every signature it verified, the refused one too: the retry must be signed in another second.
func TestTheRetryAfterRegisteringIsSignedInTheNextSecond(t *testing.T) {
	var seen []time.Time
	r := &Relay{Signed: func(_ context.Context, method, path string, _ []byte, _ int) (int, []byte, time.Duration, error) {
		now := time.Now()
		for _, at := range seen {
			if at.Unix() == now.Unix() {
				return 401, []byte(`{"error":"replayed request"}`), 0, nil
			}
		}
		seen = append(seen, now)
		if len(seen) == 1 {
			return 403, []byte(`{"error":"box key not linked"}`), 0, nil
		}
		return 201, []byte(infoBody), 0, nil
	}}
	r.RegisterSelf = func(context.Context) error { return nil }
	if info, err := r.ClaimName(context.Background(), "mybox"); err != nil || info.Name != "mybox" {
		t.Fatalf("%+v, %v", info, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	seen = nil
	if _, err := r.ClaimName(ctx, "mybox"); !errors.Is(err, context.Canceled) {
		t.Fatalf("a cancelled wait: %v", err)
	}
}

func TestStatusAndTXT(t *testing.T) {
	r, calls := fakeRelay(reply{status: 200, body: `{"name":"mybox","host":"h","domain":"d","tunnel_online":true}`})
	info, err := r.Status(context.Background())
	if err != nil || !info.TunnelOnline || info.Name != "mybox" || (*calls)[0] != (sent{"GET", "/v1/share", ""}) {
		t.Fatalf("%+v, %v, %+v", info, err, *calls)
	}
	r, _ = fakeRelay(reply{status: 404, body: `{"error":"no name"}`})
	if _, err := r.Status(context.Background()); !errors.Is(err, ErrNoName) {
		t.Fatalf("no name: %v", err)
	}
	r, _ = fakeRelay(reply{status: 404, body: "404 page not found\n"})
	if _, err := r.Status(context.Background()); !errors.Is(err, ErrRelayNoShare) {
		t.Fatalf("old relay: %v", err)
	}
	r, _ = fakeRelay(reply{status: 200, body: "not json"})
	if _, err := r.Status(context.Background()); err == nil || !strings.Contains(err.Error(), "relay") {
		t.Fatalf("bad json: %v", err)
	}

	r, calls = fakeRelay(reply{status: 204})
	if err := r.SetTXT(context.Background(), "abc"); err != nil || (*calls)[0] != (sent{"PUT", "/v1/share/acme", `{"value":"abc"}`}) {
		t.Fatalf("%v, %+v", err, *calls)
	}
	r, _ = fakeRelay(reply{status: 404, body: `{"error":"no name"}`})
	if err := r.SetTXT(context.Background(), "abc"); !errors.Is(err, ErrNoName) {
		t.Fatalf("settxt no name: %v", err)
	}
	r, calls = fakeRelay(reply{status: 204})
	if err := r.ClearTXT(context.Background()); err != nil || (*calls)[0] != (sent{"DELETE", "/v1/share/acme", ""}) {
		t.Fatalf("%v, %+v", err, *calls)
	}
}

func TestReleaseName(t *testing.T) {
	r, calls := fakeRelay(reply{status: 204})
	if err := r.ReleaseName(context.Background()); err != nil || (*calls)[0] != (sent{"DELETE", "/v1/share/name", ""}) {
		t.Fatalf("%v, %+v", err, *calls)
	}
	boom := errors.New("network down")
	r, _ = fakeRelay(reply{err: boom})
	if err := r.ReleaseName(context.Background()); err != boom {
		t.Fatalf("got %v", err)
	}
}

func TestSetProtectionParses(t *testing.T) {
	r, calls := fakeRelay(reply{status: 200, body: `{"relay_filter":true,"share_bans":true,"share_bans_at":"2026-11-13T10:00:00Z","community_size":1234,"contributed_month":5}`})
	p, err := r.SetProtection(context.Background(), true, true)
	if err != nil || !p.Filter || !p.ShareBans || p.CommunitySize != 1234 || p.ContributedMonth != 5 || p.ShareBansAt == nil || !p.ShareBansAt.Equal(time.Date(2026, 11, 13, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("%+v, %v", p, err)
	}
	if c := (*calls)[0]; c != (sent{"PUT", "/v1/share/protection", `{"relay_filter":true,"share_bans":true}`}) {
		t.Fatalf("sent %+v", c)
	}
}

func TestSetProtection404IsUnsupported(t *testing.T) {
	for _, body := range []string{`404 page not found`, `{"error":"no name"}`} {
		r, _ := fakeRelay(reply{status: 404, body: body})
		if _, err := r.SetProtection(context.Background(), true, false); !errors.Is(err, ErrRelayNoShare) {
			t.Fatalf("%q: %v", body, err)
		}
	}
}

func TestReportBansSendsJSON(t *testing.T) {
	r, calls := fakeRelay(reply{status: 204})
	since := time.Date(2026, 11, 13, 12, 20, 0, 0, time.FixedZone("x", 7200))
	if err := r.ReportBans(context.Background(), []BanReport{{Key: "203.0.113.7/32", Reason: "scan", App: "jellyfin", Since: since}}); err != nil {
		t.Fatal(err)
	}
	want := sent{"POST", "/v1/share/bans", `{"bans":[{"key":"203.0.113.7/32","reason":"scan","app":"jellyfin","since":"2026-11-13T10:20:00Z"}]}`}
	if c := (*calls)[0]; c != want {
		t.Fatalf("sent %+v", c)
	}
	r, _ = fakeRelay(reply{status: 400, body: `{"error":"bad request"}`})
	if err := r.ReportBans(context.Background(), nil); !errors.Is(err, ErrRelayRejected) {
		t.Fatalf("400: %v", err)
	}
	r, _ = fakeRelay(reply{status: 404, body: `{"error":"no name"}`})
	if err := r.ReportBans(context.Background(), nil); !errors.Is(err, ErrRelayNoShare) {
		t.Fatalf("404: %v", err)
	}
}

func TestRefusalsPassThrough(t *testing.T) {
	const body = `[{"hour":"2026-11-13T10:00:00Z","key":"203.0.113.7/32","reason":"external","source":"FireHOL level 1","count":12}]`
	r, calls := fakeRelay(reply{status: 200, body: body})
	raw, err := r.Refusals(context.Background())
	if err != nil || string(raw) != body {
		t.Fatalf("%s, %v", raw, err)
	}
	if c := (*calls)[0]; c != (sent{"GET", "/v1/share/refusals", ""}) {
		t.Fatalf("sent %+v", c)
	}
	r, _ = fakeRelay(reply{status: 404, body: `404 page not found`})
	if _, err := r.Refusals(context.Background()); !errors.Is(err, ErrRelayNoShare) {
		t.Fatalf("404: %v", err)
	}
}

// The refusals can run past the 64 KiB of the other calls: they ask for more, and a cut answer is an error, not passed on.
func TestRefusalsAskForALongAnswerAndCheckIt(t *testing.T) {
	var asked int
	answer := `[{"key":"203.0.113.1/32"}]`
	r := &Relay{Signed: func(_ context.Context, _, _ string, _ []byte, maxReply int) (int, []byte, time.Duration, error) {
		asked = maxReply
		return 200, []byte(answer), 0, nil
	}}
	if out, err := r.Refusals(context.Background()); err != nil || string(out) != answer || asked != 512<<10 {
		t.Fatalf("%s %v, asked %d", out, err, asked)
	}
	answer = `[{"key":"203.0.1`
	if _, err := r.Refusals(context.Background()); err == nil {
		t.Fatal("a cut answer was passed on")
	}
}

// A signature covers the method, the path and the second only, and the relay refuses one it has already seen: two
// requests to the same path, back to back or at once, must be signed in different seconds.
func TestNoTwoRequestsSignedInTheSameSecond(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]bool{}
	r := &Relay{Signed: func(_ context.Context, method, path string, _ []byte, _ int) (int, []byte, time.Duration, error) {
		mu.Lock()
		defer mu.Unlock()
		k := fmt.Sprint(method, path, time.Now().Unix())
		if seen[k] {
			return 401, []byte(`{"error":"replayed request"}`), 0, nil
		}
		seen[k] = true
		return 200, []byte("[]"), 0, nil
	}}
	ctx := context.Background()
	for range 2 {
		if _, err := r.Refusals(ctx); err != nil {
			t.Fatal(err)
		}
	}
	errs := make(chan error, 2)
	for range 2 {
		go func() { _, err := r.Refusals(ctx); errs <- err }()
	}
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatal(err)
		}
	}
}

// A clock set back leaves the last request's second ahead of now: the wait stays within one second.
func TestClockSetBackWaitsAtMostASecond(t *testing.T) {
	r := &Relay{Signed: func(context.Context, string, string, []byte, int) (int, []byte, time.Duration, error) {
		return 200, []byte("[]"), 0, nil
	}}
	r.gates = map[string]*pathGate{"GET /v1/share/refusals": {done: time.Now().Add(30 * time.Second)}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	if _, err := r.Refusals(ctx); err != nil || time.Since(start) > 1500*time.Millisecond {
		t.Fatalf("%v after %s", err, time.Since(start))
	}
}
