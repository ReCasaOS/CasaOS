package alerts

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const (
	runA = "0123456789abcdef0123456789abcdef"
	runB = "fedcba9876543210fedcba9876543210"
)

// finished is the end of a Docker update that went well.
func finished(nonce string) DockerRun {
	return DockerRun{Nonce: nonce, Outcome: "success", To: "29.8.0"}
}

// notified is what the marker file holds, "" when there is none.
func notified(h *Hub) string {
	data, err := os.ReadFile(filepath.Join(h.Root, DockerNotifiedFile))
	if err != nil {
		return ""
	}
	return string(data)
}

// restarted is the same box after a restart of the core: the files are there, the memory is not.
func restarted(h *Hub, o *outbox) *Hub {
	again := New(h.Root)
	again.Now, again.Hostname, again.Address, again.Send = h.Now, h.Hostname, h.Address, o.send
	return again
}

// faster makes the watcher look every millisecond, at most polls times.
func faster(t *testing.T, polls int) {
	t.Helper()
	every, times := dockerPoll, dockerPolls
	dockerPoll, dockerPolls = time.Millisecond, polls
	t.Cleanup(func() { dockerPoll, dockerPolls = every, times })
}

// manyContainers are n containers named prefix1 to prefixN, with that restart policy.
func manyContainers(policy, prefix string, n int) []DockerNotReturned {
	var list []DockerNotReturned
	for i := 1; i <= n; i++ {
		list = append(list, DockerNotReturned{Name: prefix + strconv.Itoa(i), RestartPolicy: policy})
	}
	return list
}

var fourContainerEvents = []string{"app:container-died", "app:container-unhealthy", "app:container-restarting", "app:container-healthy"}

// During a Docker update, and just after, the container events of the apps are Docker
// restarting, not news; the other events are still news.
func TestContainerEventsAreDroppedWhileMuted(t *testing.T) {
	h, o, _ := newHub(t)
	muted, asked := true, 0
	h.Muted = func() bool { asked++; return muted }
	immich := map[string]string{"app:name": "immich", "docker:container:name": "immich-server"}

	for _, name := range fourContainerEvents {
		h.onEvent(name, immich)
	}
	h.sending.Wait()
	if len(o.sent) != 0 || len(h.sent) != 0 || asked != 4 {
		t.Fatalf("muted: sent %+v, on record %v, asked %d times; want nothing sent or kept, and the question asked for each of the four", o.sent, h.sent, asked)
	}

	// what is not about a container is not muted, and does not even ask
	asked = 0
	h.onEvent("app:install-error", immich)
	h.onEvent("app:update-error", immich)
	h.onEvent("backup:error", map[string]string{"app:name": "immich", "backup:destination": "offsite", "backup:kind": "backup"})
	h.sending.Wait()
	if got := len(o.texts(phone)); got != 3 || asked != 0 {
		t.Fatalf("while muted: %d sent, asked %d times; want the 3 errors and no question", got, asked)
	}

	// and the container events are heard again once it is over
	muted = false
	h.onEvent("app:container-died", immich)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 4 || !strings.HasPrefix(got[3], "immich stopped unexpectedly") {
		t.Fatalf("after the mute: sent %q", got)
	}
}

// A raise that was dropped was never sent, so there is nothing for its resolution to follow.
func TestAMutedAlertHasNoResolvedMessageLater(t *testing.T) {
	h, o, _ := newHub(t)
	muted := true
	h.Muted = func() bool { return muted }
	immich := map[string]string{"app:name": "immich", "docker:container:name": "immich-server"}

	h.onEvent("app:container-died", immich)
	muted = false
	h.onEvent("app:container-healthy", immich)
	h.sending.Wait()

	if len(o.sent) != 0 || len(h.sent) != 0 {
		t.Fatalf("sent %+v, on record %v; want nothing: the alert was never sent", o.sent, h.sent)
	}
}

// An alert sent before the update keeps its state through it: the muted events are not counted as
// repeats, and do not resolve it. Its resolution is sent once, by the first one heard after.
func TestAnAlertSentBeforeTheUpdateIsLeftAloneByIt(t *testing.T) {
	h, o, _ := newHub(t)
	muted := false
	h.Muted = func() bool { return muted }
	immich := map[string]string{"app:name": "immich", "docker:container:name": "immich-server"}
	h.onEvent("app:container-died", immich)
	h.sending.Wait()
	if len(o.texts(phone)) != 1 {
		t.Fatalf("sent %q", o.texts(phone))
	}

	muted = true
	h.onEvent("app:container-died", immich)
	h.onEvent("app:container-healthy", immich)
	h.sending.Wait()
	if r := h.sent["app:immich:runtime"]; r == nil || r.repeats != 0 || r.resolved || len(o.texts(phone)) != 1 {
		t.Fatalf("record %+v, sent %q; want it untouched and nothing new sent", r, o.texts(phone))
	}

	muted = false
	h.onEvent("app:container-healthy", immich)
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 2 || !strings.HasPrefix(got[1], "Resolved: immich runs normally again.") {
		t.Fatalf("sent %q, want the resolution once", got)
	}
}

func TestTheDockerUpdateEndsWithOneMessage(t *testing.T) {
	for name, tc := range map[string]struct {
		run  DockerRun
		want string
	}{
		"updated": {
			DockerRun{Outcome: "success", To: "29.8.0"},
			"Docker was updated to 29.8.0.",
		},
		"updated, version unknown": {
			DockerRun{Outcome: "success"},
			"Docker was updated.",
		},
		"updated, containers missing": {
			DockerRun{Outcome: "success", To: "29.8.0", NotReturned: []DockerNotReturned{{"job", "no"}, {"scratch", ""}}},
			"Docker was updated to 29.8.0. Not running again: job, scratch (they do not start by themselves).",
		},
		// a container with a restart policy was only late: Docker is still starting it, and
		// "they do not start by themselves" would be false of it
		"updated, containers late": {
			DockerRun{Outcome: "success", To: "29.8.0", NotReturned: []DockerNotReturned{{"db", "always"}, {"web", "unless-stopped"}, {"worker", "on-failure"}}},
			"Docker was updated to 29.8.0. Still starting after 90 seconds: db, web, worker (they have a restart policy and should start by themselves).",
		},
		"updated, containers missing and late": {
			DockerRun{Outcome: "success", To: "29.8.0", NotReturned: []DockerNotReturned{{"db", "always"}, {"job", "no"}, {"web", "unless-stopped"}, {"scratch", ""}}},
			"Docker was updated to 29.8.0. Not running again: job, scratch (they do not start by themselves). Still starting after 90 seconds: db, web (they have a restart policy and should start by themselves).",
		},
		"failed, containers late": {
			DockerRun{Outcome: "failed", ErrorCode: "daemon", NotReturned: []DockerNotReturned{{"db", "always"}}},
			"The Docker update failed: Docker did not come back after the update. Still starting after 90 seconds: db (they have a restart policy and should start by themselves). Details are in the dashboard.",
		},
		"restart pending": {
			DockerRun{Outcome: "restart_pending"},
			"Docker's new version is installed, but the old one still runs: restart Docker to switch (sudo systemctl restart docker, which stops every container).",
		},
		"failed, guard": {
			DockerRun{Outcome: "failed", ErrorCode: "guard"},
			"The Docker update failed: it was no longer what you confirmed, so nothing was installed. Details are in the dashboard.",
		},
		"failed, download": {
			DockerRun{Outcome: "failed", ErrorCode: "download"},
			"The Docker update failed: the packages could not be downloaded, nothing was changed. Details are in the dashboard.",
		},
		"failed, install": {
			DockerRun{Outcome: "failed", ErrorCode: "install"},
			"The Docker update failed: the packages could not be installed. Details are in the dashboard.",
		},
		"failed, daemon": {
			DockerRun{Outcome: "failed", ErrorCode: "daemon"},
			"The Docker update failed: Docker did not come back after the update. Details are in the dashboard.",
		},
		"failed, start": {
			DockerRun{Outcome: "failed", ErrorCode: "start"},
			"The Docker update failed: the update could not be started, nothing was changed. Details are in the dashboard.",
		},
		"failed, no result": {
			DockerRun{Outcome: "failed", ErrorCode: "no_result"},
			"The Docker update failed: it stopped before it reported a result. Details are in the dashboard.",
		},
		"failed, a code nobody knows": {
			DockerRun{Outcome: "failed", ErrorCode: "x\nreboot"},
			"The Docker update failed: it stopped before it reported a result. Details are in the dashboard.",
		},
		"failed, containers missing": {
			DockerRun{Outcome: "failed", ErrorCode: "daemon", NotReturned: []DockerNotReturned{{"db", "no"}}},
			"The Docker update failed: Docker did not come back after the update. Not running again: db (they do not start by themselves). Details are in the dashboard.",
		},
		"many containers missing": {
			DockerRun{Outcome: "success", To: "29.8.0", NotReturned: manyContainers("no", "c", 12)},
			"Docker was updated to 29.8.0. Not running again: c1, c2, c3, c4, c5, c6, c7, c8, c9, c10 and 2 more (they do not start by themselves).",
		},
		"many containers late": {
			DockerRun{Outcome: "success", To: "29.8.0", NotReturned: manyContainers("always", "c", 11)},
			"Docker was updated to 29.8.0. Still starting after 90 seconds: c1, c2, c3, c4, c5, c6, c7, c8, c9, c10 and 1 more (they have a restart policy and should start by themselves).",
		},
		// each list is cut on its own
		"many of both": {
			DockerRun{Outcome: "success", To: "29.8.0", NotReturned: append(manyContainers("no", "n", 11), manyContainers("always", "l", 12)...)},
			"Docker was updated to 29.8.0. Not running again: n1, n2, n3, n4, n5, n6, n7, n8, n9, n10 and 1 more (they do not start by themselves). Still starting after 90 seconds: l1, l2, l3, l4, l5, l6, l7, l8, l9, l10 and 2 more (they have a restart policy and should start by themselves).",
		},
	} {
		t.Run(name, func(t *testing.T) {
			h, o, _ := newHub(t)
			tc.run.Nonce = runA

			h.dockerEnded(tc.run)
			h.sending.Wait()

			if len(o.sent) != 2 || o.sent[0].title != "ReCasaOS · box · updates" || o.texts(phone)[0] != tc.want+"\nhttp://192.168.1.20" {
				t.Fatalf("sent %+v, want %q", o.sent, tc.want)
			}
			if _, kept := h.sent["docker-update:"+runA]; !kept {
				t.Fatalf("not on record under docker-update:%s: %v", runA, h.sent)
			}
		})
	}
}

// One message per run: a run is told once, and a second run an hour after the first is not
// taken for a repeat of it.
func TestEachRunIsToldOnceAndNotTakenForTheLastOne(t *testing.T) {
	h, o, now := newHub(t)

	h.dockerEnded(finished(runA))
	h.dockerEnded(finished(runA))
	*now = now.Add(time.Hour) // inside the six hours an alert already sent stays quiet
	h.dockerEnded(finished(runB))
	h.dockerEnded(finished(runB))
	h.sending.Wait()

	if got := o.texts(phone); len(got) != 2 {
		t.Fatalf("sent %q, want one message for each of the two runs", got)
	}
	if got := notified(h); got != runB+"\n" {
		t.Fatalf("marker = %q, want the last run's nonce", got)
	}
}

// The marker file is what survives the core: a run told before the restart is not told again,
// one that ended while the core was down is told once.
func TestARunIsToldOnceAcrossARestart(t *testing.T) {
	h, o, _ := newHub(t)
	h.dockerEnded(finished(runA))
	h.sending.Wait()
	if got := notified(h); got != runA+"\n" {
		t.Fatalf("marker = %q", got)
	}
	if entries, _ := os.ReadDir(filepath.Dir(filepath.Join(h.Root, DockerNotifiedFile))); len(entries) != 2 {
		t.Errorf("the state directory holds %v, want alerts.json and the marker only: the write leaves nothing behind", entries)
	}

	again := restarted(h, o)
	again.LastDockerRun = func() DockerRun { return finished(runA) }
	again.watchDocker(context.Background())
	again.sending.Wait()
	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("after the restart the same run was told again: %q", got)
	}

	// the run that ended while the core was down
	again = restarted(h, o)
	again.LastDockerRun = func() DockerRun { return DockerRun{Nonce: runB, Outcome: "failed", ErrorCode: "daemon"} }
	again.watchDocker(context.Background())
	again.sending.Wait()
	got := o.texts(phone)
	if len(got) != 2 || !strings.HasPrefix(got[1], "The Docker update failed: Docker did not come back") {
		t.Fatalf("sent %q, want the second run told once", got)
	}
	if notified(h) != runB+"\n" {
		t.Fatalf("marker = %q", notified(h))
	}

	// and a third restart tells nothing
	third := restarted(h, o)
	third.LastDockerRun = func() DockerRun { return DockerRun{Nonce: runB, Outcome: "failed", ErrorCode: "daemon"} }
	third.watchDocker(context.Background())
	third.sending.Wait()
	if got := o.texts(phone); len(got) != 2 {
		t.Fatalf("sent %q", got)
	}
}

// A marker file that is garbage is no nonce: the run is told, and the file is mended.
func TestAMarkerThatIsGarbageDoesNotSilenceARun(t *testing.T) {
	h, o, _ := newHub(t)
	if err := os.WriteFile(filepath.Join(h.Root, DockerNotifiedFile), []byte("\x00\x00 not a nonce"), 0o600); err != nil {
		t.Fatal(err)
	}

	h.dockerEnded(finished(runA))
	h.sending.Wait()

	if len(o.texts(phone)) != 1 || notified(h) != runA+"\n" {
		t.Fatalf("sent %q, marker %q", o.texts(phone), notified(h))
	}
}

// The two goroutines that can look at the end of a run (the one from the core's start and the
// one from the button) must not both tell it.
func TestRacingNotifiersSendOnce(t *testing.T) {
	h, o, now := newHub(t)
	// The hub's own six quiet hours for an alert already sent would hide a second send: the clock
	// jumps a day at each look, so that only the nonce stands between the goroutines.
	var looks atomic.Int64
	h.Now = func() time.Time { return now.Add(time.Duration(looks.Add(1)) * 24 * time.Hour) }
	start := make(chan struct{})
	var all sync.WaitGroup
	for range 32 {
		all.Add(1)
		go func() {
			defer all.Done()
			<-start
			h.dockerEnded(finished(runA))
		}()
	}
	close(start)
	all.Wait()
	h.sending.Wait()

	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("sent %q, want the run told once", got)
	}
	if got := o.texts(mail); len(got) != 1 {
		t.Fatalf("the mail box got %q", got)
	}
}

func TestRacingWatchersSendOnce(t *testing.T) {
	faster(t, 1000)
	h, o, _ := newHub(t)
	var polls atomic.Int32
	h.LastDockerRun = func() DockerRun {
		if polls.Add(1) < 20 {
			return DockerRun{Nonce: runA}
		}
		return finished(runA)
	}
	var all sync.WaitGroup
	for range 4 {
		all.Add(1)
		go func() {
			defer all.Done()
			h.watchDocker(context.Background())
		}()
	}
	all.Wait()
	h.sending.Wait()

	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("sent %q, want the run told once", got)
	}
}

func TestTheWatcherWaitsForTheEndOfTheRun(t *testing.T) {
	faster(t, 1000)
	h, o, _ := newHub(t)
	var polls atomic.Int32
	h.LastDockerRun = func() DockerRun {
		if polls.Add(1) < 3 {
			return DockerRun{Nonce: runA} // running
		}
		return finished(runA)
	}

	h.watchDocker(context.Background())
	h.sending.Wait()

	if polls.Load() != 3 || len(o.texts(phone)) != 1 {
		t.Fatalf("looked %d times, sent %q", polls.Load(), o.texts(phone))
	}
}

func TestTheWatcherGivesUpWhenTheRunNeverEnds(t *testing.T) {
	faster(t, 5)
	h, o, _ := newHub(t)
	var polls atomic.Int32
	h.LastDockerRun = func() DockerRun { polls.Add(1); return DockerRun{Nonce: runA} }

	h.watchDocker(context.Background())
	h.sending.Wait()

	if polls.Load() != 5 || len(o.sent) != 0 || notified(h) != "" {
		t.Fatalf("looked %d times, sent %+v, marker %q", polls.Load(), o.sent, notified(h))
	}
}

func TestTheWatcherStopsWithTheHub(t *testing.T) {
	faster(t, 10)
	dockerPoll = time.Hour // faster's cleanup puts it back
	h, _, _ := newHub(t)
	h.LastDockerRun = func() DockerRun { return DockerRun{Nonce: runA} }
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.watchDocker(ctx); close(stopped) }()

	cancel()

	select {
	case <-stopped:
	case <-time.After(10 * time.Second):
		t.Fatal("the watcher did not stop")
	}
}

func TestTheWatcherHasNothingToWaitForWithoutARun(t *testing.T) {
	faster(t, 1000)
	h, o, _ := newHub(t)
	var polls atomic.Int32
	h.LastDockerRun = func() DockerRun { polls.Add(1); return DockerRun{} }

	h.watchDocker(context.Background())
	h.sending.Wait()

	if polls.Load() != 1 || len(o.sent) != 0 {
		t.Fatalf("looked %d times, sent %+v", polls.Load(), o.sent)
	}
	// the default hub knows of no run at all
	if got := New(t.TempDir()).LastDockerRun(); !reflect.DeepEqual(got, DockerRun{}) {
		t.Fatalf("default run = %+v", got)
	}
}

// Whether or not a channel could be told, the run is told once: a channel added next week is not
// sent the news of a run of last week.
func TestARunNobodyCouldBeToldIsStillMarked(t *testing.T) {
	h, o, _ := newHub(t)
	channels := h.load()
	saveConfig(t, h, defaults())

	h.dockerEnded(finished(runA))
	saveConfig(t, h, channels)
	h.dockerEnded(finished(runA))
	h.sending.Wait()

	if len(o.sent) != 0 || notified(h) != runA+"\n" {
		t.Fatalf("sent %+v, marker %q", o.sent, notified(h))
	}
}

// The updates category is the owner's switch for this message too.
func TestTheDockerMessageFollowsTheUpdatesCategory(t *testing.T) {
	h, o, _ := newHub(t)
	c := h.load()
	c.Categories[Updates] = false
	saveConfig(t, h, c)

	h.dockerEnded(finished(runA))
	h.sending.Wait()

	if len(o.sent) != 0 {
		t.Fatalf("sent %+v with updates off", o.sent)
	}
}

// Run looks at the last run when the core starts, so a run that ended while the core was down is
// told even if nobody opens the dashboard.
func TestRunTellsARunThatEndedWhileTheCoreWasDown(t *testing.T) {
	faster(t, 10)
	h, o, _ := newHub(t)
	h.LastDockerRun = func() DockerRun { return finished(runA) }
	ctx, cancel := context.WithCancel(context.Background())
	stopped := make(chan struct{})
	go func() { h.Run(ctx); close(stopped) }()
	defer func() {
		cancel()
		<-stopped
	}()

	deadline := time.Now().Add(10 * time.Second)
	for len(o.texts(phone)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := o.texts(phone); len(got) != 1 || !strings.HasPrefix(got[0], "Docker was updated to 29.8.0.") {
		t.Fatalf("sent %q", got)
	}
}

// WatchDockerUpdate is the button's hook: it returns at once and tells the run when it ends.
func TestWatchDockerUpdateReturnsAtOnceAndTellsTheEnd(t *testing.T) {
	faster(t, 1000)
	h, o, _ := newHub(t)
	end := make(chan struct{})
	h.LastDockerRun = func() DockerRun {
		select {
		case <-end:
			return finished(runB)
		default:
			return DockerRun{Nonce: runB}
		}
	}

	h.WatchDockerUpdate()
	close(end)

	deadline := time.Now().Add(10 * time.Second)
	for len(o.texts(phone)) == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	h.sending.Wait()
	if got := o.texts(phone); len(got) != 1 {
		t.Fatalf("sent %q", got)
	}
}
