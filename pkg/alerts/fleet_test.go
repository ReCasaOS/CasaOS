package alerts

import "testing"

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
