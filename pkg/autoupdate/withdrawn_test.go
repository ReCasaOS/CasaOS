package autoupdate

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func (f *fakeModules) Withdrawn(context.Context) ([]Withdrawn, error) {
	return append([]Withdrawn(nil), f.withdrawn...), f.withdrawnErr
}

// withdrawnBox is newModulesBox with virt v0.2.4 installed and withdrawn from the catalogue; said are the
// withdrawals told, as "name tag reason", and told is what the alerts answer.
func withdrawnBox(t *testing.T) (a *AutoUpdate, m *fakeModules, said *[]string, told *bool) {
	t.Helper()
	a, _, m, _, _ = newModulesBox(t)
	m.list = nil
	m.withdrawn = []Withdrawn{{Name: "virt", Tag: "v0.2.4", Reason: "It loses the data of its disks."}}
	said, told = &[]string{}, new(bool)
	*told = true
	a.NotifyWithdrawn = func(name, tag, reason string) bool {
		*said = append(*said, name+" "+tag+" "+reason)
		return *told
	}
	return a, m, said, told
}

func TestAWithdrawnVersionIsSaidOnce(t *testing.T) {
	a, m, said, _ := withdrawnBox(t)
	for range 10 {
		a.Check(context.Background())
	}
	if want := []string{"virt v0.2.4 It loses the data of its disks."}; !reflect.DeepEqual(*said, want) {
		t.Fatalf("said %q, want %q", *said, want)
	}
	// a core restarted reads it from autoupdate.json
	b := New(a.Root)
	b.Now, b.Command, b.Releases, b.Current, b.AppsBusy = a.Now, a.Command, a.Releases, a.Current, a.AppsBusy
	b.Modules, b.NotifyWithdrawn = m, a.NotifyWithdrawn
	b.Check(context.Background())
	if len(*said) != 1 {
		t.Fatalf("said again after a restart: %q", *said)
	}
	// another tag of the module is news
	m.withdrawn = []Withdrawn{{Name: "virt", Tag: "v0.2.5", Reason: "Broken."}}
	a.Check(context.Background())
	a.Check(context.Background())
	if len(*said) != 2 || (*said)[1] != "virt v0.2.5 Broken." {
		t.Fatalf("said %q", *said)
	}
	if len(m.updated) != 0 {
		t.Errorf("updated %v", m.updated)
	}
}

func TestAWithdrawalLiftedThenMadeAgainIsNotSaidTwice(t *testing.T) {
	a, m, said, _ := withdrawnBox(t)
	a.Check(context.Background())
	m.withdrawn = nil // the maintainers lifted it
	a.Check(context.Background())
	m.withdrawn = []Withdrawn{{Name: "virt", Tag: "v0.2.4", Reason: "Again."}}
	a.Check(context.Background())
	if len(*said) != 1 {
		t.Fatalf("said %q", *said)
	}
}

func TestAWithdrawnVersionIsSaidWithTheAutomaticUpdatesOff(t *testing.T) {
	a, m, said, _ := withdrawnBox(t)
	saveState(t, a, State{WindowStart: "03:00", WindowEnd: "05:00"})
	a.Check(context.Background())
	if len(*said) != 1 || m.listed != 0 {
		t.Fatalf("said %q, the updates listed %d times", *said, m.listed)
	}
	if a.load().Enabled {
		t.Errorf("turned on")
	}
}

func TestAWithdrawnVersionNotToldIsToldNextHour(t *testing.T) {
	a, _, said, told := withdrawnBox(t)
	*told = false // no channel, or the category is off
	a.Check(context.Background())
	*told = true
	a.Check(context.Background())
	a.Check(context.Background())
	if len(*said) != 2 {
		t.Fatalf("said %q", *said)
	}
}

func TestNothingInstalledIsWithdrawnNothingIsSaidNorWritten(t *testing.T) {
	a, m, said, _ := withdrawnBox(t)
	saveState(t, a, State{WindowStart: "03:00", WindowEnd: "05:00"})
	before := stateFile(t, a)
	m.withdrawn = nil
	a.Check(context.Background())
	m.withdrawnErr = errors.New("the catalogue cannot be read")
	a.Check(context.Background())
	if len(*said) != 0 || stateFile(t, a) != before {
		t.Fatalf("said %q, state %s", *said, stateFile(t, a))
	}
}

func TestAnOlderStateFileWithoutWithdrawalsLoads(t *testing.T) {
	a, _ := newBox(t)
	write(t, a.Root, StateFile, `{"enabled":true,"window_start":"02:00","window_end":"04:00","modules":{"virt":{"tag":"v0.2.5","first_seen":"2026-09-24T01:30:00Z"}}}`)
	if s := a.load(); !s.Enabled || s.WindowStart != "02:00" || s.Modules["virt"].Tag != "v0.2.5" || s.Withdrawn != nil {
		t.Fatalf("%+v", s)
	}
}

// the night never installs a version the catalogue withdrew, nor starts its day of waiting
func TestAWithdrawnLatestIsNeverSeenNorUpdated(t *testing.T) {
	a, _, m, at, _ := newModulesBox(t)
	m.list[0].Withdrawn = true
	for _, when := range []string{"2026-09-24 03:30", "2026-09-25 03:30", "2026-09-26 03:30"} {
		at(when)
		a.Check(context.Background())
	}
	if len(m.updated) != 0 || len(a.load().Modules) != 0 {
		t.Fatalf("updated %v, seen %+v", m.updated, a.load().Modules)
	}
}
