package hoststats

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
)

func init() {
	logError = func(string, error) {}
	logPartial = func(string, error) {}
}

// fakeSource scripts what the machine shows: each good Read adds 100 to the
// total CPU time and cpu to the busy time, so alert/cpu_high is cpu.
type fakeSource struct {
	clock *stats.FakeClock

	mu      sync.Mutex
	cpu     float64 // percent busy over the last interval
	mem     uint64  // percent of RAM used
	fail    bool
	busy    float64
	total   float64
	block   chan struct{} // non-nil: Read waits for it to close
	entered chan struct{} // non-nil: signalled when Read starts
}

func (f *fakeSource) set(cpu float64, mem uint64) {
	f.mu.Lock()
	f.cpu, f.mem = cpu, mem
	f.mu.Unlock()
}

func (f *fakeSource) setFail(b bool) {
	f.mu.Lock()
	f.fail = b
	f.mu.Unlock()
}

func (f *fakeSource) Read(ctx context.Context) (Reading, error) {
	f.mu.Lock()
	block, entered := f.block, f.entered
	f.mu.Unlock()
	if entered != nil {
		entered <- struct{}{}
	}
	if block != nil {
		<-block
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.fail {
		return Reading{}, errors.New("no /proc")
	}
	f.busy += f.cpu
	f.total += 100
	return Reading{
		At:       f.clock.Now(),
		CPU:      CPUTimes{Busy: f.busy, Total: f.total},
		Cores:    4,
		MemTotal: 1000,
		MemUsed:  f.mem * 10,
	}, nil
}

type event struct {
	name string
	p    map[string]string
}

type rig struct {
	t     *testing.T
	clock *stats.FakeClock
	src   *fakeSource
	dir   string
	svc   *Service

	mu       sync.Mutex
	hub, pub []event
	publish  func(name string, p map[string]string) // replaces the capture when set
}

func newRig(t *testing.T) *rig {
	clock := stats.NewFakeClock(t0)
	r := &rig{t: t, clock: clock, src: &fakeSource{clock: clock, cpu: 10, mem: 50}, dir: t.TempDir()}
	r.open()
	t.Cleanup(func() { _ = r.svc.Close() })
	return r
}

func (r *rig) open() {
	r.t.Helper()
	svc, err := New(Config{
		Dir:    r.dir,
		Source: r.src,
		Clock:  r.clock,
		Publish: func(name string, p map[string]string) {
			r.mu.Lock()
			pub := r.publish
			r.mu.Unlock()
			if pub != nil {
				pub(name, p)
				return
			}
			r.mu.Lock()
			r.pub = append(r.pub, event{name, p})
			r.mu.Unlock()
		},
		Hub: func(name string, p map[string]string) {
			r.mu.Lock()
			r.hub = append(r.hub, event{name, p})
			r.mu.Unlock()
		},
		Name: func() string { return "nas" },
	})
	if err != nil {
		r.t.Fatal(err)
	}
	r.svc = svc
}

// restart closes the service and opens a new one on the same directory.
func (r *rig) restart() {
	r.t.Helper()
	if err := r.svc.Close(); err != nil {
		r.t.Fatal(err)
	}
	r.open()
}

// tick samples at the clock's time, then moves the clock 5 s on.
func (r *rig) tick() error {
	err := r.svc.Tick(context.Background())
	r.clock.Advance(interval)
	return err
}

// ticks runs n ticks; a failure fails the test unless the source is scripted to fail.
func (r *rig) ticks(n int) {
	r.t.Helper()
	for range n {
		if err := r.tick(); err != nil && !r.src.fail {
			r.t.Fatal(err)
		}
	}
}

func (r *rig) minutes(n int) { r.t.Helper(); r.ticks(12 * n) }

// names returns the names of the hub (or, once closed, the published) events.
func names(evs []event) []string {
	out := []string{}
	for _, e := range evs {
		out = append(out, e.name+":"+e.p["situation"])
	}
	return out
}

func (r *rig) hubNames() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return names(r.hub)
}

// pubNames closes the service first: what is queued is published by then.
func (r *rig) pubNames() []string {
	r.t.Helper()
	if err := r.svc.Close(); err != nil {
		r.t.Fatal(err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return names(r.pub)
}

func (r *rig) want(got []string, want ...string) {
	r.t.Helper()
	if want == nil {
		want = []string{}
	}
	if !reflect.DeepEqual(got, want) {
		r.t.Fatalf("events %v, want %v", got, want)
	}
}

const (
	firingCPU   = "resource:alert-firing:cpu_high"
	resolvedCPU = "resource:alert-resolved:cpu_high"
)

func TestATickRecordsEveryFiveSeconds(t *testing.T) {
	r := newRig(t)
	r.ticks(12)
	res, err := r.svc.Store.Query(Item, stats.RangeLive)
	if err != nil {
		t.Fatal(err)
	}
	var ts []int64
	for _, s := range res.Series {
		if s.Chart+"/"+s.Name != "memory/rss" {
			continue
		}
		for _, p := range s.Points {
			if p.Avg != nil {
				ts = append(ts, p.T)
			}
		}
	}
	if len(ts) != 12 {
		t.Fatalf("got %d points, want 12: %v", len(ts), ts)
	}
	for i := 1; i < len(ts); i++ {
		if ts[i]-ts[i-1] != 5 {
			t.Fatalf("points %v are not 5 s apart", ts)
		}
	}
}

func TestFiveMinutesBelowTheLineNeverFire(t *testing.T) {
	r := newRig(t)
	r.src.set(85, 85)
	r.minutes(30)
	r.want(r.hubNames())
	r.want(r.pubNames())
}

func TestTenHighMinutesFireExactlyOnce(t *testing.T) {
	r := newRig(t)
	r.src.set(95, 50)
	r.minutes(9)
	r.ticks(12) // minute 9 committed at its first tick, the 10th still open
	r.want(r.hubNames())
	r.ticks(1) // the first tick of minute 11 completes the 10th
	r.want(r.hubNames(), firingCPU)
	r.mu.Lock()
	got := r.hub[0].p
	r.mu.Unlock()
	for k, v := range map[string]string{"situation": "cpu_high", "value": "95", "minutes": "10", "source": "core", "kind": "host", "item": "host"} {
		if got[k] != v {
			t.Errorf("%s = %q, want %q", k, got[k], v)
		}
	}
	r.minutes(3)
	r.want(r.hubNames(), firingCPU)
	r.src.set(10, 50)
	r.ticks(11) // the rest of the minute that was open
	r.minutes(1)
	r.ticks(1)
	r.want(r.hubNames(), firingCPU, resolvedCPU)
	r.want(r.pubNames(), firingCPU, resolvedCPU)
}

func TestMemoryNeedsFiveMinutes(t *testing.T) {
	r := newRig(t)
	r.src.set(10, 92)
	r.minutes(4)
	r.ticks(1) // minute 4 complete
	r.want(r.hubNames())
	r.ticks(11)
	r.ticks(1) // minute 5 complete
	r.want(r.hubNames(), "resource:alert-firing:mem_high")
	r.mu.Lock()
	got := r.hub[0].p
	r.mu.Unlock()
	if got["value"] != "92" || got["minutes"] != "5" {
		t.Fatalf("got %v", got)
	}
}

func TestARestartInTheMiddleOfTheCountNeitherForgetsNorFiresTwice(t *testing.T) {
	r := newRig(t)
	r.src.set(95, 50)
	r.minutes(9)
	r.ticks(1) // minute 9 committed
	// The minute in progress at Close counts as its minute, so the alert may fire
	// at the restart; either way it fires exactly once across both lifetimes.
	r.restart()
	r.ticks(11)
	r.ticks(1) // minute 10 complete
	r.want(r.hubNames(), firingCPU)

	r.restart() // the alert is already firing
	r.minutes(3)
	r.ticks(1)
	r.want(r.hubNames(), firingCPU)
	if a := r.svc.Eval.Active(Item); len(a) != 1 || a[0].Key != "cpu_high" {
		t.Fatalf("active %v", a)
	}
	r.want(r.pubNames(), firingCPU)
}

// The first tick after a start has no CPU rate; alone in its minute it would
// write a minute without alert/cpu_high, which ends the alert.
func TestAFiringAlertSurvivesARestartWhoseFirstTickIsLate(t *testing.T) {
	r := newRig(t)
	r.src.set(95, 50)
	r.minutes(10)
	r.ticks(1)
	r.want(r.hubNames(), firingCPU)
	if err := r.svc.Close(); err != nil {
		t.Fatal(err)
	}
	r.clock.Set(t0.Add(10*time.Minute + 57*time.Second)) // 12:10:57
	r.open()
	r.minutes(3)
	r.want(r.hubNames(), firingCPU)
	if a := r.svc.Eval.Active(Item); len(a) != 1 {
		t.Fatalf("active %v", a)
	}
}

func TestAFailingReadRecordsNothingAndTheNextTickWorks(t *testing.T) {
	r := newRig(t)
	r.ticks(2)
	r.src.setFail(true)
	if err := r.tick(); err == nil {
		t.Fatal("a failed Read must fail the tick")
	}
	r.src.setFail(false)
	r.ticks(1)
	res, err := r.svc.Store.Query(Item, stats.RangeLive)
	if err != nil {
		t.Fatal(err)
	}
	at := map[int64]*float64{}
	for _, s := range res.Series {
		if s.Chart+"/"+s.Name == "memory/rss" {
			for _, p := range s.Points {
				at[p.T] = p.Avg
			}
		}
	}
	base := t0.Unix()
	for i, wantPoint := range []bool{true, true, false, true} {
		v := at[base+int64(5*i)]
		if (v != nil) != wantPoint {
			t.Errorf("tick %d: point %v, want present=%v", i+1, v, wantPoint)
		}
	}
}

func TestAFailingReadDoesNotResolveAFiringAlert(t *testing.T) {
	r := newRig(t)
	r.src.set(95, 50)
	r.minutes(10)
	r.ticks(1)
	r.want(r.hubNames(), firingCPU)
	before := r.svc.Eval.Active(Item)

	r.src.setFail(true)
	r.ticks(11 + 36) // the rest of the open minute, then 3 minutes
	r.src.setFail(false)
	r.want(r.hubNames(), firingCPU)
	if after := r.svc.Eval.Active(Item); !reflect.DeepEqual(after, before) {
		t.Fatalf("active %v, was %v", after, before)
	}

	r.minutes(5)
	r.want(r.hubNames(), firingCPU)
	r.src.set(10, 50)
	r.minutes(1)
	r.ticks(1)
	r.want(r.hubNames(), firingCPU, resolvedCPU)
	r.want(r.pubNames(), firingCPU, resolvedCPU)
}

func TestTheStoreLeavesTheEvaluatorsFilesAlone(t *testing.T) {
	r := newRig(t)
	if err := r.svc.Close(); err != nil {
		t.Fatal(err)
	}
	// alerts.json as the evaluator writes it idle, so that it has nothing to save
	// (a save goes through alerts.json.tmp)
	files := map[string]string{
		"alerts.json":     `{"version":1,"items":{}}`,
		"alerts.json.tmp": "half written",
		"alerts.json.bad": "moved aside",
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(r.dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	r.open()
	r.restart()
	ringTmp := filepath.Join(r.dir, "x.ring.tmp") // proves the sweep ran
	if err := os.WriteFile(ringTmp, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Store.Sweep(r.clock.Now().Add(30 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(ringTmp); !os.IsNotExist(err) {
		t.Fatalf("the sweep did not run: %v", err)
	}
	for name, body := range files {
		b, err := os.ReadFile(filepath.Join(r.dir, name))
		if err != nil || string(b) != body {
			t.Errorf("%s: %q, %v", name, b, err)
		}
	}
}

func TestAFailingPublishDoesNotStopTheHub(t *testing.T) {
	r := newRig(t)
	release := make(chan struct{})
	defer close(release)
	r.mu.Lock()
	r.publish = func(string, map[string]string) {
		select {
		case <-release:
		case <-time.After(10 * time.Second):
		}
	}
	r.mu.Unlock()
	r.src.set(95, 50)
	r.minutes(10)
	start := time.Now()
	if err := r.tick(); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d >= time.Second {
		t.Fatalf("the tick took %v", d)
	}
	r.want(r.hubNames(), firingCPU)
}

// One publisher goroutine, fed in emit order: a slow firing is not overtaken by its resolved.
func TestEventsReachPublishInTheOrderTheyWereEmitted(t *testing.T) {
	r := newRig(t)
	if err := r.svc.Eval.SetRules(Item, []stats.Rule{{Key: "cpu_high", Enabled: true, Threshold: 90, Minutes: 1}}); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	r.mu.Lock()
	r.publish = func(name string, p map[string]string) {
		if strings.HasSuffix(name, "firing") {
			<-release
		}
		r.mu.Lock()
		r.pub = append(r.pub, event{name, p})
		r.mu.Unlock()
	}
	r.mu.Unlock()
	r.src.set(95, 50)
	r.minutes(1)
	r.ticks(1)
	r.src.set(10, 50)
	r.ticks(11)
	r.ticks(1)
	r.want(r.hubNames(), firingCPU, resolvedCPU)
	close(release)
	r.want(r.pubNames(), firingCPU, resolvedCPU)
}

func TestEventPropertiesAreExactlyTheContracts(t *testing.T) {
	since := time.Date(2026, 1, 1, 13, 0, 0, 0, time.FixedZone("CET", 3600))
	name, p := eventProperties(stats.Event{Item: "host", Key: "cpu_high", State: "firing", Value: 94.6, Since: since}, 10, "nas")
	want := map[string]string{
		"item": "host", "name": "nas", "kind": "host", "situation": "cpu_high", "value": "95",
		"since": "2026-01-01T12:00:00Z", "source": "core", "minutes": "10",
	}
	if name != "resource:alert-firing" || !maps.Equal(p, want) {
		t.Fatalf("firing: %s %v", name, p)
	}
	name, p = eventProperties(stats.Event{Item: "host", Key: "mem_high", State: "resolved", Value: 40, Since: since}, 5, "nas")
	want = map[string]string{
		"item": "host", "name": "nas", "kind": "host", "situation": "mem_high",
		"since": "2026-01-01T12:00:00Z", "source": "core", "minutes": "5",
	}
	if name != "resource:alert-resolved" || !maps.Equal(p, want) {
		t.Fatalf("resolved: %s %v", name, p)
	}
}

func TestNoSharedCPUPercentCall(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(b), "cpu.Percent(") {
			t.Errorf("%s calls cpu.Percent: it would share gopsutil's window with the widget", f)
		}
	}
}

func TestTheNameFallsBackToMachine(t *testing.T) {
	for _, name := range []func() string{nil, func() string { return "" }} {
		if got := (&Service{cfg: Config{Name: name}}).Name(); got != "Machine" {
			t.Errorf("got %q", got)
		}
	}
	if got := (&Service{cfg: Config{}}).BootTime(); !got.IsZero() {
		t.Errorf("boot %v", got)
	}
	boot := t0.Add(-time.Hour)
	s := &Service{cfg: Config{Name: func() string { return "nas" }, Boot: func() time.Time { return boot }}}
	if s.Name() != "nas" || !s.BootTime().Equal(boot) {
		t.Errorf("got %q %v", s.Name(), s.BootTime())
	}
}

func TestCloseWaitsForATickAndIsIdempotent(t *testing.T) {
	r := newRig(t)
	block, entered := make(chan struct{}), make(chan struct{}, 1)
	r.src.mu.Lock()
	r.src.block, r.src.entered = block, entered
	r.src.mu.Unlock()
	ticked := make(chan error, 1)
	go func() { ticked <- r.svc.Tick(context.Background()) }()
	<-entered
	closed := make(chan error, 1)
	go func() { closed <- r.svc.Close() }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned during a tick: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(block)
	if err := <-ticked; err != nil {
		t.Fatal(err)
	}
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
	if err := r.svc.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if err := r.svc.Tick(context.Background()); !errors.Is(err, stats.ErrClosed) {
		t.Fatalf("Tick after Close: %v", err)
	}
}

func TestRunTicksAtOnceAndStopsWithItsContext(t *testing.T) {
	r := newRig(t)
	entered := make(chan struct{}, 1)
	r.src.mu.Lock()
	r.src.entered = entered
	r.src.mu.Unlock()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { r.svc.Run(ctx); close(done) }()
	select {
	case <-entered:
	case <-time.After(time.Second): // the ticker's first tick would be 5 s away
		t.Fatal("Run did not tick at once")
	}
	cancel()
	<-done
}

// zeroCores is a source that never knows its core count, so never has a CPU rate.
type zeroCores struct{ *fakeSource }

func (z zeroCores) Read(ctx context.Context) (Reading, error) {
	r, err := z.fakeSource.Read(ctx)
	r.Cores = 0
	return r, err
}

func TestASourceWithoutCoresLosesNoPoint(t *testing.T) {
	r := newRig(t)
	r.src.set(10, 50)
	if err := r.svc.Close(); err != nil {
		t.Fatal(err)
	}
	r.src.clock = r.clock
	src := zeroCores{r.src}
	svc, err := New(Config{Dir: t.TempDir(), Source: src, Clock: r.clock})
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Close()
	for range 12 {
		if err := svc.Tick(context.Background()); err != nil {
			t.Fatal(err)
		}
		r.clock.Advance(interval)
	}
	res, err := svc.Store.Query(Item, stats.RangeLive)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, s := range res.Series {
		if s.Chart+"/"+s.Name == "memory/rss" {
			for _, p := range s.Points {
				if p.Avg != nil {
					n++
				}
			}
		}
	}
	if n != 12 {
		t.Fatalf("got %d points, want 12", n)
	}
}

// manySeries renames half of its interfaces at every read: the Collector keeps at most
// 8 of them, but the Store never frees a series, so the 48 are reached after some reads.
type manySeries struct{ n *int }

func (m manySeries) Read(context.Context) (Reading, error) {
	*m.n++
	nets := map[string]NetCounters{}
	for i := range 8 {
		nets[fmt.Sprintf("eth%d", *m.n*4+i)] = NetCounters{}
	}
	return Reading{At: t0.Add(time.Duration(*m.n) * 5 * time.Second), MemTotal: 100, Nets: nets}, nil
}

func TestAPartialSampleIsNotAFailedSample(t *testing.T) {
	var failed, partial atomic.Int32
	oldE, oldP := logError, logPartial
	logError = func(string, error) { failed.Add(1) }
	logPartial = func(string, error) { partial.Add(1) }
	t.Cleanup(func() { logError, logPartial = oldE, oldP })

	svc, err := New(Config{Dir: t.TempDir(), Source: manySeries{n: new(int)}, Clock: stats.NewFakeClock(t0)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Close() })
	var tickErr error
	for i := 0; i < 30 && !stats.IsPartial(tickErr); i++ {
		tickErr = svc.Tick(context.Background())
	}
	if !stats.IsPartial(tickErr) {
		t.Fatalf("precondition: Tick = %v, want a partial error", tickErr)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { svc.Run(ctx); close(done) }()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	if failed.Load() != 0 || partial.Load() != 1 {
		t.Fatalf("failed logs %d, partial logs %d; want 0 and 1", failed.Load(), partial.Load())
	}
}
