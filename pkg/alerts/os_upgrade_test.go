package alerts

import (
	"reflect"
	"testing"
)

// The core tells the owner of an upgrade through the category of updates, to every channel, with the dashboard's address as for any alert.
func TestAnUpgradeNoticeGoesToEveryChannelAsAnUpdate(t *testing.T) {
	h, o, _ := newHub(t)

	if !h.OSUpgradeNotice("run:awaiting_reboot:1", "The upgrade waits for its restart.") {
		t.Fatal("a channel was there, and the notice was not taken")
	}
	h.sending.Wait()

	want := message{phone, "ReCasaOS · box · updates", "The upgrade waits for its restart.\nhttp://192.168.1.20"}
	if len(o.sent) != 2 || (o.sent[0] != want && o.sent[1] != want) {
		t.Fatalf("sent %+v, want %+v and the same to the mail box", o.sent, want)
	}
}

// Two things to say that are not the same thing are two messages: a run that stopped again after the owner resumed it is told again, and not held back by the six hours.
func TestTwoUpgradeNoticesOfOneRunAreTwoMessages(t *testing.T) {
	h, o, _ := newHub(t)

	h.OSUpgradeNotice("run:stopped_at_waypoint:2", "It stopped half way.")
	h.OSUpgradeNotice("run:stopped_at_waypoint:3", "It stopped half way again.")
	h.sending.Wait()

	if got := o.texts(phone); !reflect.DeepEqual(sortedTexts(got), sortedTexts([]string{"It stopped half way.\nhttp://192.168.1.20", "It stopped half way again.\nhttp://192.168.1.20"})) {
		t.Fatalf("the phone got %q", got)
	}
}

// With the category of updates off, or no channel, nothing is sent and the core is told so, as for any alert.
func TestAnUpgradeNoticeIsLeftUnsentWhenUpdatesAreOff(t *testing.T) {
	h, o, _ := newHub(t)
	saveConfig(t, h, Config{Channels: []Channel{{ID: "a1", Name: "Phone", URL: phone}}, Categories: map[string]bool{Updates: false, Apps: true}, DiskThreshold: 90})

	if h.OSUpgradeNotice("run:succeeded:1", "It worked.") {
		t.Fatal("a notice was taken with the updates off")
	}
	h.sending.Wait()
	if len(o.sent) != 0 {
		t.Fatalf("sent %+v", o.sent)
	}
}

func sortedTexts(in []string) []string {
	out := append([]string(nil), in...)
	for i := range out {
		for j := i + 1; j < len(out); j++ {
			if out[j] < out[i] {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}
