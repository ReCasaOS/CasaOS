package hoststats

import (
	"context"
	"errors"
	"maps"
	"math"
	"strconv"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// interval is the sampling period.
const interval = 5 * time.Second

// Variables only so tests can shorten them.
var (
	publishTimeout = 5 * time.Second // one Publish call
	drainTimeout   = 5 * time.Second // what Close waits for the queued events
)

// queueSize bounds the events waiting for the bus. Full means the bus has been
// stuck for longer than any burst of alerts: the newest event is dropped.
const queueSize = 64

// logError is replaced by tests: the shared logger is not initialised there.
var logError = func(msg string, err error) { logger.Error(msg, zap.Error(err)) }

// Source reads the machine; Reading.At is set by the source from its clock.
type Source interface {
	Read(ctx context.Context) (Reading, error)
}

type Config struct {
	Dir     string // /var/lib/casaos/stats/core
	Source  Source
	Clock   stats.Clock
	Publish func(name string, properties map[string]string) // the bus; errors logged by the caller
	Hub     func(name string, properties map[string]string) // alerts.Default.Handle
	Name    func() string                                   // the machine's display name, "" -> "Machine"
	Boot    func() time.Time                                // boot time, zero = unknown
}

// Service samples the host every 5 s into Store; Eval judges its minutes.
type Service struct {
	Store *stats.Store
	Eval  *stats.Evaluator

	cfg Config
	pub *publisher

	tick   sync.Mutex // Tick never runs twice at once; Close waits for it
	closed bool       // under tick
	col    Collector  // under tick
}

// Default is the running service, nil until it started.
var Default *Service

// New opens the store and the evaluator in cfg.Dir, wires the evaluator to the
// store's minutes and rebuilds the host's alert counts from its history.
func New(cfg Config) (*Service, error) {
	st, err := stats.Open(cfg.Dir, cfg.Clock)
	if err != nil {
		return nil, err
	}
	s := &Service{Store: st, cfg: cfg, pub: startPublisher(cfg.Publish)}
	ev, err := stats.NewEvaluator(st, cfg.Dir, s.emit)
	if err != nil {
		_ = st.Close()
		s.pub.stop()
		return nil, err
	}
	s.Eval = ev
	st.SetOnMinute(ev.OnMinute)
	ev.Rebuild(Item)
	return s, nil
}

// Name is the machine's display name, "Machine" when unknown.
func (s *Service) Name() string {
	if s.cfg.Name != nil {
		if n := s.cfg.Name(); n != "" {
			return n
		}
	}
	return "Machine"
}

// BootTime is the machine's boot time, zero when unknown.
func (s *Service) BootTime() time.Time {
	if s.cfg.Boot == nil {
		return time.Time{}
	}
	return s.cfg.Boot()
}

// Run ticks now, then every 5 s, until ctx ends. A failed tick is logged once
// per outage; a slow tick delays the next one, ticks never overlap.
func (s *Service) Run(ctx context.Context) {
	t := time.NewTicker(interval)
	defer t.Stop()
	failing := false
	for {
		err := s.Tick(ctx)
		if err != nil && !failing && ctx.Err() == nil {
			logError("host statistics: a sample failed", err)
		}
		failing = err != nil
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

// Tick takes one sample and records it. A failed Read is a gap: nothing is
// recorded and nothing resolves. The Store's hook judges each minute written.
func (s *Service) Tick(ctx context.Context) error {
	s.tick.Lock()
	defer s.tick.Unlock()
	if s.closed {
		return stats.ErrClosed
	}
	r, err := s.cfg.Source.Read(ctx)
	if err != nil {
		return err
	}
	smp := s.col.Sample(r)
	// Only a start has no CPU rate (the Collector keeps its previous reading
	// across failed Reads). Alone in its minute (the next tick falls in the next
	// minute) it would write a minute without alert/cpu_high, which ends an
	// active cpu_high; so it is not recorded (a minute without any Record keeps
	// the alert). Cost: one 5 s point. alert/mem_high needs no previous reading,
	// so it has no such case. A source with Cores == 0 never has a CPU rate, so
	// it is always recorded.
	if _, ok := smp["alert/cpu_high"]; !ok && r.Cores > 0 && r.At.Add(interval).Unix()/60 != r.At.Unix()/60 {
		return nil
	}
	return s.Store.Record(Item, r.At, smp)
}

// Close waits for a tick in flight, closes the store, then publishes what is
// queued (at most about 5 s); later calls return nil.
func (s *Service) Close() error {
	s.tick.Lock()
	defer s.tick.Unlock()
	if s.closed {
		return nil
	}
	s.closed = true
	err := s.Store.Close()
	s.pub.stop()
	return err
}

// emit is the Evaluator's callback, called outside its lock. It takes no lock
// of the Service: a tick holds s.tick while the Store calls into the Evaluator.
func (s *Service) emit(ev stats.Event) {
	minutes := 0
	for _, r := range s.Eval.Rules(ev.Item) {
		if r.Key == ev.Key {
			minutes = r.Minutes
		}
	}
	name, p := eventProperties(ev, minutes, s.Name())
	if s.cfg.Hub != nil {
		s.cfg.Hub(name, maps.Clone(p))
	}
	if !s.pub.submit(name, p) {
		logError("a host resource alert was not published, the bus queue is full or stopped", errors.New(name+" "+ev.Key))
	}
}

// eventProperties is the bus event of an alert transition; minutes is the rule's window.
func eventProperties(ev stats.Event, minutes int, name string) (eventName string, p map[string]string) {
	eventName = "resource:alert-resolved"
	if ev.State == "firing" {
		eventName = "resource:alert-firing"
	}
	p = map[string]string{
		"item":      ev.Item,
		"name":      name,
		"kind":      "host",
		"situation": ev.Key,
		"since":     ev.Since.UTC().Format(time.RFC3339),
		"source":    "core",
		"minutes":   strconv.Itoa(minutes),
	}
	// the contract: value only on a firing
	if ev.State == "firing" && !math.IsNaN(ev.Value) && !math.IsInf(ev.Value, 0) {
		p["value"] = strconv.FormatFloat(math.Round(ev.Value), 'f', 0, 64)
	}
	return eventName, p
}

type job struct {
	name string
	p    map[string]string
}

// publisher is one goroutine fed by a bounded queue, so events reach the bus in
// the order they were emitted and a tick never waits on the bus.
type publisher struct {
	publish func(string, map[string]string)
	mu      sync.Mutex
	closed  bool
	jobs    chan job
	done    chan struct{}
}

func startPublisher(publish func(string, map[string]string)) *publisher {
	p := &publisher{publish: publish, jobs: make(chan job, queueSize), done: make(chan struct{})}
	go func() {
		defer close(p.done)
		for j := range p.jobs {
			p.call(j)
		}
	}()
	return p
}

// call waits for one Publish at most publishTimeout.
// ponytail: Publish takes no context, so a call past its timeout is abandoned,
// not cancelled; if it ends later it can land after the next event. Pass a
// context to Publish if the bus ever hangs for that long.
func (p *publisher) call(j job) {
	if p.publish == nil {
		return
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		p.publish(j.name, j.p)
	}()
	t := time.NewTimer(publishTimeout)
	defer t.Stop()
	select {
	case <-done:
	case <-t.C:
		logError("publishing a host resource alert timed out", errors.New(j.name))
	}
}

// submit never blocks; false when the event was dropped.
func (p *publisher) submit(name string, props map[string]string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	select {
	case p.jobs <- job{name, props}:
		return true
	default:
		return false
	}
}

// stop publishes what is queued, waiting at most drainTimeout; calling it again does nothing.
func (p *publisher) stop() {
	p.mu.Lock()
	if !p.closed {
		p.closed = true
		close(p.jobs)
	}
	p.mu.Unlock()
	t := time.NewTimer(drainTimeout)
	defer t.Stop()
	select {
	case <-p.done:
	case <-t.C:
		logError("host resource alerts still queued at shutdown were not published", errors.New("timeout"))
	}
}
