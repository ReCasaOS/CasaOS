package share

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"
)

var noContent = reply{status: 204}

func banAt(key, reason, app string, since time.Time) gatewayBan {
	return gatewayBan{Key: key, Reason: reason, App: app, Since: since}
}

func TestPickReportsFilters(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	recent := now.Add(-time.Hour)
	allow := func(app, key string) bool { return app == "jellyfin" && key == "203.0.113.9/32" }
	bans := []gatewayBan{
		banAt("203.0.113.1", "waf", "jellyfin", recent),
		banAt("203.0.113.2", "country", "jellyfin", recent),
		banAt("203.0.113.3", "blocklist", "jellyfin", recent),
		banAt("203.0.113.4", "scan", "jellyfin", now.Add(-25*time.Hour)),
		banAt("203.0.113.9", "scan", "jellyfin", recent),
		banAt("192.168.1.5", "scan", "jellyfin", recent),
		banAt("100.64.0.7", "scan", "jellyfin", recent),
		banAt("203.0.113.5", "scan", "Bad App", recent),
		banAt("203.0.113.6", "scan", "jellyfin", now.Add(5*time.Minute)),
		banAt("::ffff:203.0.113.7", "rate", "jellyfin", recent),
		banAt("2001:db8:1:2::/64", "code", "jellyfin", recent),
		banAt("fe80::/64", "code", "jellyfin", recent),
		banAt("203.0.113.8", "scan", "jellyfin", recent),
		banAt("255.255.255.255", "scan", "jellyfin", recent),
	}
	sent := map[string]time.Time{"203.0.113.8/32|" + recent.Format(time.RFC3339): now}
	got := pickReports(bans, allow, sent, now)
	var keys []string
	for _, r := range got {
		keys = append(keys, r.Key+" "+r.Reason)
	}
	want := []string{"203.0.113.1/32 waf", "203.0.113.7/32 rate", "2001:db8:1:2::/64 code"}
	if fmt.Sprint(keys) != fmt.Sprint(want) {
		t.Fatalf("got %v want %v", keys, want)
	}
}

// bansBody is n distinct recent bans as the Gateway writes them.
func bansBody(n int, since time.Time) string {
	var bans []map[string]any
	for i := 0; i < n; i++ {
		bans = append(bans, map[string]any{"key": fmt.Sprintf("45.%d.%d.1", i/256, i%256), "reason": "scan", "app": "jellyfin", "since": since})
	}
	b, _ := json.Marshal(bans)
	return string(b)
}

// The relay refuses a whole batch with one item older than its own clock less a day, and the clocks may differ by
// 2 minutes: the box keeps 10 minutes of margin.
func TestPickReportsAgeMargin(t *testing.T) {
	now := time.Date(2026, 11, 13, 12, 0, 0, 0, time.UTC)
	edge := now.Add(-24*time.Hour + 10*time.Minute)
	bans := []gatewayBan{banAt("203.0.113.1", "scan", "jellyfin", edge.Add(time.Second)), banAt("203.0.113.2", "scan", "jellyfin", edge.Add(-time.Second))}
	got := pickReports(bans, func(string, string) bool { return false }, nil, now)
	if len(got) != 1 || got[0].Key != "203.0.113.1/32" {
		t.Fatalf("got %+v", got)
	}
}

// protectionSharing is the relay's answer once it holds the owner's consent.
var protectionSharing = reply{status: 200, body: `{"relay_filter":true,"share_bans":true,"share_bans_at":"2026-11-09T12:00:00Z","community_size":12,"contributed_month":3}`}

// sharing is a fixture whose owner agreed to share bans, with the Gateway holding n bans; posts are what the relay saw after that.
func sharing(t *testing.T, n int, replies ...reply) *fixture {
	t.Helper()
	f := twoShared(t, jellyCode(), append([]reply{protectionSharing}, replies...)...)
	on := true
	if _, err := f.s.SetRelay(context.Background(), nil, &on, true); err != nil {
		t.Fatal(err)
	}
	f.mgmt.mu.Lock()
	f.mgmt.bans = bansBody(n, f.now.Add(-time.Hour))
	f.mgmt.mu.Unlock()
	return f
}

func (f *fixture) posts() []sent {
	var out []sent
	for _, c := range *f.relay {
		if c.path == "/v1/share/bans" {
			out = append(out, c)
		}
	}
	return out
}

func (f *fixture) tickReports(t *testing.T) {
	t.Helper()
	if err := f.s.reportOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestReportsSentOnce(t *testing.T) {
	f := sharing(t, 2, noContent, noContent)
	f.tickReports(t)
	f.tickReports(t)
	if n := len(f.posts()); n != 1 {
		t.Fatalf("%d posts", n)
	}
}

func TestReportsChunkedBy200(t *testing.T) {
	f := sharing(t, 450, noContent, noContent, noContent)
	f.tickReports(t)
	var sizes []int
	for _, p := range f.posts() {
		var b struct{ Bans []BanReport }
		if err := json.Unmarshal([]byte(p.body), &b); err != nil {
			t.Fatal(err)
		}
		sizes = append(sizes, len(b.Bans))
	}
	if fmt.Sprint(sizes) != "[200 200 50]" {
		t.Fatalf("sizes %v", sizes)
	}
}

func TestOffStopsAtOnce(t *testing.T) {
	f := sharing(t, 2, protectionOK, noContent)
	off := false
	if _, err := f.s.SetRelay(context.Background(), nil, &off, false); err != nil {
		t.Fatal(err)
	}
	f.tickReports(t)
	if n := len(f.posts()); n != 0 {
		t.Fatalf("%d posts", n)
	}
}

func TestNoGuardNoReports(t *testing.T) {
	f := sharing(t, 2, noContent)
	f.mgmt.setGuard(404)
	before := len(*f.relay)
	f.tickReports(t)
	if len(*f.relay) != before {
		t.Fatal("the relay was called")
	}
}

func TestRelayErrorRetriesNextTick(t *testing.T) {
	f := sharing(t, 2, reply{status: 500, body: "boom"}, noContent)
	if err := f.s.reportOnce(context.Background()); err == nil {
		t.Fatal("no error")
	}
	f.tickReports(t)
	if p := f.posts(); len(p) != 2 || p[0].body != p[1].body {
		t.Fatalf("posts %+v", p)
	}
}

func TestRelayRejectionDropsTheBatch(t *testing.T) {
	f := sharing(t, 2, reply{status: 400, body: `{"error":"bad request"}`})
	f.tickReports(t)
	f.tickReports(t)
	if n := len(f.posts()); n != 1 {
		t.Fatalf("%d posts", n)
	}
}

// A relay that answered share_bans:false does not hold the consent: it would drop the bans, which must not count as sent.
func TestReportsNeedTheRelaysConsent(t *testing.T) {
	f := twoShared(t, jellyCode(), protectionOK, protectionSharing, noContent)
	on := true
	if _, err := f.s.SetRelay(context.Background(), nil, &on, true); err != nil {
		t.Fatal(err)
	}
	f.mgmt.mu.Lock()
	f.mgmt.bans = bansBody(2, f.now.Add(-time.Hour))
	f.mgmt.mu.Unlock()
	f.tickReports(t)
	if n := len(f.posts()); n != 0 {
		t.Fatalf("%d posts", n)
	}
	if err := f.s.resync(context.Background()); err != nil { // the relay now holds it
		t.Fatal(err)
	}
	f.tickReports(t)
	if n := len(f.posts()); n != 1 {
		t.Fatalf("%d posts", n)
	}
}

// A busy relay's Retry-After holds the reports until then, not until the next minute.
func TestBusyRelayHoldsReportsUntilRetryAfter(t *testing.T) {
	f := sharing(t, 2, reply{status: 429, retry: 10 * time.Minute}, noContent)
	if err := f.s.reportOnce(context.Background()); err == nil {
		t.Fatal("no error")
	}
	f.advance(9 * time.Minute)
	f.tickReports(t)
	if n := len(f.posts()); n != 1 {
		t.Fatalf("%d posts before the Retry-After", n)
	}
	f.advance(2 * time.Minute)
	f.tickReports(t)
	if n := len(f.posts()); n != 2 {
		t.Fatalf("%d posts after the Retry-After", n)
	}
}

// The owner's allow list of an app keeps its addresses off the relay, an allowed address inside a banned /64 too.
func TestReportsSkipTheAppsAllowList(t *testing.T) {
	g := jellyCode()
	g.Apps["jellyfin"] = GuardApp{Access: "code", Override: &PolicyOverride{Allow: []string{"45.0.0.0/24", "2001:db8:1:2::5"}}}
	f := twoShared(t, g, protectionSharing, noContent)
	on := true
	if _, err := f.s.SetRelay(context.Background(), nil, &on, true); err != nil {
		t.Fatal(err)
	}
	since := f.now.Add(-time.Hour)
	b, _ := json.Marshal([]gatewayBan{banAt("45.0.0.1", "scan", "jellyfin", since), banAt("45.0.1.1", "scan", "jellyfin", since),
		banAt("2001:db8:1:2::/64", "scan", "jellyfin", since), banAt("45.0.0.2", "scan", "nextcloud", since)})
	f.mgmt.mu.Lock()
	f.mgmt.bans = string(b)
	f.mgmt.mu.Unlock()
	f.tickReports(t)
	p := f.posts()
	if len(p) != 1 {
		t.Fatalf("%d posts", len(p))
	}
	var got struct{ Bans []BanReport }
	if err := json.Unmarshal([]byte(p[0].body), &got); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range got.Bans {
		keys = append(keys, r.App+" "+r.Key)
	}
	if fmt.Sprint(keys) != "[jellyfin 45.0.1.1/32 nextcloud 45.0.0.2/32]" {
		t.Fatalf("sent %v", keys)
	}
}

// Leaving the relay ends the ban sharing it was agreed for, with or without a name: another relay must be agreed to again.
func TestResetEndsBanSharing(t *testing.T) {
	for _, named := range []bool{true, false} {
		f := sharing(t, 2, noContent)
		if !named {
			if err := saveState(f.state, State{}); err != nil {
				t.Fatal(err)
			}
		}
		f.s.Reset(context.Background())
		if saved, _ := loadGuard(f.s.guardPath()); saved.ShareBans || !saved.ShareBansAt.IsZero() {
			t.Fatalf("named %v: saved %+v", named, saved)
		}
		view, err := f.s.GuardView(context.Background())
		if r := view.Relay; err != nil || r.Supported || r.ShareBans || r.CommunitySize != 0 {
			t.Fatalf("named %v: %+v %v", named, r, err)
		}
		before := len(*f.relay)
		f.tickReports(t)
		if len(*f.relay) != before {
			t.Fatalf("named %v: the relay was called", named)
		}
	}
}

// A relay joined after leaving another gets the box's current bans at once: what the old one took or asked to wait for
// does not carry over.
func TestResetStartsReportsAfresh(t *testing.T) {
	f := sharing(t, 2, noContent, protectionSharing, noContent)
	f.tickReports(t)
	f.s.reportAfter = f.now.Add(time.Hour) // the old relay was busy
	if err := saveState(f.state, State{}); err != nil {
		t.Fatal(err)
	}
	f.s.Reset(context.Background())
	on := true
	if _, err := f.s.SetRelay(context.Background(), nil, &on, true); err != nil {
		t.Fatal(err)
	}
	f.tickReports(t)
	if p := f.posts(); len(p) != 2 || p[0].body != p[1].body {
		t.Fatalf("posts %+v", p)
	}
}
