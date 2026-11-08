package fleet

import (
	"context"
	"testing"
	"time"
)

func viewOf(t *testing.T, f *Fleet) BackupsView {
	t.Helper()
	v, on := f.BackupsView()
	if !on {
		t.Fatal("BackupsView says the master is off")
	}
	return v
}

func boxOf(t *testing.T, v BackupsView, id string) BoxBackups {
	t.Helper()
	for _, b := range v.Boxes {
		if b.BoxID == id {
			return b
		}
	}
	t.Fatalf("box %s is not in the view: %+v", id, v.Boxes)
	return BoxBackups{}
}

func TestBackupsViewListsEveryBoxEvenNeverPolled(t *testing.T) {
	f, ids, _, _ := backupMaster(t, "Maison", "Chalet")
	f.passBackups(context.Background()) // polls the two online boxes
	third := addBox(t, f, "Garage").BoxID
	f.Notifies = func() bool { return true }
	f.backupEvery = 7 * time.Minute

	v := viewOf(t, f)
	if len(v.Boxes) != 3 || !v.Notify || v.IntervalSeconds != 420 {
		t.Fatalf("view = %+v", v)
	}
	if got := boxOf(t, v, ids[0]); !got.Online || got.PolledAt.IsZero() || got.State != "ok" || !got.Green || len(got.Couples) != 1 {
		t.Errorf("polled box = %+v", got)
	}
	never := boxOf(t, v, third)
	if never.Online || !never.PolledAt.IsZero() || never.Stale || never.State != "unknown" || never.Green || never.Couples == nil || len(never.Couples) != 0 || never.Name != "Garage" {
		t.Errorf("never polled box = %+v", never)
	}
	markOnline(f, third) // online comes from the tunnel at read time
	if got := boxOf(t, viewOf(t, f), third); !got.Online || got.Green || !got.PolledAt.IsZero() {
		t.Errorf("never polled, online = %+v", got)
	}
}

func TestBackupsViewOfflineBoxKeepsItsSnapshotAndIsNotGreen(t *testing.T) {
	f, ids, _, _ := backupMaster(t, "Maison")
	f.passBackups(context.Background())
	markOffline(f, ids[0])
	got := boxOf(t, viewOf(t, f), ids[0])
	if got.Online || got.Green || got.Stale || len(got.Couples) != 1 || got.State != "ok" || got.PolledAt.IsZero() {
		t.Errorf("offline box = %+v", got)
	}
}

func TestBackupsViewStaleIsNotGreen(t *testing.T) {
	f, ids, _, _ := backupMaster(t, "Maison")
	f.passBackups(context.Background())
	for _, c := range []struct {
		age   time.Duration
		stale bool
	}{{staleAfter, false}, {staleAfter + time.Second, true}} {
		f.Now = func() time.Time { return pollNow.Add(c.age) }
		got := boxOf(t, viewOf(t, f), ids[0])
		if got.Stale != c.stale || got.Green == c.stale {
			t.Errorf("age %v: stale = %v green = %v", c.age, got.Stale, got.Green)
		}
	}
}

func TestBackupsViewShowsTheCurrentNameAndOnlyBoxesStillInTheFleet(t *testing.T) {
	f, ids, _, _ := backupMaster(t, "Maison", "Chalet")
	f.passBackups(context.Background())
	name := "Villa"
	if err := f.RenameBox(ids[0], &name, nil); err != nil {
		t.Fatal(err)
	}
	if err := f.RemoveBox(ids[1]); err != nil {
		t.Fatal(err)
	}
	v := viewOf(t, f)
	if len(v.Boxes) != 1 || v.Boxes[0].Name != "Villa" {
		t.Errorf("view = %+v", v.Boxes)
	}
}

func TestBackupsViewIsACopyOfTheStore(t *testing.T) {
	f, ids, _, _ := backupMaster(t, "Maison")
	f.backupMu.Lock()
	f.backups = map[string]BoxBackups{}
	f.backups[ids[0]] = BoxBackups{
		BoxID: ids[0], PolledAt: pollNow, State: "ok", Green: true,
		Couples:              []Couple{{App: "a", Destination: "d", ResticCheck: &ResticCheck{OK: true}}},
		VerificationSchedule: &VerificationSchedule{EveryDays: 7},
	}
	f.backupMu.Unlock()
	got := boxOf(t, viewOf(t, f), ids[0])
	got.Couples[0].App = "changed"
	got.Couples[0].ResticCheck.OK = false
	got.VerificationSchedule.EveryDays = 1
	again := boxOf(t, viewOf(t, f), ids[0])
	if again.Couples[0].App != "a" || !again.Couples[0].ResticCheck.OK || again.VerificationSchedule.EveryDays != 7 {
		t.Errorf("the store followed the view: %+v", again)
	}
}

func TestBackupsViewOffWhenNotAMaster(t *testing.T) {
	f := New(t.TempDir())
	if v, on := f.BackupsView(); on || len(v.Boxes) != 0 {
		t.Errorf("view = %+v, on = %v", v, on)
	}
}
