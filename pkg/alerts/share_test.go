package alerts

import "testing"

func TestShareGuardProblemUsesApps(t *testing.T) {
	h, o, _ := newHub(t)
	const sentence = "The shared app jellyfin is under attack: 23 addresses were banned in 10 minutes."
	h.ShareGuardProblem("share:wave:jellyfin", sentence, true)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 1 || got[0] != sentence+"\nhttp://192.168.1.20" {
		t.Fatalf("%q", got)
	}
	h.ShareGuardProblem("share:wave:jellyfin", "Resolved: the attack on the shared app jellyfin has calmed down.", false)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 2 || got[1] != "Resolved: the attack on the shared app jellyfin has calmed down.\nhttp://192.168.1.20" {
		t.Fatalf("%q", got)
	}
	c := h.load()
	c.Categories[Apps] = false
	saveConfig(t, h, c)
	h.ShareGuardProblem("share:wave:nextcloud", sentence, true)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 2 {
		t.Fatalf("sent with Apps off: %q", got)
	}
}
