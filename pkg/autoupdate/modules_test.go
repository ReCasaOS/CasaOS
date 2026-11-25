package autoupdate

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

// fakeModules is the modules of a box: the installed ones that have an update.
// An update that succeeds takes the module off the list, as the real list does.
type fakeModules struct {
	list     []ModuleUpdate
	listErr  error
	fail     map[string]string // name: the detail of an update that fails
	refuse   map[string]string // name: the detail of an update the box refuses to start
	onUpdate func(name string)

	listed  int
	updated []string
}

func (f *fakeModules) List(context.Context) ([]ModuleUpdate, error) {
	f.listed++
	return append([]ModuleUpdate(nil), f.list...), f.listErr
}

func (f *fakeModules) Update(_ context.Context, name string) (Outcome, string) {
	f.updated = append(f.updated, name)
	if f.onUpdate != nil {
		f.onUpdate(name)
	}
	if detail, refused := f.refuse[name]; refused {
		return NotStarted, detail
	}
	if detail, failed := f.fail[name]; failed {
		return Failed, detail
	}
	for i, m := range f.list {
		if m.Name == name {
			f.list = append(f.list[:i], f.list[i+1:]...)
			break
		}
	}
	return Updated, ""
}

// newModulesBox is newBox up to date (no release to start), turned on from 03:00
// to 05:00, with virt v0.2.4 installed and v0.2.5 in the catalogue. at moves the
// clock; alerts are the round's sentences.
func newModulesBox(t *testing.T) (a *AutoUpdate, b *box, m *fakeModules, at func(string), alerts *[]string) {
	t.Helper()
	a, b = newBox(t)
	b.current = "0.5.8"
	saveState(t, a, on())
	m = &fakeModules{list: []ModuleUpdate{{Name: "virt", Installed: "v0.2.4", Latest: "v0.2.5"}}}
	a.Modules = m
	alerts = &[]string{}
	a.NotifyModules = func(sentence string) { *alerts = append(*alerts, sentence) }
	at = func(value string) { a.Now = func() time.Time { return local(value) } }
	return a, b, m, at, alerts
}

func TestModuleUpdateWaitsADay(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	ctx := context.Background()

	at("2026-09-24 03:30") // first seen
	a.Check(ctx)
	at("2026-09-25 03:29") // 23 h 59 min later, inside the window
	a.Check(ctx)
	if len(m.updated) != 0 {
		t.Fatalf("updated %v before the module's version was 24 hours old on this box", m.updated)
	}

	at("2026-09-25 03:30")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt"}) {
		t.Fatalf("updated %v 24 hours after the first sight, want virt", m.updated)
	}
}

func TestModuleUpdateOnlyInsideTheWindow(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	at("2026-09-25 06:00") // old enough, after the window
	a.Check(ctx)
	at("2026-09-26 02:59")
	a.Check(ctx)
	if len(m.updated) != 0 {
		t.Fatalf("updated %v outside the window", m.updated)
	}

	at("2026-09-26 03:00")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt"}) {
		t.Fatalf("updated %v when the window opened, want virt", m.updated)
	}
}

func TestNoModuleUpdateWhenAutomaticUpdatesAreOff(t *testing.T) {
	a, b, m, at, _ := newModulesBox(t)
	saveState(t, a, State{WindowStart: "03:00", WindowEnd: "05:00"})
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	at("2026-09-26 03:30")
	a.Check(ctx)

	if len(m.updated) != 0 || m.listed != 0 {
		t.Fatalf("updated %v, listed %d times while automatic updates are off", m.updated, m.listed)
	}
	if len(b.commands) != 0 {
		t.Fatalf("asked systemd %v while off", b.commands)
	}
}

func TestANewTagRestartsTheWait(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	ctx := context.Background()

	at("2026-09-24 03:30") // v0.2.5 seen
	a.Check(ctx)
	m.list[0].Latest = "v0.2.6"
	at("2026-09-25 02:00") // v0.2.6 seen before v0.2.5 was 24 hours old
	a.Check(ctx)
	at("2026-09-25 03:30") // v0.2.5 would be old enough, v0.2.6 is not
	a.Check(ctx)
	if len(m.updated) != 0 {
		t.Fatalf("updated %v: the wait starts again from the newer tag's first sight", m.updated)
	}

	at("2026-09-26 03:00")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt"}) {
		t.Fatalf("updated %v 25 hours after v0.2.6 was seen, want virt", m.updated)
	}
	if got := a.load().Modules["virt"].Tag; got != "v0.2.6" {
		t.Fatalf("tag in autoupdate.json = %q, want v0.2.6", got)
	}
}

func TestAFailedModuleUpdateIsNotTriedAgain(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	m.list = []ModuleUpdate{
		{Name: "scan", Installed: "v0.1.2", Latest: "v0.1.3"},
		{Name: "virt", Installed: "v0.2.4", Latest: "v0.2.5"},
	}
	m.fail = map[string]string{"scan": "put back"}
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	at("2026-09-25 03:30")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"scan", "virt"}) {
		t.Fatalf("updated %v, want scan then virt in the same round", m.updated)
	}

	for day := 0; day < 10; day++ {
		at(local("2026-09-25 03:40").AddDate(0, 0, day).Format("2006-01-02 15:04"))
		a.Check(ctx)
	}
	if !reflect.DeepEqual(m.updated, []string{"scan", "virt"}) {
		t.Fatalf("updated %v over ten more rounds: a failed version is tried once", m.updated)
	}

	m.list[0].Latest = "v0.1.4" // another tag: its own wait, its own try
	at("2026-10-05 03:30")
	a.Check(ctx)
	at("2026-10-06 03:30")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"scan", "virt", "scan"}) {
		t.Fatalf("updated %v, want scan tried again for v0.1.4", m.updated)
	}
}

func TestModulesWaitWhileReCasaOSUpdates(t *testing.T) {
	for name, hold := range map[string]func(a *AutoUpdate, b *box){
		"a release started in this round": func(a *AutoUpdate, b *box) { b.current = "0.5.7" },
		"casaos-update runs":              func(a *AutoUpdate, b *box) { b.unit = "active\n" },
		"a package update, Docker or the OS upgrade": func(a *AutoUpdate, b *box) {
			a.Busy = func() bool { return true }
		},
		"the last automatic update still runs": func(a *AutoUpdate, b *box) {
			if err := a.modify(func(s *State) bool { s.Last = running("v0.5.8"); return true }); err != nil {
				t.Fatal(err)
			}
			b.unit = "active\n"
		},
	} {
		t.Run(name, func(t *testing.T) {
			a, b, m, at, _ := newModulesBox(t)
			ctx := context.Background()
			at("2026-09-23 03:30") // seen a day before
			a.Check(ctx)
			hold(a, b)

			at("2026-09-24 03:30")
			a.Check(ctx)
			if len(m.updated) != 0 {
				t.Fatalf("updated %v while ReCasaOS or the packages were being updated", m.updated)
			}
		})
	}
}

func TestARoundStopsWhenTheBoxBecomesBusy(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	m.list = []ModuleUpdate{
		{Name: "scan", Installed: "v0.1.2", Latest: "v0.1.3"},
		{Name: "virt", Installed: "v0.2.4", Latest: "v0.2.5"},
	}
	busy := false
	a.Busy = func() bool { return busy }
	m.onUpdate = func(string) { busy = true } // say, a package update the owner started
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	at("2026-09-25 03:30")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"scan"}) {
		t.Fatalf("updated %v, want the round to stop before virt", m.updated)
	}

	busy = false
	m.onUpdate = nil
	at("2026-09-25 04:30")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"scan", "virt"}) {
		t.Fatalf("updated %v: virt was not tried, so the next round tries it", m.updated)
	}
}

func TestTheNightTellsWhatItDidWithModules(t *testing.T) {
	a, _, m, at, alerts := newModulesBox(t)
	m.list = []ModuleUpdate{
		{Name: "scan", Installed: "v0.1.2", Latest: "v0.1.3"},
		{Name: "virt", Installed: "v0.2.4", Latest: "v0.2.5"},
	}
	m.fail = map[string]string{"scan": "put back"}
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	if len(*alerts) != 0 {
		t.Fatalf("alerts %q when nothing was attempted", *alerts)
	}
	at("2026-09-25 03:30")
	a.Check(ctx)
	want := "Modules updated: virt v0.2.4 → v0.2.5. Module updates failed: scan v0.1.2 → v0.1.3 (put back)."
	if !reflect.DeepEqual(*alerts, []string{want}) {
		t.Fatalf("alerts %q, want one: %q", *alerts, want)
	}
	at("2026-09-25 04:30")
	a.Check(ctx)
	if len(*alerts) != 1 {
		t.Fatalf("alerts %q: no alert for a round that attempted nothing", *alerts)
	}
}

func TestAListThatFailsChangesNothing(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	ctx := context.Background()
	at("2026-09-24 03:30")
	a.Check(ctx)
	before := stateFile(t, a)
	m.listErr = errors.New("the catalogue cannot be read")
	at("2026-09-25 03:30")
	a.Check(ctx)
	if len(m.updated) != 0 {
		t.Fatalf("updated %v without a list", m.updated)
	}
	if after := stateFile(t, a); after != before {
		t.Fatalf("autoupdate.json = %s, want it as it was: %s", after, before)
	}
}

func TestTheModulesAreKeptInAutoupdateJSON(t *testing.T) {
	a, _, _, at, _ := newModulesBox(t)
	at("2026-09-24 03:30")
	a.Check(context.Background())

	data := stateFile(t, a)
	if !strings.Contains(data, `"modules":{"virt":{"tag":"v0.2.5","first_seen":"2026-09-24T01:30:00Z"}}`) {
		t.Fatalf("autoupdate.json = %s, want virt's first sight, not tried", data)
	}
	// the previous version's reader ignores the key it does not know
	var old struct {
		Enabled     bool   `json:"enabled"`
		WindowStart string `json:"window_start"`
		WindowEnd   string `json:"window_end"`
	}
	if err := json.Unmarshal([]byte(data), &old); err != nil || !old.Enabled {
		t.Fatalf("the previous reader: %+v, %v", old, err)
	}
	// and a file without the key loads
	write(t, a.Root, StateFile, `{"enabled":true,"window_start":"03:00","window_end":"05:00"}`)
	if s := a.load(); !s.Enabled || s.Modules != nil {
		t.Fatalf("an older autoupdate.json loads as %+v", s)
	}
}

func TestARefusedModuleUpdateIsTriedAgainNextRound(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	m.refuse = map[string]string{"virt": "not started: busy"}
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	before := stateFile(t, a)
	at("2026-09-25 03:30")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt"}) {
		t.Fatalf("updated %v, want virt tried once", m.updated)
	}
	if after := stateFile(t, a); after != before {
		t.Fatalf("autoupdate.json = %s, want it as it was (not tried, first sight kept): %s", after, before)
	}

	delete(m.refuse, "virt")
	at("2026-09-25 04:30")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt", "virt"}) || len(m.list) != 0 {
		t.Fatalf("updated %v, list %v: want virt tried again and updated", m.updated, m.list)
	}
}

func TestARefusalDoesNotStopTheNextModule(t *testing.T) {
	a, _, m, at, alerts := newModulesBox(t)
	m.list = []ModuleUpdate{
		{Name: "scan", Installed: "v0.1.2", Latest: "v0.1.3"},
		{Name: "virt", Installed: "v0.2.4", Latest: "v0.2.5"},
	}
	m.refuse = map[string]string{"scan": "not started: apt_failed"}
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	at("2026-09-25 03:30")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"scan", "virt"}) {
		t.Fatalf("updated %v, want virt attempted after scan's refusal", m.updated)
	}
	if want := []string{"Modules updated: virt v0.2.4 → v0.2.5."}; !reflect.DeepEqual(*alerts, want) {
		t.Fatalf("alerts %q, want %q: a refusal is not told", *alerts, want)
	}
}

func TestNoAlertWhenNothingStarted(t *testing.T) {
	a, _, m, at, alerts := newModulesBox(t)
	m.refuse = map[string]string{"virt": "not started: busy"}
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	at("2026-09-25 03:30")
	a.Check(ctx)
	if len(m.updated) != 1 || len(*alerts) != 0 {
		t.Fatalf("updated %v, alerts %q: want one refused attempt and no alert", m.updated, *alerts)
	}
}
