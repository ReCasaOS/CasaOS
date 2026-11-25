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

func TestAListThatFallsBackDoesNotRestartTheWait(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	m.list[0].Latest = "v0.2.6"
	var tags []string
	m.onUpdate = func(string) { tags = append(tags, m.list[0].Latest) }
	ctx := context.Background()

	at("2026-09-24 03:30") // v0.2.6 seen, online
	a.Check(ctx)
	m.list[0].Latest = "v0.2.5" // the shipped catalogue for an hour
	at("2026-09-24 04:30")
	a.Check(ctx)
	m.list[0].Latest = "v0.2.6"
	at("2026-09-24 05:30")
	a.Check(ctx)
	if got := a.load().Modules["virt"]; got.Tag != "v0.2.6" || !got.FirstSeen.Equal(local("2026-09-24 03:30")) {
		t.Fatalf("virt in autoupdate.json = %+v, want v0.2.6 first seen at 03:30", got)
	}

	at("2026-09-25 03:30")
	a.Check(ctx)
	if !reflect.DeepEqual(tags, []string{"v0.2.6"}) {
		t.Fatalf("updated to %v, want v0.2.6 24 hours after its first sight", tags)
	}
}

func TestAnOlderTagIsNeverUpdatedTo(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	m.list[0].Latest = "v0.2.6"
	ctx := context.Background()

	at("2026-09-23 03:30")
	a.Check(ctx)
	m.list[0].Latest = "v0.2.5" // falls back for the whole night
	at("2026-09-25 03:30")
	a.Check(ctx)
	if len(m.updated) != 0 {
		t.Fatalf("updated %v to a tag older than the one recorded", m.updated)
	}
}

func TestAFailedTagIsNotRetriedAfterAFallBack(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	m.list[0].Latest = "v0.2.6"
	m.fail = map[string]string{"virt": "put back"}
	ctx := context.Background()

	at("2026-09-23 03:30")
	a.Check(ctx)
	at("2026-09-24 03:30") // v0.2.6 fails
	a.Check(ctx)
	m.list[0].Latest = "v0.2.5"
	at("2026-09-24 04:30")
	a.Check(ctx)
	m.list[0].Latest = "v0.2.6"
	for day := 1; day <= 3; day++ {
		at(local("2026-09-24 03:30").AddDate(0, 0, day).Format("2006-01-02 15:04"))
		a.Check(ctx)
	}
	if !reflect.DeepEqual(m.updated, []string{"virt"}) {
		t.Fatalf("updated %v: the failed v0.2.6 was tried again after the fall back", m.updated)
	}
}

func TestALaterTagAfterAnUpdateStartsAFreshWait(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	ctx := context.Background()

	at("2026-09-23 03:30")
	a.Check(ctx)
	at("2026-09-24 03:30") // v0.2.5 installed
	a.Check(ctx)
	m.list = []ModuleUpdate{{Name: "virt", Installed: "v0.2.5", Latest: "v0.2.6"}}
	at("2026-09-24 04:00")
	a.Check(ctx)
	at("2026-09-25 03:59")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt"}) {
		t.Fatalf("updated %v before v0.2.6 was 24 hours old", m.updated)
	}
	at("2026-09-25 04:00")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt", "virt"}) {
		t.Fatalf("updated %v, want v0.2.6 after its own wait", m.updated)
	}
}

func TestATagSpelledDifferentlyKeepsItsWaitAndItsTry(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	m.fail = map[string]string{"virt": "put back"}
	ctx := context.Background()

	at("2026-09-23 03:30")
	a.Check(ctx)
	at("2026-09-24 03:30") // v0.2.5 fails
	a.Check(ctx)
	before := a.load().Modules["virt"]
	m.list[0].Latest = "v0.2.05" // the same version, written otherwise
	for day := 1; day <= 3; day++ {
		at(local("2026-09-24 03:30").AddDate(0, 0, day).Format("2006-01-02 15:04"))
		a.Check(ctx)
	}
	if got := a.load().Modules["virt"]; got != before {
		t.Fatalf("virt in autoupdate.json = %+v, want the record kept: %+v", got, before)
	}
	if !reflect.DeepEqual(m.updated, []string{"virt"}) {
		t.Fatalf("updated %v: the failed version was tried again under another spelling", m.updated)
	}
}

func TestTheTryIsSavedBeforeTheUpdateStarts(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	ctx := context.Background()
	at("2026-09-24 03:30")
	a.Check(ctx)
	at("2026-09-25 03:30")
	m.onUpdate = func(name string) {
		// what a core restarted now would read
		if got := (&AutoUpdate{Root: a.Root}).load().Modules[name]; !got.Tried.Equal(local("2026-09-25 03:30")) {
			t.Errorf("autoupdate.json during the update has %s as %+v, want tried at 03:30", name, got)
		}
	}
	a.Check(ctx)
	if len(m.updated) != 1 {
		t.Fatalf("updated %v, want virt", m.updated)
	}
}

// A module written outside ReCasaOS is left alone unless the owner of the box said yes for it: not seen, not
// tried, not told. Allowed, it waits its 24 hours as a module of ReCasaOS does. The module of ReCasaOS beside it
// is updated as before in both cases.
func TestAForeignModuleIsLeftAloneUnlessItsOwnerSaidYes(t *testing.T) {
	a, _, m, at, alerts := newModulesBox(t)
	m.list = append(m.list, ModuleUpdate{Name: "tool", Installed: "v1.0.0", Latest: "v1.1.0", Foreign: true})
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	at("2026-09-25 03:30")
	a.Check(ctx)
	at("2026-09-26 03:30")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt"}) {
		t.Fatalf("updated %v, want virt alone", m.updated)
	}
	if _, seen := a.load().Modules["tool"]; seen {
		t.Fatalf("the foreign module that is not allowed was recorded: %+v", a.load().Modules)
	}
	for _, sentence := range *alerts {
		if strings.Contains(sentence, "tool") {
			t.Fatalf("the alert names the foreign module: %q", sentence)
		}
	}

	// the owner says yes: first seen now, and updated 24 hours later, once
	m.list[len(m.list)-1].Allowed = true // tool
	at("2026-09-26 03:40")
	a.Check(ctx)
	at("2026-09-27 03:39")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt"}) {
		t.Fatalf("updated %v before the allowed module's version was 24 hours old on this box", m.updated)
	}
	if got := a.load().Modules["tool"]; got.Tag != "v1.1.0" || !got.FirstSeen.Equal(local("2026-09-26 03:40")) {
		t.Fatalf("the allowed module was recorded as %+v", got)
	}
	at("2026-09-27 03:40")
	a.Check(ctx)
	if !reflect.DeepEqual(m.updated, []string{"virt", "tool"}) {
		t.Fatalf("updated %v, want virt then tool", m.updated)
	}
	if n := len(*alerts); n == 0 || !strings.Contains((*alerts)[n-1], "tool v1.0.0 → v1.1.0") {
		t.Fatalf("alerts %q", *alerts)
	}
}

// A foreign module that was allowed and then refused is no longer tried, even when its version is old enough.
func TestAForeignModuleNoLongerAllowedIsNotTried(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	m.list = []ModuleUpdate{{Name: "tool", Installed: "v1.0.0", Latest: "v1.1.0", Foreign: true, Allowed: true}}
	ctx := context.Background()

	at("2026-09-24 02:00") // first seen, outside the window
	a.Check(ctx)
	m.list[0].Allowed = false
	at("2026-09-25 03:30")
	a.Check(ctx)
	if len(m.updated) != 0 {
		t.Fatalf("updated %v after the owner said no", m.updated)
	}
}

// Off is off, for an allowed foreign module too.
func TestAnAllowedForeignModuleWaitsForAutomaticUpdates(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	saveState(t, a, State{WindowStart: "03:00", WindowEnd: "05:00"})
	m.list = []ModuleUpdate{{Name: "tool", Installed: "v1.0.0", Latest: "v1.1.0", Foreign: true, Allowed: true}}
	ctx := context.Background()

	at("2026-09-24 03:30")
	a.Check(ctx)
	at("2026-09-26 03:30")
	a.Check(ctx)
	if len(m.updated) != 0 || m.listed != 0 {
		t.Fatalf("updated %v, listed %d times while automatic updates are off", m.updated, m.listed)
	}
}
