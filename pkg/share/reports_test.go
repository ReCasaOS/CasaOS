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

// sharing is a fixture whose owner agreed to share bans, with the Gateway holding n bans; posts are what the relay saw after that.
func sharing(t *testing.T, n int, replies ...reply) *fixture {
	t.Helper()
	f := twoShared(t, jellyCode(), append([]reply{protectionOK}, replies...)...)
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
