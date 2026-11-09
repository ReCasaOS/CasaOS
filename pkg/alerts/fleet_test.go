package alerts

import (
	"testing"
	"time"
)

func TestFleetOfflineAlertAndResolution(t *testing.T) {
	h, o, _ := newHub(t)
	h.FleetOffline("b1", "Maison", true)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 1 || got[0] != "The box Maison has not reached the master for 10 minutes.\nhttp://192.168.1.20" {
		t.Fatalf("%q", got)
	}
	h.FleetOffline("b1", "Maison", false)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 2 || got[1] != "Resolved: the box Maison reached the master again.\nhttp://192.168.1.20" {
		t.Fatalf("%q", got)
	}
	// a resolution without an alert sent says nothing
	h.FleetOffline("b2", "Bureau", false)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 2 {
		t.Fatalf("%q", got)
	}
}

func TestFleetIsACategoryOnByDefault(t *testing.T) {
	if !defaults().Categories[Fleet] {
		t.Fatal("off")
	}
}

func TestFleetBackupProblemSendsWithBoxNameThenResolves(t *testing.T) {
	h, o, now := newHub(t)
	key := "fleet:b1:backup:jarvis:GDrive:failed"
	h.FleetBackupProblem(key, "Backup of jarvis on Maison to GDrive failed. See the Fleet page.", true)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 1 || got[0] != "Backup of jarvis on Maison to GDrive failed. See the Fleet page.\nhttp://192.168.1.20" {
		t.Fatalf("%q", got)
	}
	// the poller says it again every five minutes: nothing more within six hours
	*now = now.Add(5 * time.Minute)
	h.FleetBackupProblem(key, "Backup of jarvis on Maison to GDrive failed. See the Fleet page.", true)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("%q", got)
	}
	for range 2 {
		h.FleetBackupProblem(key, "Resolved: the backup of jarvis on Maison to GDrive no longer fails.", false)
		h.sending.Wait()
	}
	if got := o.texts(phone); len(got) != 2 || got[1] != "Resolved: the backup of jarvis on Maison to GDrive no longer fails.\nhttp://192.168.1.20" {
		t.Fatalf("%q", got)
	}
}

func TestDeliversNeedsAChannelAndTheCategory(t *testing.T) {
	h, _, _ := newHub(t)
	if !h.Delivers(Fleet) {
		t.Fatal("a channel and the category on: not delivered")
	}
	if _, err := h.Update(Change{Categories: map[string]bool{Fleet: false}}); err != nil {
		t.Fatal(err)
	}
	if h.Delivers(Fleet) {
		t.Fatal("category off: delivered")
	}
	saveConfig(t, h, Config{Channels: []Channel{}, Categories: map[string]bool{Fleet: true}, DiskThreshold: 90})
	if h.Delivers(Fleet) {
		t.Fatal("no channel: delivered")
	}
}
