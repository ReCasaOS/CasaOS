package alerts

import (
	"errors"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
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

// A notice that no channel took is not lost: the network may be down when a run stops, or not up yet when the machine is back from its restart, and the core has already written
// down that it told the owner. It is sent again, and once a channel takes it, it is not sent again.
func TestAnUpgradeNoticeThatNoChannelTookIsSentAgainWhenOneDoes(t *testing.T) {
	h, o, _ := newHub(t)
	h.OSUpgradeRetries = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond}
	// the network is down for the first two tries of the two channels, and back for the third
	var calls atomic.Int32
	h.Send = func(rawURL, title, text string) error {
		if calls.Add(1) <= 4 {
			return errors.New("network is unreachable")
		}
		return o.send(rawURL, title, text)
	}

	if !h.OSUpgradeNotice("run:stopped_at_waypoint:1", "It stopped half way.") {
		t.Fatal("a channel was there, and the notice was not taken")
	}
	h.sending.Wait()

	want := "It stopped half way.\nhttp://192.168.1.20"
	if got := o.texts(phone); len(got) != 1 || got[0] != want {
		t.Fatalf("the phone got %q", got)
	}
	if got := o.texts(mail); len(got) != 1 || got[0] != want {
		t.Fatalf("the mail box got %q", got)
	}
	if n := calls.Load(); n != 6 {
		t.Errorf("%d sends: it was sent again after a channel took it", n)
	}
}

// A notice that reaches no channel in all its tries is given up, and the core is not held for it.
func TestAnUpgradeNoticeIsGivenUpAfterItsLastTry(t *testing.T) {
	h, o, _ := newHub(t)
	h.OSUpgradeRetries = []time.Duration{time.Millisecond, time.Millisecond}
	down := errors.New("network is unreachable")
	o.mu.Lock()
	o.fail[phone], o.fail[mail] = down, down
	o.mu.Unlock()

	h.OSUpgradeNotice("run:failed:1", "It failed.")
	done := make(chan struct{})
	go func() { h.sending.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the sending is still trying")
	}
	if len(o.sent) != 0 {
		t.Fatalf("sent %+v", o.sent)
	}
}
