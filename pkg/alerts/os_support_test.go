package alerts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
)

const handoff = " ReCasaOS cannot upgrade the operating system for you yet: see the dashboard."

var (
	debian11 = map[string]string{"ID": "debian", "VERSION_ID": "11", "VERSION_CODENAME": "bullseye"}
	debian12 = map[string]string{"ID": "debian", "VERSION_ID": "12", "VERSION_CODENAME": "bookworm"}
	ubuntu20 = map[string]string{"ID": "ubuntu", "VERSION_ID": "20.04", "VERSION_CODENAME": "focal"}
	ubuntu22 = map[string]string{"ID": "ubuntu", "VERSION_ID": "22.04", "VERSION_CODENAME": "jammy"}
	mint     = map[string]string{"ID": "linuxmint", "ID_LIKE": "ubuntu debian", "VERSION_ID": "21.3", "VERSION_CODENAME": "virginia"}
)

// osFrom is the box of a release at the day of the hub's clock: the answer of the service, with the
// real table.
func osFrom(h *Hub, release map[string]string, now *time.Time) {
	h.OSSupport = func() ossupport.Report { return ossupport.Describe(release, "amd64", *now) }
}

// osMarker is what the marker file holds, "" when there is none.
func osMarker(h *Hub) string {
	data, err := os.ReadFile(filepath.Join(h.Root, OSSupportNotifiedFile))
	if err != nil {
		return ""
	}
	return string(data)
}

// onDay is the hub's clock at midnight of a day.
func onDay(t *testing.T, now *time.Time, day string) {
	t.Helper()
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		t.Fatal(err)
	}
	*now = d
}

func TestTheSupportOfTheOperatingSystemIsToldInWords(t *testing.T) {
	for name, tc := range map[string]struct {
		release map[string]string
		day     string
		want    string
	}{
		"Debian 11 ended": {debian11, "2026-10-10",
			"Debian 11 no longer receives Debian security updates since 31 August 2026." + handoff},
		"Debian 11 ended, a year later": {debian11, "2027-10-10",
			"Debian 11 no longer receives Debian security updates since 31 August 2026." + handoff},
		"Debian 12 ending": {debian12, "2028-01-15",
			"Debian 12 stops receiving Debian security updates on 30 June 2028." + handoff},
		"Ubuntu 22.04 ending, to the month": {ubuntu22, "2027-01-15",
			"Ubuntu 22.04 stops receiving standard security updates around June 2027." + handoff},
		"Ubuntu 22.04 ended, to the month": {ubuntu22, "2027-06-02",
			"Ubuntu 22.04 no longer receives standard security updates since around June 2027." + handoff},
		"Ubuntu 20.04 ended": {ubuntu20, "2026-10-10",
			"Ubuntu 20.04 no longer receives standard security updates since 29 May 2025." + handoff},
	} {
		t.Run(name, func(t *testing.T) {
			h, o, now := newHub(t)
			osFrom(h, tc.release, now)
			onDay(t, now, tc.day)

			h.checkOSSupport()
			h.sending.Wait()

			if got := o.texts(phone); len(got) != 1 || got[0] != tc.want+"\nhttp://192.168.1.20" {
				t.Fatalf("sent %q, want %q", got, tc.want)
			}
			if o.sent[0].title != "ReCasaOS · box · updates" {
				t.Fatalf("title = %q", o.sent[0].title)
			}
		})
	}
}

// Nothing for a release that is supported, for one that is not known, and for a hub that was never
// told how to look.
func TestNothingIsToldForASupportedOrUnknownSystem(t *testing.T) {
	for name, tc := range map[string]struct {
		release map[string]string
		day     string
	}{
		"Debian 12, supported":            {debian12, "2026-10-10"},
		"Ubuntu 22.04, supported":         {ubuntu22, "2026-12-02"},
		"a Mint, which is not known":      {mint, "2026-10-10"},
		"nothing read":                    {nil, "2026-10-10"},
		"a table gone stale, a Debian 11": {debian11, "2028-04-10"},
	} {
		t.Run(name, func(t *testing.T) {
			h, o, now := newHub(t)
			osFrom(h, tc.release, now)
			onDay(t, now, tc.day)

			h.checkOSSupport()
			h.sending.Wait()

			if len(o.sent) != 0 || len(h.sent) != 0 || osMarker(h) != "" {
				t.Fatalf("sent %+v, on record %v, marker %q; want nothing", o.sent, h.sent, osMarker(h))
			}
		})
	}
	h, o, _ := newHub(t)
	h.checkOSSupport() // the hub New made, with no one to ask
	h.sending.Wait()
	if len(o.sent) != 0 {
		t.Fatalf("sent %+v", o.sent)
	}
}

// One message, to each channel, for a state, however often it is looked at.
func TestAStateIsToldOnce(t *testing.T) {
	h, o, now := newHub(t)
	osFrom(h, debian11, now)
	onDay(t, now, "2026-10-10")

	for range 5 {
		h.checkOSSupport()
	}
	h.sending.Wait()

	if len(o.texts(phone)) != 1 || len(o.texts(mail)) != 1 {
		t.Fatalf("sent %+v, want one message for each of the two channels", o.sent)
	}
	if got := osMarker(h); got != "debian-11-security_ended\n" {
		t.Fatalf("marker = %q", got)
	}
	if _, kept := h.sent["os-support:debian-11-security_ended"]; !kept {
		t.Fatalf("not on record under os-support:debian-11-security_ended: %v", h.sent)
	}
}

// Told again only when the state changes: Ubuntu 22.04 goes from supported, to ending, to ended, a
// day, and the six quiet hours of an alert already sent do not hide the second state.
func TestAStateIsToldAgainOnlyWhenItChanges(t *testing.T) {
	h, o, now := newHub(t)
	osFrom(h, ubuntu22, now)

	step := func(day string) {
		onDay(t, now, day)
		h.checkOSSupport()
		h.sending.Wait()
	}
	step("2026-10-10")
	if len(o.sent) != 0 {
		t.Fatalf("supported: sent %+v", o.sent)
	}
	step("2026-12-03")
	step("2026-12-04")
	step("2027-05-31")
	if got := o.texts(phone); len(got) != 1 || !strings.HasPrefix(got[0], "Ubuntu 22.04 stops receiving standard security updates around June 2027.") {
		t.Fatalf("ending: sent %q", got)
	}
	if got := osMarker(h); got != "ubuntu-22.04-ending_soon\n" {
		t.Fatalf("marker = %q", got)
	}

	// the day after, a few hours later in the same six: the state is new
	step("2027-06-02")
	step("2027-06-03")
	got := o.texts(phone)
	if len(got) != 2 || !strings.HasPrefix(got[1], "Ubuntu 22.04 no longer receives standard security updates since around June 2027.") {
		t.Fatalf("ended: sent %q", got)
	}
	if got := osMarker(h); got != "ubuntu-22.04-security_ended\n" {
		t.Fatalf("marker = %q", got)
	}
}

// A box that was told, restarted, is not told again; a state that came while it was down is told once.
func TestAStateIsToldOnceAcrossARestart(t *testing.T) {
	h, o, now := newHub(t)
	osFrom(h, ubuntu22, now)
	onDay(t, now, "2027-01-15")
	h.checkOSSupport()
	h.sending.Wait()
	if len(o.texts(phone)) != 1 {
		t.Fatalf("sent %q", o.texts(phone))
	}
	if entries, _ := os.ReadDir(filepath.Dir(filepath.Join(h.Root, OSSupportNotifiedFile))); len(entries) != 2 {
		t.Errorf("the state directory holds %v, want alerts.json and the marker only: the write leaves nothing behind", entries)
	}

	again := restarted(h, o)
	osFrom(again, ubuntu22, now)
	again.checkOSSupport()
	again.sending.Wait()
	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("after the restart the same state was told again: %q", got)
	}

	// it ended while the core was down
	onDay(t, now, "2027-06-10")
	again = restarted(h, o)
	osFrom(again, ubuntu22, now)
	again.checkOSSupport()
	again.sending.Wait()
	if got := o.texts(phone); len(got) != 2 || !strings.Contains(got[1], "no longer receives") {
		t.Fatalf("sent %q, want the new state told once", got)
	}

	// and another restart tells nothing
	third := restarted(h, o)
	osFrom(third, ubuntu22, now)
	third.checkOSSupport()
	third.sending.Wait()
	if got := o.texts(phone); len(got) != 2 {
		t.Fatalf("sent %q", got)
	}
}

// What was told stays told when the box is upgraded: a Debian 12 that was ending is told again when
// it is a Debian 13 that is ending, because that is another release.
func TestAnotherReleaseIsAnotherMessage(t *testing.T) {
	h, o, now := newHub(t)
	osFrom(h, debian12, now)
	onDay(t, now, "2028-02-01")
	h.checkOSSupport()
	h.sending.Wait()
	// a Debian 13 would be told ending in 2030; the keys are what differ
	h.OSSupport = func() ossupport.Report {
		return ossupport.Report{
			Identity: ossupport.Identity{ID: "debian", VersionID: "13", Known: true},
			Support:  ossupport.Support{State: ossupport.EndingSoon, Ends: "2030-06-30", Precision: ossupport.PrecisionDay},
		}
	}
	h.checkOSSupport()
	h.sending.Wait()
	got := o.texts(phone)
	if len(got) != 2 || !strings.HasPrefix(got[0], "Debian 12 stops") || !strings.HasPrefix(got[1], "Debian 13 stops receiving Debian security updates on 30 June 2030.") {
		t.Fatalf("sent %q", got)
	}
}

// A marker file that is garbage is no state: it is told, and the file is mended.
func TestAMarkerThatIsGarbageDoesNotSilenceAState(t *testing.T) {
	h, o, now := newHub(t)
	osFrom(h, debian11, now)
	onDay(t, now, "2026-10-10")
	if err := os.WriteFile(filepath.Join(h.Root, OSSupportNotifiedFile), []byte("\x00\x00 not a state"), 0o600); err != nil {
		t.Fatal(err)
	}

	h.checkOSSupport()
	h.sending.Wait()

	if len(o.texts(phone)) != 1 || osMarker(h) != "debian-11-security_ended\n" {
		t.Fatalf("sent %q, marker %q", o.texts(phone), osMarker(h))
	}
}

// The marker is written before the message is raised: a restart in between loses the message and
// never repeats it. The clock is read by raise before it sends, and looks for the marker.
func TestTheMarkerIsWrittenBeforeTheMessageIsRaised(t *testing.T) {
	h, o, now := newHub(t)
	osFrom(h, debian11, now)
	onDay(t, now, "2026-10-10")
	clock := *now
	var seen []string
	h.Now = func() time.Time {
		seen = append(seen, osMarker(h))
		return clock
	}

	h.checkOSSupport()
	h.sending.Wait()

	if len(o.texts(phone)) != 1 || len(seen) == 0 {
		t.Fatalf("sent %q, the clock was read %d times", o.texts(phone), len(seen))
	}
	for _, marker := range seen {
		if marker != "debian-11-security_ended\n" {
			t.Fatalf("when the alert was raised the marker was %q, want it written already", marker)
		}
	}
}

// The marker is replaced, not written over: the old file, which anything holding it open still has,
// is whole until the new one is renamed over it. A second name of the old file (a hard link) shows
// it: it keeps the old state, and the path has the new one.
func TestTheMarkerIsReplacedAtomically(t *testing.T) {
	h, _, now := newHub(t)
	osFrom(h, ubuntu22, now)
	onDay(t, now, "2027-01-15")
	h.checkOSSupport()
	h.sending.Wait()

	path := filepath.Join(h.Root, OSSupportNotifiedFile)
	old := path + ".old"
	if err := os.Link(path, old); err != nil {
		t.Skipf("no hard links here: %v", err)
	}
	onDay(t, now, "2027-06-10")
	h.checkOSSupport()
	h.sending.Wait()

	if data, _ := os.ReadFile(old); string(data) != "ubuntu-22.04-ending_soon\n" {
		t.Fatalf("the old file now holds %q: the marker was written in place, not replaced", data)
	}
	if got := osMarker(h); got != "ubuntu-22.04-security_ended\n" {
		t.Fatalf("marker = %q", got)
	}
}

// Two looks at once (the daily one, and whoever else) must not both tell a state. The clock jumps a
// day at each look, so that only the state stands between them and a second message.
func TestRacingLooksSendOnce(t *testing.T) {
	h, o, _ := newHub(t)
	h.OSSupport = func() ossupport.Report {
		return ossupport.Describe(debian11, "amd64", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
	}
	var looks atomic.Int64
	base := time.Date(2026, 9, 25, 6, 0, 0, 0, time.UTC)
	h.Now = func() time.Time { return base.Add(time.Duration(looks.Add(1)) * 24 * time.Hour) }
	start := make(chan struct{})
	var all sync.WaitGroup
	for range 32 {
		all.Add(1)
		go func() {
			defer all.Done()
			<-start
			h.checkOSSupport()
		}()
	}
	close(start)
	all.Wait()
	h.sending.Wait()

	if len(o.texts(phone)) != 1 || len(o.texts(mail)) != 1 {
		t.Fatalf("sent %+v, want the state told once to each channel", o.sent)
	}
}

// The state is on record whether or not there is a channel: the owner who adds one next week is not
// sent the news of this one.
func TestAStateIsRecordedWithNoChannelToTell(t *testing.T) {
	h, o, now := newHub(t)
	saveConfig(t, h, Config{Categories: map[string]bool{Backups: true, Disks: true, Updates: true, Apps: true}, DiskThreshold: 90})
	osFrom(h, debian11, now)
	onDay(t, now, "2026-10-10")

	h.checkOSSupport()
	h.sending.Wait()
	saveConfig(t, h, Config{
		Channels:      []Channel{{ID: "a1", Name: "Phone", URL: phone}},
		Categories:    map[string]bool{Backups: true, Disks: true, Updates: true, Apps: true},
		DiskThreshold: 90,
	})
	h.checkOSSupport()
	h.sending.Wait()

	if len(o.sent) != 0 || osMarker(h) != "debian-11-security_ended\n" {
		t.Fatalf("sent %+v, marker %q", o.sent, osMarker(h))
	}
}

// With a state directory that cannot be written the state is still told, once while the core runs.
func TestAStateIsToldOnceEvenIfTheMarkerCannotBeWritten(t *testing.T) {
	h, o, now := newHub(t)
	// the marker is a directory: it can be neither read nor replaced
	if err := os.MkdirAll(filepath.Join(h.Root, OSSupportNotifiedFile), 0o755); err != nil {
		t.Fatal(err)
	}
	osFrom(h, debian11, now)
	onDay(t, now, "2026-10-10")
	// the hub's clock jumps a day at each look, so that only what the watcher remembers of the state
	// stands between it and a second message: not the six quiet hours of an alert already sent
	var looks atomic.Int64
	base := *now
	h.Now = func() time.Time { return base.Add(time.Duration(looks.Add(1)) * 24 * time.Hour) }

	h.checkOSSupport()
	h.checkOSSupport()
	h.sending.Wait()

	if len(o.texts(phone)) != 1 {
		t.Fatalf("sent %q, want the state told once", o.texts(phone))
	}
}

// An alert already sent stays quiet for six hours, but under its own key: the next state of the same
// release, a moment later, is not a repeat of the last.
func TestTheSixQuietHoursDoNotHideTheNextState(t *testing.T) {
	h, o, _ := newHub(t) // the hub's clock stays at 06:00 on 2026-09-25
	day := time.Date(2027, 5, 31, 0, 0, 0, 0, time.UTC)
	h.OSSupport = func() ossupport.Report { return ossupport.Describe(ubuntu22, "amd64", day) }

	h.checkOSSupport()
	day = day.AddDate(0, 0, 2)
	h.checkOSSupport()
	h.sending.Wait()

	got := o.texts(phone)
	if len(got) != 2 || !strings.Contains(got[0], "stops receiving") || !strings.Contains(got[1], "no longer receives") {
		t.Fatalf("sent %q, want the two states told, a moment apart", got)
	}
}

// The daily look: the first one waits, the next ones come every day, a state that changes between
// two of them is told, and the goroutine ends with its context.
func TestTheDailyLookTellsWhatChangedAndStopsWithItsContext(t *testing.T) {
	first, every := osSupportFirst, osSupportEvery
	osSupportFirst, osSupportEvery = time.Millisecond, time.Millisecond
	t.Cleanup(func() { osSupportFirst, osSupportEvery = first, every })

	h, o, _ := newHub(t)
	var looks atomic.Int32
	h.OSSupport = func() ossupport.Report {
		n := looks.Add(1)
		switch {
		case n < 3:
			return ossupport.Describe(ubuntu22, "amd64", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC))
		case n < 6:
			return ossupport.Describe(ubuntu22, "amd64", time.Date(2027, 1, 15, 0, 0, 0, 0, time.UTC))
		}
		return ossupport.Describe(ubuntu22, "amd64", time.Date(2027, 6, 10, 0, 0, 0, 0, time.UTC))
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.watchOSSupport(ctx)
		close(done)
	}()
	deadline := time.After(10 * time.Second)
	for looks.Load() < 9 {
		select {
		case <-deadline:
			t.Fatalf("only %d looks", looks.Load())
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not stop with its context")
	}
	h.sending.Wait()

	got := o.texts(phone)
	if len(got) != 2 || !strings.Contains(got[0], "stops receiving") || !strings.Contains(got[1], "no longer receives") {
		t.Fatalf("sent %q, want the two states told once each across the looks", got)
	}
}

func TestTheFirstLookWaitsAndCanBeCancelled(t *testing.T) {
	first := osSupportFirst
	osSupportFirst = time.Hour
	t.Cleanup(func() { osSupportFirst = first })

	h, _, _ := newHub(t)
	var looks atomic.Int32
	h.OSSupport = func() ossupport.Report { looks.Add(1); return ossupport.Report{} }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.watchOSSupport(ctx)
		close(done)
	}()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not stop with its context")
	}
	if looks.Load() != 0 {
		t.Fatalf("looked %d times before the first delay was over", looks.Load())
	}
}

// The hub's own Run is what starts the daily look: a box whose Debian is out of support is told
// shortly after the core starts.
func TestRunStartsTheDailyLook(t *testing.T) {
	first := osSupportFirst
	osSupportFirst = time.Millisecond
	t.Cleanup(func() { osSupportFirst = first })

	h, o, now := newHub(t)
	osFrom(h, debian11, now)
	onDay(t, now, "2026-10-10")
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		h.Run(ctx)
		close(done)
	}()
	deadline := time.After(10 * time.Second)
	for {
		o.mu.Lock()
		told := len(o.sent)
		o.mu.Unlock()
		if told > 0 {
			break
		}
		select {
		case <-deadline:
			cancel()
			t.Fatal("nothing was told")
		case <-time.After(time.Millisecond):
		}
	}
	cancel()
	<-done
	h.sending.Wait()

	if got := o.texts(phone); len(got) != 1 || !strings.HasPrefix(got[0], "Debian 11 no longer receives Debian security updates since 31 August 2026.") {
		t.Fatalf("sent %q", got)
	}
}
