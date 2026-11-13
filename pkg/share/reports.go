package share

import (
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"regexp"
	"time"
)

const (
	reportEvery = time.Minute
	reportChunk = 200 // the relay takes at most 200 bans a post
	reportAge   = 24 * time.Hour
	reportSkew  = 2 * time.Minute
	// reportKeep is the oldest ban sent: the relay refuses a batch with one older than its clock less reportAge, and
	// the two clocks may differ by reportSkew
	reportKeep = reportAge - 10*time.Minute
)

// gatewayBan is the part of the Gateway's ban the relay needs.
type gatewayBan struct {
	Key    string    `json:"key"`
	App    string    `json:"app"`
	Reason string    `json:"reason"`
	Since  time.Time `json:"since"`
}

var (
	reportApp = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	cgnat     = netip.MustParsePrefix("100.64.0.0/10")
)

// addressKey is the relay's key for a Gateway key: IPv4 as /32, IPv6 as its /64; ok is false for an address the relay never keeps.
func addressKey(key string) (netip.Prefix, bool) {
	a, err := netip.ParseAddr(key)
	if err != nil {
		p, perr := netip.ParsePrefix(key)
		if perr != nil {
			return netip.Prefix{}, false
		}
		a = p.Addr()
	}
	a = a.Unmap().WithZone("")
	if !a.IsGlobalUnicast() || a.IsPrivate() || cgnat.Contains(a) { // the relay's own rule
		return netip.Prefix{}, false
	}
	if a.Is4() {
		return netip.PrefixFrom(a, 32), true
	}
	return netip.PrefixFrom(a, 64).Masked(), true
}

func sentKey(r BanReport) string { return r.Key + "|" + r.Since.UTC().Format(time.RFC3339) }

// pickReports is what is worth sending: the relay refuses a whole batch for one bad item, so an item it would
// refuse is dropped here, as are the ones the owner allowed (allow gets the app and the address key) and the ones already sent.
func pickReports(bans []gatewayBan, allow func(app, key string) bool, sent map[string]time.Time, now time.Time) []BanReport {
	var out []BanReport
	seen := map[string]bool{}
	for _, b := range bans {
		p, ok := addressKey(b.Key)
		switch b.Reason {
		case "waf", "scan", "rate", "code":
		default:
			continue
		}
		if !ok || !reportApp.MatchString(b.App) || now.Sub(b.Since) > reportKeep || b.Since.Sub(now) > reportSkew || allow(b.App, p.String()) {
			continue
		}
		r := BanReport{Key: p.String(), Reason: b.Reason, App: b.App, Since: b.Since.UTC().Truncate(time.Second)}
		k := sentKey(r)
		if _, done := sent[k]; done || seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, r)
	}
	return out
}

// reportOnce sends the new bans of the Gateway to the relay, when the owner agreed, the relay holds that consent and
// it is not busy. A batch the relay refuses is not sent again; any other failure leaves it for the next call, or for
// the end of a busy relay's Retry-After.
func (s *Service) reportOnce(ctx context.Context) error {
	s.mu.Lock()
	if s.reportFresh { // the box left its relay: a new one gets the current bans at once
		s.reported, s.reportAfter, s.reportFresh = nil, time.Time{}, false
	}
	if s.now().Before(s.reportAfter) {
		s.mu.Unlock()
		return nil
	}
	g, err := s.guardLocked()
	if err != nil || !g.ShareBans || s.relayOK == nil || !*s.relayOK || !s.relayInfo.ShareBans { // without it the relay drops them
		s.mu.Unlock()
		return nil
	}
	box, apps := g.Box, map[string]GuardApp{}
	for k, v := range g.Apps {
		apps[k] = v
	}
	s.mu.Unlock()

	raw, err := s.Gateway.GuardBans(ctx)
	if errors.Is(err, ErrGatewayNoGuard) {
		return nil
	}
	if err != nil {
		return err
	}
	var bans []gatewayBan
	if err := json.Unmarshal(raw, &bans); err != nil {
		return err
	}
	now := s.now()
	for k, at := range s.reported {
		if now.Sub(at) > reportAge {
			delete(s.reported, k)
		}
	}
	if s.reported == nil {
		s.reported = map[string]time.Time{}
	}
	allowed := map[string][]netip.Prefix{}
	allow := func(app, key string) bool {
		list, ok := allowed[app]
		if !ok {
			var o *PolicyOverride
			if a, has := apps[app]; has {
				o = a.Override
			}
			for _, e := range Effective(box, o).Allow {
				if p, err := netip.ParsePrefix(e); err == nil {
					list = append(list, p)
				} else if a, err := netip.ParseAddr(e); err == nil {
					list = append(list, netip.PrefixFrom(a, a.BitLen()))
				}
			}
			allowed[app] = list
		}
		k := netip.MustParsePrefix(key)
		for _, p := range list {
			if p.Overlaps(k) { // an allowed address inside a banned /64 counts too
				return true
			}
		}
		return false
	}
	todo := pickReports(bans, allow, s.reported, now)
	for len(todo) > 0 {
		n := min(len(todo), reportChunk)
		batch := todo[:n]
		todo = todo[n:]
		err := s.Relay.ReportBans(ctx, batch)
		var busy BusyError
		switch {
		case errors.As(err, &busy):
			s.reportAfter = now.Add(busy.RetryAfter)
			return err
		case errors.Is(err, ErrRelayNoShare):
			return nil
		case errors.Is(err, ErrRelayRejected):
			s.log("share: the relay refused a batch of bans; it is dropped", nil)
		case err != nil:
			return err
		}
		for _, r := range batch {
			s.reported[sentKey(r)] = now
		}
	}
	return nil
}

// reportLoop sends the bans every minute and when a sharing setting changes.
func (s *Service) reportLoop(ctx context.Context) {
	for {
		if err := s.reportOnce(ctx); err != nil && ctx.Err() == nil {
			s.log("share: the bans could not be sent, will try again", err)
		}
		if !sleep(ctx, reportEvery, s.reportCh) {
			return
		}
	}
}
