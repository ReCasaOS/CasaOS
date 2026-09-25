package alerts

import (
	"reflect"
	"testing"
	"time"
)

func TestAutomaticUpdatesAlerts(t *testing.T) {
	for result, want := range map[string]string{
		"succeeded": "ReCasaOS updated itself to v0.5.8.",
		"failed":    "The automatic update to v0.5.8 failed: it is tried again next night.",
		"paused":    "The automatic update to v0.5.8 failed twice: it is paused until you resume it in the settings, or a newer release comes out.",
	} {
		t.Run(result, func(t *testing.T) {
			h, o, _ := newHub(t)

			h.AutoUpdate(result, "v0.5.8")
			h.sending.Wait()

			if len(o.sent) != 2 || o.sent[0].title != "ReCasaOS · box · updates" || o.texts(phone)[0] != want+"\nhttp://192.168.1.20" {
				t.Fatalf("sent %+v, want %q", o.sent, want)
			}
			if _, sent := h.sent["update:v0.5.8"]; !sent {
				t.Fatal("not on record under update:v0.5.8")
			}
		})
	}
}

// With automatic updates off, a newer release is announced once, looked for
// at most once a day.
func TestANewerReleaseIsAnnouncedOnce(t *testing.T) {
	h, o, now := newHub(t)
	latest, auto := "v0.5.8", false
	h.Latest = func() string { return latest }
	h.Current = func() string { return "0.5.7" }
	h.AutoUpdates = func() bool { return auto }
	check := func(after time.Duration) {
		*now = now.Add(after)
		h.checkRelease()
		h.sending.Wait()
	}

	check(0)
	check(time.Hour)
	check(24 * time.Hour) // a day later, the same release: once is enough
	latest = "v0.5.9"
	check(time.Hour) // looked at less than a day ago
	auto = true
	check(24 * time.Hour) // automatic updates announce their own
	auto = false
	check(24 * time.Hour)

	want := []string{
		"ReCasaOS v0.5.8 is out, v0.5.7 is installed: update from the dashboard's settings.\nhttp://192.168.1.20",
		"ReCasaOS v0.5.9 is out, v0.5.7 is installed: update from the dashboard's settings.\nhttp://192.168.1.20",
	}
	if got := o.texts(phone); !reflect.DeepEqual(got, want) {
		t.Fatalf("sent %q, want %q", got, want)
	}
}

func TestNoAnnouncementWhenUpToDateOrUnknown(t *testing.T) {
	for _, latest := range []string{"", "v0.5.7", "v0.5.6"} {
		h, o, _ := newHub(t)
		h.Latest = func() string { return latest }
		h.Current = func() string { return "0.5.7" }
		h.AutoUpdates = func() bool { return false }

		h.checkRelease()
		h.sending.Wait()

		if len(o.sent) != 0 {
			t.Fatalf("latest %q: sent %+v", latest, o.sent)
		}
	}
}

// A release announced while there was no channel is announced again once there is one.
func TestAReleaseNotSentIsNotAnnounced(t *testing.T) {
	h, o, now := newHub(t)
	h.Latest = func() string { return "v0.5.8" }
	h.Current = func() string { return "0.5.7" }
	h.AutoUpdates = func() bool { return false }
	channels := h.load()
	saveConfig(t, h, defaults())

	h.checkRelease()
	saveConfig(t, h, channels)
	*now = now.Add(24 * time.Hour)
	h.checkRelease()
	h.sending.Wait()

	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("sent %q, want the release once there is a channel", got)
	}
}
