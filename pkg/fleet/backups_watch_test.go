package fleet

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// hookCall is one call of the BackupProblem hook.
type hookCall struct {
	key, sentence string
	problem       bool
}

// hookLog records the hook's calls; passBackups makes them from its goroutines.
type hookLog struct {
	mu    sync.Mutex
	calls []hookCall
}

func (l *hookLog) add(key, sentence string, problem bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls = append(l.calls, hookCall{key, sentence, problem})
}

// take returns the calls since the last take.
func (l *hookLog) take() []hookCall {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := l.calls
	l.calls = nil
	return out
}

func markOffline(f *Fleet, id string) {
	f.mu.Lock()
	delete(f.tunnels, id)
	f.mu.Unlock()
}

// backupMaster is a master whose boxes, named names, are online, each
// answering through its own fakeBox (healthy until a test changes it).
func backupMaster(t *testing.T, names ...string) (*Fleet, []string, map[string]*fakeBox, *hookLog) {
	t.Helper()
	f, _ := newMaster(t)
	f.Now = func() time.Time { return pollNow }
	log := &hookLog{}
	f.BackupProblem = log.add
	ids := make([]string, 0, len(names))
	boxes := map[string]*fakeBox{}
	for _, name := range names {
		id := addBox(t, f, name).BoxID
		ids = append(ids, id)
		boxes[id] = &fakeBox{routes: oneCouple("", false, "")}
		markOnline(f, id)
	}
	f.call = func(ctx context.Context, id, method, path string) (Response, error) {
		return boxes[id].call(ctx, id, method, path)
	}
	return f, ids, boxes, log
}

// oneCouple is a box with one scheduled couple, jarvis to GDrive, last slot
// 10:00 (two hours before pollNow): its last run carries runErr, a late one
// started the day before, and verify is the status of its last restore test
// ("" for none). Every error text is "token=abc", which no alert may quote.
func oneCouple(runErr string, late bool, verify string) map[string]Response {
	at := "2026-11-08T10:00:10Z"
	if late {
		at = "2026-11-07T10:00:10Z"
	}
	verifications := `{"data":[]}`
	if verify != "" {
		verifications = fmt.Sprintf(`{"data":[{"id":"v1","destination":"GDrive","app":"jarvis","stamp":"s","status":%q,"error":"token=abc","started_at":"2026-11-08T11:00:00Z","finished_at":"2026-11-08T11:02:00Z"}]}`, verify)
	}
	return map[string]Response{
		am + "/backup/runs":                       ok(fmt.Sprintf(`{"data":[{"app":"jarvis","destination":"GDrive","stamp":"s","started_at":%q,"finished_at":%q,"scheduled":true,"error":%q}]}`, at, at, runErr)),
		am + "/backup/schedules":                  ok(fmt.Sprintf(`{"data":[{"app":"jarvis","destination":"GDrive","every":"daily","at":"10:00","keep":7,"enabled":true,"last_run":%q,"last_slot":"2026-11-08T10:00:00Z","next_slot":"2026-11-09T10:00:00Z"}]}`, at)),
		am + "/operations":                        ok(`{"operations":[],"os_upgrade_open":false}`),
		am + "/backup/verifications":              ok(verifications),
		am + "/backup/destinations/GDrive/restic": ok(`{"data":{"available":true,"enabled":false,"kept":false}}`),
	}
}

func (b *fakeBox) set(routes map[string]Response) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.routes, b.err = routes, nil
}

func (b *fakeBox) fail(err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.err = err
}

func wantCalls(t *testing.T, got []hookCall, want ...hookCall) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("hook calls:\n got %+v\nwant %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("hook call %d:\n got %+v\nwant %+v", i, got[i], want[i])
		}
	}
}

func TestAFailedBackupRaisesOnceAndResolvesWhenGreen(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison")
	now := pollNow
	f.Now = func() time.Time { return now }
	key := "fleet:" + ids[0] + ":backup:jarvis:GDrive:failed"
	failed := hookCall{key, "Backup of jarvis on Maison to GDrive failed. See the Fleet page.", true}

	boxes[ids[0]].set(oneCouple("token=abc", false, ""))
	f.passBackups(context.Background())
	wantCalls(t, log.take(), failed)

	// still failed five minutes later: the same key again, which the hub keeps quiet for 6 h
	now = now.Add(5 * time.Minute)
	f.passBackups(context.Background())
	wantCalls(t, log.take(), failed)

	now = now.Add(5 * time.Minute)
	boxes[ids[0]].set(oneCouple("", false, ""))
	f.passBackups(context.Background())
	wantCalls(t, log.take(), hookCall{key, "Resolved: the backup of jarvis on Maison to GDrive no longer fails.", false})

	now = now.Add(5 * time.Minute)
	f.passBackups(context.Background())
	wantCalls(t, log.take())
}

func TestLateAndFailedAreDistinctKeys(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison")
	prefix := "fleet:" + ids[0] + ":backup:jarvis:GDrive:"

	boxes[ids[0]].set(oneCouple("", true, ""))
	f.passBackups(context.Background())
	wantCalls(t, log.take(), hookCall{prefix + "late", "Backup of jarvis on Maison to GDrive is late: its last scheduled slot has passed. See the Fleet page.", true})

	boxes[ids[0]].set(oneCouple("token=abc", true, ""))
	f.passBackups(context.Background())
	wantCalls(t, log.take(),
		hookCall{prefix + "failed", "Backup of jarvis on Maison to GDrive failed. See the Fleet page.", true},
		hookCall{prefix + "late", "Resolved: the backup of jarvis on Maison to GDrive is no longer late.", false})
}

func TestOfflineBoxRaisesNothingAndResolvesNothing(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison")
	boxes[ids[0]].set(oneCouple("token=abc", false, ""))
	f.passBackups(context.Background())
	if got := log.take(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}

	// offline and green meanwhile: nothing is asked, nothing said
	markOffline(f, ids[0])
	boxes[ids[0]].set(oneCouple("", false, ""))
	f.passBackups(context.Background())
	wantCalls(t, log.take())
	if n := boxes[ids[0]].asked("/backup/runs"); n != 1 {
		t.Fatalf("an offline box was asked: %d", n)
	}

	// back: the problem it had is resolved by the first pass that reads it
	markOnline(f, ids[0])
	f.passBackups(context.Background())
	if got := log.take(); len(got) != 1 || got[0].problem {
		t.Fatalf("%+v", got)
	}
}

func TestAFailedPassKeepsTheProblemsAndRaisesNothing(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison")
	boxes[ids[0]].set(oneCouple("token=abc", false, ""))
	f.passBackups(context.Background())
	if got := log.take(); len(got) != 1 {
		t.Fatalf("%+v", got)
	}

	for _, err := range []error{errors.New("boom"), ErrOffline} {
		boxes[ids[0]].fail(err)
		f.passBackups(context.Background())
		wantCalls(t, log.take())
	}

	boxes[ids[0]].set(oneCouple("", false, ""))
	f.passBackups(context.Background())
	if got := log.take(); len(got) != 1 || got[0].problem {
		t.Fatalf("%+v", got)
	}
}

func TestNotVerifiableNeverAlerts(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison")
	boxes[ids[0]].set(oneCouple("", false, "not_verifiable"))
	f.passBackups(context.Background())
	wantCalls(t, log.take())

	// never backed up, nor any other state that is not a problem
	never := oneCouple("", false, "")
	never[am+"/backup/runs"] = ok(`{"data":[]}`)
	boxes[ids[0]].set(never)
	f.passBackups(context.Background())
	wantCalls(t, log.take())
}

func TestVerificationFailureHasItsOwnKey(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison")
	key := "fleet:" + ids[0] + ":verify:jarvis:GDrive"
	boxes[ids[0]].set(oneCouple("", false, "failed"))
	f.passBackups(context.Background())
	wantCalls(t, log.take(), hookCall{key, "Verification of the backup of jarvis on Maison to GDrive failed. See the Fleet page.", true})

	boxes[ids[0]].set(oneCouple("", false, "passed"))
	f.passBackups(context.Background())
	wantCalls(t, log.take(), hookCall{key, "Resolved: the verification of the backup of jarvis on Maison to GDrive no longer fails.", false})
}

func TestSentenceHasNoBoxErrorText(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison")
	boxes[ids[0]].set(oneCouple("token=abc", false, "failed"))
	f.passBackups(context.Background())
	boxes[ids[0]].set(oneCouple("", false, "passed"))
	f.passBackups(context.Background())
	got := log.take()
	if len(got) != 4 {
		t.Fatalf("%+v", got)
	}
	for _, c := range got {
		if strings.Contains(c.sentence, "token") || strings.Contains(c.key, "token") {
			t.Fatalf("box error text in an alert: %+v", c)
		}
	}
}

func TestRemovedBoxDropsItsSnapshotAndItsProblems(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison", "Bureau")
	for _, id := range ids {
		boxes[id].set(oneCouple("token=abc", false, ""))
	}
	f.passBackups(context.Background())
	if got := log.take(); len(got) != 2 {
		t.Fatalf("%+v", got)
	}

	if err := f.RemoveBox(ids[0]); err != nil {
		t.Fatal(err)
	}
	// dropped without a word, like the offline alert of a removed box
	wantCalls(t, log.take())
	f.backupMu.Lock()
	_, kept := f.backups[ids[0]]
	_, other := f.backups[ids[1]]
	var left []string
	for key := range f.backupProblems {
		left = append(left, key)
	}
	f.backupMu.Unlock()
	if kept || !other {
		t.Fatalf("snapshots: removed kept %v, other kept %v", kept, other)
	}
	if len(left) != 1 || !strings.HasPrefix(left[0], "fleet:"+ids[1]+":") {
		t.Fatalf("problems left: %v", left)
	}
}

func TestAttachWakesThePoller(t *testing.T) {
	f, srv := newMaster(t)
	code := addBox(t, f, "Maison")
	fakeMember(t, srv, code, okJSON)
	select {
	case id := <-f.backupWake:
		if id != code.BoxID {
			t.Fatalf("woke for %q", id)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("attach did not wake the poller")
	}
}

// setDelays shortens the poller's delays of f, before its loop starts.
func setDelays(f *Fleet, first, every, settle time.Duration) {
	f.backupFirst, f.backupEvery, f.backupSettle = first, every, settle
}

// runWatch runs watchBackups until stop says so, then stops it and waits for it.
func runWatch(t *testing.T, f *Fleet, what string, stop func() bool) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { f.watchBackups(ctx); close(done) }()
	defer func() { cancel(); <-done }()
	waitFor(t, what, stop)
}

func TestWatchBackupsRunsAtTheInterval(t *testing.T) {
	t.Run("first pass after the start delay", func(t *testing.T) {
		f, ids, boxes, _ := backupMaster(t, "Maison")
		setDelays(f, 10*time.Millisecond, time.Hour, time.Hour)
		runWatch(t, f, "the first pass", func() bool { return boxes[ids[0]].asked("/backup/runs") == 1 })
	})
	t.Run("then at the interval", func(t *testing.T) {
		f, ids, boxes, _ := backupMaster(t, "Maison")
		setDelays(f, time.Hour, 10*time.Millisecond, time.Hour)
		runWatch(t, f, "three passes", func() bool { return boxes[ids[0]].asked("/backup/runs") >= 3 })
	})
	t.Run("a wake polls that box only", func(t *testing.T) {
		f, ids, boxes, _ := backupMaster(t, "Maison", "Bureau")
		setDelays(f, time.Hour, time.Hour, 10*time.Millisecond)
		f.RefreshBackups(ids[1])
		runWatch(t, f, "the woken box's pass", func() bool { return boxes[ids[1]].asked("/backup/runs") == 1 })
		if n := boxes[ids[0]].asked("/backup/runs"); n != 0 {
			t.Fatalf("the other box was polled %d times", n)
		}
	})
}

func TestNewFleetPollsEveryFiveMinutes(t *testing.T) {
	f := New(t.TempDir())
	if f.backupEvery != 5*time.Minute || f.backupFirst != 10*time.Second || f.backupSettle != 2*time.Second {
		t.Fatalf("delays %v %v %v", f.backupEvery, f.backupFirst, f.backupSettle)
	}
}

// verifyCalls counts the raises and the resolutions of verification keys.
func verifyCalls(calls []hookCall) (raises, resolves int) {
	for _, c := range calls {
		switch {
		case !strings.Contains(c.key, ":verify:"):
		case c.problem:
			raises++
		default:
			resolves++
		}
	}
	return raises, resolves
}

// A verification alert lasts until a restore test passes (or the couple goes):
// a test running, or a pass that cannot read the verifications, does not end
// it. A resolution then would be false, and would let the hub tell the same
// failure again as new.
func TestAVerificationAlertLastsUntilATestPasses(t *testing.T) {
	failedThen := func(t *testing.T, between map[string]Response) []hookCall {
		f, ids, boxes, log := backupMaster(t, "Maison")
		boxes[ids[0]].set(oneCouple("", false, "failed"))
		f.passBackups(context.Background())
		boxes[ids[0]].set(between)
		f.passBackups(context.Background())
		boxes[ids[0]].set(oneCouple("", false, "failed"))
		f.passBackups(context.Background())
		return log.take()
	}
	t.Run("a test running", func(t *testing.T) {
		calls := failedThen(t, oneCouple("", false, "running"))
		// said at each failed pass (the hub sends it once), never resolved
		if raises, resolves := verifyCalls(calls); raises != 2 || resolves != 0 {
			t.Fatalf("%+v", calls)
		}
	})
	t.Run("the optional routes failing", func(t *testing.T) {
		routes := oneCouple("", false, "")
		routes[am+"/backup/verifications"] = Response{Status: 500}
		routes[am+"/backup/destinations/GDrive/restic"] = Response{Status: 500}
		calls := failedThen(t, routes)
		if raises, resolves := verifyCalls(calls); raises != 2 || resolves != 0 {
			t.Fatalf("%+v", calls)
		}
	})
	t.Run("a test passing", func(t *testing.T) {
		f, ids, boxes, log := backupMaster(t, "Maison")
		boxes[ids[0]].set(oneCouple("", false, "failed"))
		f.passBackups(context.Background())
		for range 2 {
			boxes[ids[0]].set(oneCouple("", false, "passed"))
			f.passBackups(context.Background())
		}
		if raises, resolves := verifyCalls(log.take()); raises != 1 || resolves != 1 {
			t.Fatalf("raises %d resolves %d", raises, resolves)
		}
	})
	t.Run("the couple removed", func(t *testing.T) {
		f, ids, boxes, log := backupMaster(t, "Maison")
		boxes[ids[0]].set(oneCouple("", false, "failed"))
		f.passBackups(context.Background())
		gone := oneCouple("", false, "")
		gone[am+"/backup/schedules"] = ok(`{"data":[]}`)
		for range 2 {
			boxes[ids[0]].set(gone)
			f.passBackups(context.Background())
		}
		if raises, resolves := verifyCalls(log.take()); raises != 1 || resolves != 1 {
			t.Fatalf("raises %d resolves %d", raises, resolves)
		}
	})
}

// gate makes each box's first request of a pass (its runs) wait for release;
// entered gets the box's id as it starts waiting.
type gate struct {
	mu       sync.Mutex
	inFlight int
	max      int
	entered  chan string
	release  chan struct{}
}

func newGate(f *Fleet, n int) *gate {
	g := &gate{entered: make(chan string, n), release: make(chan struct{})}
	next := f.call
	f.call = func(ctx context.Context, id, method, path string) (Response, error) {
		if strings.HasSuffix(path, "/backup/runs") {
			g.mu.Lock()
			g.inFlight++
			g.max = max(g.max, g.inFlight)
			g.mu.Unlock()
			g.entered <- id
			<-g.release
			g.mu.Lock()
			g.inFlight--
			g.mu.Unlock()
		}
		return next(ctx, id, method, path)
	}
	return g
}

func TestAPassPollsFourBoxesAtOnceInIDOrder(t *testing.T) {
	f, ids, _, _ := backupMaster(t, "a", "b", "c", "d", "e", "f")
	g := newGate(f, len(ids))
	done := make(chan struct{})
	go func() { f.passBackups(context.Background()); close(done) }()

	started := map[string]bool{}
	for range 4 {
		started[<-g.entered] = true
	}
	select {
	case id := <-g.entered:
		t.Fatalf("a fifth box started while four were polled: %s", id)
	case <-time.After(50 * time.Millisecond):
	}
	sorted := append([]string(nil), ids...)
	sort.Strings(sorted)
	for _, id := range sorted[:4] {
		if !started[id] {
			t.Fatalf("started %v, want the four first ids %v", started, sorted[:4])
		}
	}
	close(g.release)
	<-done
	if g.max != 4 {
		t.Fatalf("at most %d at once, want 4", g.max)
	}
}

// A box removed while its poll is on its way: the answer arrives for nothing.
func TestARemovedBoxsPollInFlightLeavesNothing(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison")
	boxes[ids[0]].set(oneCouple("token=abc", false, "failed"))
	g := newGate(f, 1)
	done := make(chan struct{})
	go func() { f.passBackups(context.Background()); close(done) }()
	<-g.entered
	if err := f.RemoveBox(ids[0]); err != nil {
		t.Fatal(err)
	}
	close(g.release)
	<-done
	wantCalls(t, log.take())
	f.backupMu.Lock()
	defer f.backupMu.Unlock()
	if len(f.backups) != 0 || len(f.backupProblems) != 0 {
		t.Fatalf("left: %v %v", f.backups, f.backupProblems)
	}
}

func TestDisableMasterForgetsTheBackups(t *testing.T) {
	f, ids, boxes, log := backupMaster(t, "Maison", "Bureau")
	for _, id := range ids {
		boxes[id].set(oneCouple("token=abc", false, ""))
	}
	f.passBackups(context.Background())
	if got := log.take(); len(got) != 2 {
		t.Fatalf("%+v", got)
	}
	if err := f.DisableMaster(); err != nil {
		t.Fatal(err)
	}
	f.backupMu.Lock()
	defer f.backupMu.Unlock()
	if len(f.backups) != 0 || len(f.backupProblems) != 0 {
		t.Fatalf("left: %v %v", f.backups, f.backupProblems)
	}
}
