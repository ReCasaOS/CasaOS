package fleet

import (
	"context"
	"time"
)

// A box with no tunnel for offlineAfter gets one alert; nothing is said during
// startGrace after the master starts, while its boxes come back on their own.
var (
	offlineAfter = 10 * time.Minute
	startGrace   = 10 * time.Minute
	watchEvery   = time.Minute
)

// watchOffline runs checkOffline every minute until ctx ends.
func (f *Fleet) watchOffline(ctx context.Context) {
	tick := time.NewTicker(watchEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			f.checkOffline()
		}
	}
}

// checkOffline raises the alert for each box seen before and gone for
// offlineAfter, and resolves it when the box is back or removed. A box that
// never connected is not gone: its code may still be on its way.
func (f *Fleet) checkOffline() {
	now := f.Now()
	type event struct {
		id, name string
		offline  bool
	}
	var events []event
	f.mu.Lock()
	if f.started.IsZero() {
		f.started = now
	}
	st, on := f.master()
	known := map[string]bool{}
	for _, b := range st.Boxes {
		if !on {
			break
		}
		known[b.ID] = true
		if f.tunnels[b.ID] != nil {
			if f.offline[b.ID] {
				delete(f.offline, b.ID)
				events = append(events, event{b.ID, b.Name, false})
			}
			continue
		}
		gone := !b.LastSeen.IsZero() && now.Sub(b.LastSeen) >= offlineAfter && now.Sub(f.started) >= startGrace
		if gone && !f.offline[b.ID] {
			f.offline[b.ID] = true
			events = append(events, event{b.ID, b.Name, true})
		}
	}
	// a box removed (or the master disabled) while an alert was up: stop
	// tracking it, but send no "reached the master again" — it did not, the
	// owner took it out, and the message would carry no box name.
	for id := range f.offline {
		if !known[id] {
			delete(f.offline, id)
		}
	}
	f.mu.Unlock()
	for _, e := range events {
		f.Offline(e.id, e.name, e.offline)
	}
}
