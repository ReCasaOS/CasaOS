package dockerpkg

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

const (
	testNonce  = "0f1e2d3c4b5a69788796a5b4c3d2e1f0"
	otherNonce = "ffffffffffffffffffffffffffffffff"
)

var (
	t0 = time.Date(2026, 10, 9, 10, 0, 0, 0, time.UTC)
	ts = func(seconds int) string { return t0.Add(time.Duration(seconds) * time.Second).Format(time.RFC3339) }
)

func marker(kind, fields string) string {
	line := "CASAOS_DOCKER_UPDATE_" + kind + " " + testNonce
	if fields != "" {
		line += " " + fields
	}
	return line
}

func logOf(lines ...string) string { return strings.Join(lines, "\n") + "\n" }

func TestQueuedMarker(t *testing.T) {
	if got, want := QueuedMarker(testNonce, t0), "CASAOS_DOCKER_UPDATE_QUEUED "+testNonce+" 2026-10-09T10:00:00Z\n"; got != want {
		t.Errorf("QueuedMarker() = %q, want %q", got, want)
	}
	// always UTC, whatever the clock's zone
	zone := time.FixedZone("CEST", 2*60*60)
	if got, want := QueuedMarker(testNonce, time.Date(2026, 10, 9, 12, 0, 0, 0, zone)), "CASAOS_DOCKER_UPDATE_QUEUED "+testNonce+" 2026-10-09T10:00:00Z\n"; got != want {
		t.Errorf("QueuedMarker() in another zone = %q, want %q", got, want)
	}
	// and what the parser takes as its first line
	run := ParseRun(QueuedMarker(testNonce, t0))
	if run.Nonce != testNonce || !run.QueuedAt.Equal(t0) {
		t.Errorf("ParseRun(QueuedMarker()) = %#v", run)
	}
}

func TestValidNonce(t *testing.T) {
	for _, s := range []string{testNonce, otherNonce, strings.Repeat("0", 32), strings.Repeat("a", 32), "0123456789abcdef0123456789abcdef"} {
		if !ValidNonce(s) {
			t.Errorf("ValidNonce(%q) = false", s)
		}
	}
	for _, s := range []string{"", strings.Repeat("a", 31), strings.Repeat("a", 33), strings.ToUpper(testNonce), strings.Repeat("g", 32), strings.Repeat("a", 31) + " ", strings.Repeat("a", 31) + "\n", " " + strings.Repeat("a", 31), "0x" + strings.Repeat("a", 30), strings.Repeat("a", 31) + "\u00e9", strings.Repeat("a", 16) + "\x00" + strings.Repeat("a", 15)} {
		if ValidNonce(s) {
			t.Errorf("ValidNonce(%q) = true", s)
		}
	}
}

func TestNewNonce(t *testing.T) {
	a, err := NewNonce()
	if err != nil || !ValidNonce(a) {
		t.Fatalf("NewNonce() = %q, %v", a, err)
	}
	if b, _ := NewNonce(); a == b {
		t.Errorf("two nonces are both %q", a)
	}
}

func TestParseRunSuccess(t *testing.T) {
	log := logOf(
		strings.TrimSuffix(QueuedMarker(testNonce, t0), "\n"),
		marker("STARTED", ts(1)),
		marker("PREVIOUS", "containerd.io=1.7.27-1 docker-ce=5:28.0.4-1~debian.11~bullseye docker-ce-cli=5:28.0.4-1~debian.11~bullseye"),
		"container: web always",
		"container: db unless-stopped",
		"container: cache no",
		"Reading package lists...",
		"Get:1 https://download.docker.com/linux/debian bullseye/stable amd64 docker-ce amd64 5:29.8.0-1~debian.11~bullseye [20.5 MB]",
		marker("DOWNLOADED", ts(60)),
		"Preparing to unpack .../docker-ce_5%3a29.8.0-1~debian.11~bullseye_amd64.deb ...",
		marker("INSTALLED", ts(90)),
		marker("DAEMON", "29.8.0"),
		marker("NOTRETURNED", "cache no"),
		marker("SUCCESS", ts(100)),
	)
	want := Run{
		Nonce: testNonce, QueuedAt: t0, StartedAt: t0.Add(1 * time.Second), DownloadedAt: t0.Add(60 * time.Second), InstalledAt: t0.Add(90 * time.Second), CompletedAt: t0.Add(100 * time.Second),
		Terminal: TerminalSuccess, DaemonVersion: "29.8.0", PreviousSeen: true,
		Previous:    []string{"containerd.io=1.7.27-1", "docker-ce=5:28.0.4-1~debian.11~bullseye", "docker-ce-cli=5:28.0.4-1~debian.11~bullseye"},
		NotReturned: []NotReturned{{Name: "cache", RestartPolicy: "no"}},
	}
	got := ParseRun(log)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("ParseRun() =\n%#v\nwant\n%#v", got, want)
	}
}

func TestParseRunTerminalMarkers(t *testing.T) {
	head := []string{QueuedMarker(testNonce, t0)[:len(QueuedMarker(testNonce, t0))-1], marker("STARTED", ts(1))}
	cases := []struct {
		name         string
		tail         []string
		terminal     string
		reason       string
		completedSec int
	}{
		{"nothing yet", nil, "", "", 0},
		{"success", []string{marker("SUCCESS", ts(10))}, TerminalSuccess, "", 10},
		{"restart pending", []string{marker("DAEMON", "28.0.4"), marker("RESTART_PENDING", ts(11))}, TerminalRestartPending, "", 11},
		{"failed guard", []string{marker("GUARD", "plan"), marker("FAILED", ts(12)+" guard")}, TerminalFailed, FailGuard, 12},
		{"failed download", []string{marker("FAILED", ts(13)+" download")}, TerminalFailed, FailDownload, 13},
		{"failed install", []string{marker("FAILED", ts(14)+" install")}, TerminalFailed, FailInstall, 14},
		{"failed daemon", []string{marker("FAILED", ts(15)+" daemon")}, TerminalFailed, FailDaemon, 15},
		{"failed start (the core's word, for a unit systemd did not start)", []string{marker("FAILED", ts(16)+" start")}, TerminalFailed, FailStart, 16},
		{"the last terminal marker wins (failed after success)", []string{marker("SUCCESS", ts(10)), marker("FAILED", ts(20)+" daemon")}, TerminalFailed, FailDaemon, 20},
		{"the last terminal marker wins (success after failed)", []string{marker("FAILED", ts(10)+" install"), marker("SUCCESS", ts(21))}, TerminalSuccess, "", 21},
		{"a failure with no reason is not a marker", []string{marker("FAILED", ts(10))}, "", "", 0},
		{"a failure with an unknown reason is not a marker", []string{marker("FAILED", ts(10)+" reboot")}, "", "", 0},
		{"a failure with a hostile reason is not a marker", []string{marker("FAILED", ts(10)+" install;reboot")}, "", "", 0},
		{"a failure with a second reason is not a marker", []string{marker("FAILED", ts(10)+" install daemon")}, "", "", 0},
		{"a success with no time is not a marker", []string{marker("SUCCESS", "")}, "", "", 0},
		{"a success with a bad time is not a marker", []string{marker("SUCCESS", "yesterday"), marker("SUCCESS", "2026-10-09 10:00:00"), marker("SUCCESS", "2026-13-09T10:00:00Z")}, "", "", 0},
		{"a success with a field too many is not a marker", []string{marker("SUCCESS", ts(10)+" extra")}, "", "", 0},
		{"a bad terminal marker leaves the good one before it", []string{marker("SUCCESS", ts(10)), marker("FAILED", ts(11)+" nonsense")}, TerminalSuccess, "", 10},
		{"a time in another zone is UTC", []string{marker("SUCCESS", "2026-10-09T12:00:30+02:00")}, TerminalSuccess, "", 30},
	}
	for _, c := range cases {
		got := ParseRun(logOf(append(append([]string{}, head...), c.tail...)...))
		if got.Nonce != testNonce || got.Terminal != c.terminal || got.FailReason != c.reason {
			t.Errorf("%s: Terminal = %q, FailReason = %q (nonce %q), want %q, %q", c.name, got.Terminal, got.FailReason, got.Nonce, c.terminal, c.reason)
		}
		wantCompleted := time.Time{}
		if c.terminal != "" {
			wantCompleted = t0.Add(time.Duration(c.completedSec) * time.Second)
		}
		if !got.CompletedAt.Equal(wantCompleted) || got.CompletedAt.Location() != time.UTC && !got.CompletedAt.IsZero() {
			t.Errorf("%s: CompletedAt = %v, want %v", c.name, got.CompletedAt, wantCompleted)
		}
	}
}

func TestParseRunNeedsTheFirstLine(t *testing.T) {
	good := marker("QUEUED", ts(0))
	later := []string{marker("STARTED", ts(1)), marker("DAEMON", "29.8.0"), marker("SUCCESS", ts(5))}
	for name, first := range map[string]string{
		"nothing":                 "",
		"apt output":              "Reading package lists...",
		"the truncation notice":   "[Earlier log output omitted]",
		"a started marker":        marker("STARTED", ts(0)),
		"a leading space":         " " + good,
		"a leading tab":           "\t" + good,
		"a byte order mark":       "\ufeff" + good,
		"a short nonce":           "CASAOS_DOCKER_UPDATE_QUEUED " + testNonce[:31] + " " + ts(0),
		"a long nonce":            "CASAOS_DOCKER_UPDATE_QUEUED " + testNonce + "0 " + ts(0),
		"an upper case nonce":     "CASAOS_DOCKER_UPDATE_QUEUED " + strings.ToUpper(testNonce) + " " + ts(0),
		"a nonce that is not hex": "CASAOS_DOCKER_UPDATE_QUEUED " + strings.Repeat("z", 32) + " " + ts(0),
		"no time":                 "CASAOS_DOCKER_UPDATE_QUEUED " + testNonce,
		"a bad time":              "CASAOS_DOCKER_UPDATE_QUEUED " + testNonce + " soon",
		"a field too many":        good + " extra",
		"another prefix":          "CASAOS_PACKAGE_UPDATE_QUEUED " + testNonce + " " + ts(0),
		"two spaces":              "CASAOS_DOCKER_UPDATE_QUEUED  " + testNonce + " " + ts(0),
		"lower case":              strings.ToLower(good),
	} {
		got := ParseRun(logOf(append([]string{first}, later...)...))
		if !reflect.DeepEqual(got, Run{}) {
			t.Errorf("%s: ParseRun() = %#v, want the zero Run", name, got)
		}
	}
	// the first line is the first line: a good marker on the second line does not count
	if got := ParseRun(logOf(append([]string{"Reading package lists...", good}, later...)...)); !reflect.DeepEqual(got, Run{}) {
		t.Errorf("a QUEUED line that is not first gave %#v", got)
	}
	if got := ParseRun(""); !reflect.DeepEqual(got, Run{}) {
		t.Errorf("ParseRun(\"\") = %#v", got)
	}
	// a first line with no new line yet, or with a carriage return, is the first line
	if got := ParseRun(good); got.Nonce != testNonce || got.Terminal != "" {
		t.Errorf("ParseRun(first line only) = %#v", got)
	}
	if got := ParseRun(good + "\r\n" + marker("SUCCESS", ts(5)) + "\r\n"); got.Nonce != testNonce || got.Terminal != TerminalSuccess {
		t.Errorf("ParseRun(CRLF log) = %#v", got)
	}
	// the one nonce is the one on line one: a second QUEUED line changes nothing
	other := "CASAOS_DOCKER_UPDATE_QUEUED " + otherNonce + " " + ts(50)
	got := ParseRun(logOf(good, other, marker("QUEUED", ts(60)), marker("SUCCESS", ts(70))))
	if got.Nonce != testNonce || !got.QueuedAt.Equal(t0) || got.Terminal != TerminalSuccess {
		t.Errorf("a second QUEUED line changed the run: %#v", got)
	}
}

func TestParseRunIgnoresForgedAndHostileLines(t *testing.T) {
	forged := func(kind, fields string) string {
		return "CASAOS_DOCKER_UPDATE_" + kind + " " + otherNonce + " " + fields
	}
	log := logOf(
		strings.TrimSuffix(QueuedMarker(testNonce, t0), "\n"),
		marker("STARTED", ts(1)),
		// a container named like a marker, in the unit's own snapshot lines
		"container: CASAOS_DOCKER_UPDATE_SUCCESS always",
		"container: CASAOS_DOCKER_UPDATE_FAILED "+ts(2)+" install no",
		// the right kind with the wrong nonce
		forged("SUCCESS", ts(3)),
		forged("FAILED", ts(3)+" install"),
		forged("DAEMON", "6.6.6"),
		forged("PREVIOUS", "docker-ce=5:1.0"),
		forged("NOTRETURNED", "forged no"),
		"CASAOS_DOCKER_UPDATE_SUCCESS "+testNonce[:31]+" "+ts(3),
		"CASAOS_DOCKER_UPDATE_SUCCESS "+testNonce+"0 "+ts(3),
		"CASAOS_DOCKER_UPDATE_SUCCESS "+strings.ToUpper(testNonce)+" "+ts(3),
		// no nonce at all
		"CASAOS_DOCKER_UPDATE_SUCCESS",
		"CASAOS_DOCKER_UPDATE_SUCCESS "+ts(3),
		// the right nonce, in the middle of apt or dpkg output
		"Setting up docker-ce (5:29.8.0-1~debian.11~bullseye) ... "+marker("SUCCESS", ts(4)),
		"Processing triggers for man-db ...  "+marker("FAILED", ts(4)+" install"),
		"x"+marker("SUCCESS", ts(4)),
		" "+marker("SUCCESS", ts(4)),
		"\t"+marker("SUCCESS", ts(4)),
		"| "+marker("SUCCESS", ts(4)),
		"> "+marker("FAILED", ts(4)+" daemon"),
		"container: "+marker("SUCCESS", ts(4)),
		// the wrong prefix, case, kind or spacing
		strings.ToLower(marker("SUCCESS", ts(5))),
		"CASAOS_DOCKER_UPDATE_SUCCESSX "+testNonce+" "+ts(5),
		"CASAOS_DOCKER_UPDATE_SUCCES "+testNonce+" "+ts(5),
		"CASAOS_DOCKER_UPDATE_ "+testNonce+" "+ts(5),
		"CASAOS_DOCKER_UPDATE_BOGUS "+testNonce+" "+ts(5),
		"CASAOS_DOCKER_UPDATE_SUCCESS\t"+testNonce+" "+ts(5),
		"CASAOS_DOCKER_UPDATE_SUCCESS  "+testNonce+" "+ts(5),
		"CASAOS_DOCKER_UPDATE_SUCCESS"+testNonce+" "+ts(5),
		"CASAOS_PACKAGE_UPDATE_SUCCESS "+testNonce+" "+ts(5),
		"CASAOS_DOCKER_UPDATE_SUCCESS "+testNonce, // no time
		"Reading package lists... Done",
	)
	got := ParseRun(log)
	want := Run{Nonce: testNonce, QueuedAt: t0, StartedAt: t0.Add(time.Second)}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("hostile lines changed the run:\n%#v\nwant\n%#v", got, want)
	}
	// and a good marker after all of that still counts
	got = ParseRun(log + marker("SUCCESS", ts(9)) + "\n")
	if got.Terminal != TerminalSuccess || !got.CompletedAt.Equal(t0.Add(9*time.Second)) {
		t.Errorf("the genuine marker after hostile lines gave %#v", got)
	}
}

func TestParseRunAContainerNamedLikeAMarkerIsOnlyAName(t *testing.T) {
	log := logOf(
		strings.TrimSuffix(QueuedMarker(testNonce, t0), "\n"),
		marker("NOTRETURNED", "CASAOS_DOCKER_UPDATE_SUCCESS no"),
		marker("NOTRETURNED", "CASAOS_DOCKER_UPDATE_FAILED always"),
	)
	got := ParseRun(log)
	want := []NotReturned{{Name: "CASAOS_DOCKER_UPDATE_SUCCESS", RestartPolicy: "no"}, {Name: "CASAOS_DOCKER_UPDATE_FAILED", RestartPolicy: "always"}}
	if got.Terminal != "" || !reflect.DeepEqual(got.NotReturned, want) {
		t.Errorf("ParseRun() = %#v", got)
	}
}

func TestParseRunValidatesPrevious(t *testing.T) {
	long := func(n int) string { return strings.Repeat("a", n) }
	fields := []string{
		"containerd.io=1.7.27-1",     // good
		"docker-ce;rm=5:1",           // a shell character in the name
		"docker-ce=5:1;reboot",       // ... in the version
		"docker-ce-cli=5:1`id`",      // a backtick
		"docker-buildx-plugin=$(id)", // a substitution
		"evil=1.0",                   // a name that is not one of the seven
		"libc6=2.31-13",              // a good name that is not the engine
		"docker.io=26.1.5",           // the distribution's package
		"docker-ce=5:1=2",            // two equals
		"docker-ce",                  // no version
		"docker-ce=",                 // an empty version
		"=1.0",                       // an empty name
		"docker-model-plugin=" + strings.Repeat("1", 101), // overlong version
		long(129) + "=1.0",               // overlong name
		"-oAPT::Foo=1",                   // an option
		"docker-ce:amd64;reboot=5:1",     // an architecture that is a command
		"containerd.io:$(id)=1.0",        // ... or a substitution
		"DOCKER-CE=5:1",                  // upper case
		"docker-ce=1\u00e9",              // not ASCII
		"docker-compose-plugin=2.34.0-1", // good
		"containerd.io=1.7.27-1",         // the same, twice
		"docker-ce:amd64=5:28.0.4-1",     // good, with an architecture
	}
	got := ParseRun(logOf(QueuedMarker(testNonce, t0)[:len(QueuedMarker(testNonce, t0))-1], marker("PREVIOUS", strings.Join(fields, " ")))).Previous
	want := []string{"containerd.io=1.7.27-1", "docker-compose-plugin=2.34.0-1", "docker-ce:amd64=5:28.0.4-1"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("Previous = %#v, want %#v", got, want)
	}
	// nothing valid, or nothing at all: no list
	for _, only := range []string{"", "evil=1.0 docker-ce;x=1"} {
		got := ParseRun(logOf(QueuedMarker(testNonce, t0)[:len(QueuedMarker(testNonce, t0))-1], marker("PREVIOUS", only))).Previous
		if len(got) != 0 {
			t.Errorf("Previous of %q = %#v", only, got)
		}
	}
	// the first snapshot is the snapshot
	got = ParseRun(logOf(QueuedMarker(testNonce, t0)[:len(QueuedMarker(testNonce, t0))-1], marker("PREVIOUS", "docker-ce=5:28.0.4-1"), marker("PREVIOUS", "docker-ce=5:29.0.0-1"))).Previous
	if !reflect.DeepEqual(got, []string{"docker-ce=5:28.0.4-1"}) {
		t.Errorf("a second PREVIOUS marker gave %#v", got)
	}
}

func TestParseRunValidatesDaemonVersion(t *testing.T) {
	first := QueuedMarker(testNonce, t0)[:len(QueuedMarker(testNonce, t0))-1]
	for fields, want := range map[string]string{
		"29.8.0": "29.8.0", "28.0.4": "28.0.4", "25.0.0-rc.1": "25.0.0-rc.1", "20.10.24+dfsg1": "20.10.24+dfsg1",
		"": "", "29.8.0;reboot": "", "29.8.0 extra": "", "$(id)": "", "`id`": "", "-1": "", "29/8": "", "29.8.0\r": "29.8.0",
		strings.Repeat("1", 101): "", "1é": "",
	} {
		if got := ParseRun(logOf(first, marker("DAEMON", fields))).DaemonVersion; got != want {
			t.Errorf("DaemonVersion of %q = %q, want %q", fields, got, want)
		}
	}
	// the last good one is the daemon that is running now; a bad one does not replace it
	got := ParseRun(logOf(first, marker("DAEMON", "28.0.4"), marker("DAEMON", "29.8.0"), marker("DAEMON", "29.8.0;x"))).DaemonVersion
	if got != "29.8.0" {
		t.Errorf("DaemonVersion = %q", got)
	}
}

func TestParseRunValidatesNotReturned(t *testing.T) {
	first := QueuedMarker(testNonce, t0)[:len(QueuedMarker(testNonce, t0))-1]
	long := strings.Repeat("a", 129)
	cases := []struct {
		fields string
		want   []NotReturned
	}{
		{"cache no", []NotReturned{{"cache", "no"}}},
		{"db unless-stopped", []NotReturned{{"db", "unless-stopped"}}},
		{"my_app-1.2 on-failure", []NotReturned{{"my_app-1.2", "on-failure"}}},
		{"lonely", []NotReturned{{"lonely", ""}}},    // a container with no policy at all
		{"lonely2 ", []NotReturned{{"lonely2", ""}}}, // ... printed with its trailing space
		{strings.Repeat("a", 128) + " no", []NotReturned{{strings.Repeat("a", 128), "no"}}},
		{"", nil},
		{"a b always", nil}, // a name with a space is two fields and a policy
		{"a;b no", nil},     // shell characters
		{"$(id) no", nil},
		{"`id` no", nil},
		{"a|b no", nil},
		{"a&b no", nil},
		{"a>b no", nil},
		{"-dash no", nil}, // not a container name
		{".dot no", nil},
		{long + " no", nil}, // overlong
		{"web No", nil},     // upper case policy
		{"web always;x", nil},
		{"web " + strings.Repeat("a", 33), nil},
		{"web 1", nil},
		{"web no extra", nil}, // a field too many
		{"wéb no", nil},
	}
	for _, c := range cases {
		got := ParseRun(logOf(first, marker("NOTRETURNED", c.fields))).NotReturned
		if !reflect.DeepEqual(got, c.want) && !(len(got) == 0 && len(c.want) == 0) {
			t.Errorf("NotReturned of %q = %#v, want %#v", c.fields, got, c.want)
		}
	}
	// several, in the order they were printed
	got := ParseRun(logOf(first, marker("NOTRETURNED", "b no"), marker("NOTRETURNED", "bad;x no"), marker("NOTRETURNED", "a always"))).NotReturned
	if want := []NotReturned{{"b", "no"}, {"a", "always"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("NotReturned = %#v, want %#v", got, want)
	}
}

func TestParseRunRunningAndSkippedSteps(t *testing.T) {
	first := QueuedMarker(testNonce, t0)[:len(QueuedMarker(testNonce, t0))-1]
	// queued, not started
	if got := ParseRun(first); got.Nonce != testNonce || !got.QueuedAt.Equal(t0) || !got.StartedAt.IsZero() || got.Terminal != "" || !got.CompletedAt.IsZero() {
		t.Errorf("a queued run = %#v", got)
	}
	// installed, but not finished
	got := ParseRun(logOf(first, marker("STARTED", ts(1)), marker("INSTALLED", ts(30))))
	if got.Terminal != "" || !got.InstalledAt.Equal(t0.Add(30*time.Second)) {
		t.Errorf("an installed run = %#v", got)
	}
	// a failed run that never installed has no InstalledAt
	got = ParseRun(logOf(first, marker("STARTED", ts(1)), marker("GUARD", "plan"), marker("FAILED", ts(2)+" guard")))
	if got.Terminal != TerminalFailed || !got.InstalledAt.IsZero() {
		t.Errorf("a refused run = %#v", got)
	}
	// the first STARTED is the start
	got = ParseRun(logOf(first, marker("STARTED", ts(1)), marker("STARTED", ts(9))))
	if !got.StartedAt.Equal(t0.Add(time.Second)) {
		t.Errorf("StartedAt = %v", got.StartedAt)
	}
}

// The page tells the owner what the apps are going through from the phase: the markers the unit
// has written say where it is.
func TestRunPhase(t *testing.T) {
	first := QueuedMarker(testNonce, t0)[:len(QueuedMarker(testNonce, t0))-1]
	started := marker("STARTED", ts(1))
	previous := marker("PREVIOUS", "docker-ce=5:28.0.4-1")
	downloaded := marker("DOWNLOADED", ts(60))
	installed := marker("INSTALLED", ts(90))
	daemon := marker("DAEMON", "29.8.0")
	forged := func(kind, fields string) string {
		return "CASAOS_DOCKER_UPDATE_" + kind + " " + otherNonce + " " + fields
	}
	for _, c := range []struct {
		name  string
		lines []string
		want  string
	}{
		{"queued, the unit has not written", []string{first}, PhasePreparing},
		{"started", []string{first, started}, PhasePreparing},
		{"the snapshot is taken", []string{first, started, previous}, PhaseDownloading},
		{"a snapshot of nothing is a snapshot", []string{first, started, marker("PREVIOUS", "")}, PhaseDownloading},
		{"the containers are listed, the guard is on", []string{first, started, previous, "container: web always", "Inst docker-ce [5:28.0.4-1] (5:29.8.0-1 Docker CE:bullseye [amd64])"}, PhaseDownloading},
		{"downloaded", []string{first, started, previous, downloaded}, PhaseInstalling},
		{"installed", []string{first, started, previous, downloaded, installed}, PhaseWaitingDocker},
		{"the daemon is back", []string{first, started, previous, downloaded, installed, daemon}, PhaseWaitingContainers},
		{"and some containers are not", []string{first, started, previous, downloaded, installed, daemon, marker("NOTRETURNED", "db always")}, PhaseWaitingContainers},
		{"success", []string{first, started, previous, downloaded, installed, daemon, marker("SUCCESS", ts(100))}, ""},
		{"restart pending", []string{first, started, previous, downloaded, installed, marker("DAEMON", "28.0.4"), marker("RESTART_PENDING", ts(100))}, ""},
		{"failed in the guard", []string{first, started, previous, marker("GUARD", "plan"), marker("FAILED", ts(5)+" guard")}, ""},
		{"failed in the install", []string{first, started, previous, downloaded, marker("FAILED", ts(70)+" install")}, ""},
		{"the unit could not be started", []string{first, marker("FAILED", ts(1)+" start")}, ""},
		// the unit brings Docker back after a failed install, before it says so: still the install
		{"the install is failing", []string{first, started, previous, downloaded, "E: Sub-process /usr/bin/dpkg returned an error code (1)"}, PhaseInstalling},
		// a marker that is not the unit's moves nothing
		{"another nonce", []string{first, started, previous, forged("DOWNLOADED", ts(60)), forged("INSTALLED", ts(90)), forged("DAEMON", "29.8.0")}, PhaseDownloading},
		{"in the middle of a line", []string{first, started, previous, "dpkg: " + downloaded}, PhaseDownloading},
		{"a daemon that is not a version", []string{first, started, previous, downloaded, installed, marker("DAEMON", "29.8.0;reboot")}, PhaseWaitingDocker},
		{"a time that is not one", []string{first, started, previous, marker("DOWNLOADED", "soon")}, PhaseDownloading},
	} {
		if got := ParseRun(logOf(c.lines...)).Phase(); got != c.want {
			t.Errorf("%s: Phase() = %q, want %q", c.name, got, c.want)
		}
	}
	// no run is no phase
	if got := (Run{}).Phase(); got != "" {
		t.Errorf("the zero Run is in phase %q", got)
	}
	if got := ParseRun(logOf("Reading package lists...", started, previous)).Phase(); got != "" {
		t.Errorf("a log that is not a run is in phase %q", got)
	}
}

func TestRollbackCommand(t *testing.T) {
	got := RollbackCommand([]string{"docker-ce=5:28.0.4-1~debian.11~bullseye", "containerd.io=1.7.27-1", "docker-ce-cli=5:28.0.4-1~debian.11~bullseye"})
	want := "sudo apt-get install --allow-downgrades docker-ce=5:28.0.4-1~debian.11~bullseye containerd.io=1.7.27-1 docker-ce-cli=5:28.0.4-1~debian.11~bullseye"
	if got != want {
		t.Errorf("RollbackCommand() = %q, want %q", got, want)
	}
	for _, none := range [][]string{nil, {}, {""}} {
		if got := RollbackCommand(none); got != "" {
			t.Errorf("RollbackCommand(%q) = %q, want none", none, got)
		}
	}
	hostile := []string{
		"docker-ce;reboot=5:1", "docker-ce=5:1;reboot", "docker-ce=5:1`id`", "docker-ce=$(id)", "docker-ce=5:1 && reboot", "docker-ce=5:1\nreboot",
		"docker-ce=5:1|id", "docker-ce=5:1>x", "docker-ce='5:1'", `docker-ce="5:1"`, "docker-ce=5:1\\", "docker-ce=5:1#", "docker-ce=5:1&",
		"docker-ce", "docker-ce=", "=5:1", "=", "", "docker-ce=5:1=2", "libc6=2.31", "docker.io=26.1.5", "-oAPT::Foo=1", "--allow-remove-essential=1",
		"DOCKER-CE=5:1", strings.Repeat("a", 129) + "=1", "docker-ce=" + strings.Repeat("1", 101),
		"docker-ce:amd64;reboot=5:1", "containerd.io:$(id)=1.0",
	}
	if got := RollbackCommand(hostile); got != "" {
		t.Errorf("RollbackCommand(hostile) = %q, want none", got)
	}
	// among good ones, only the good ones
	got = RollbackCommand(append(append([]string{"docker-ce=5:28.0.4-1"}, hostile...), "containerd.io=1.7.27-1"))
	if want := "sudo apt-get install --allow-downgrades docker-ce=5:28.0.4-1 containerd.io=1.7.27-1"; got != want {
		t.Errorf("RollbackCommand(mixed) = %q, want %q", got, want)
	}
	if strings.ContainsAny(got, ";&|$`'\"\\<>()\n#*?!{}") {
		t.Errorf("the command holds a shell character: %q", got)
	}
}

func TestRollbackCommandFromARun(t *testing.T) {
	first := QueuedMarker(testNonce, t0)[:len(QueuedMarker(testNonce, t0))-1]
	run := ParseRun(logOf(first, marker("PREVIOUS", "docker-ce=5:28.0.4-1 evil;x=1 containerd.io=1.7.27-1"), marker("FAILED", ts(5)+" daemon")))
	if got, want := RollbackCommand(run.Previous), "sudo apt-get install --allow-downgrades docker-ce=5:28.0.4-1 containerd.io=1.7.27-1"; got != want {
		t.Errorf("RollbackCommand() = %q, want %q", got, want)
	}
}

func TestIsMarkerLineIsTheShapeParseRunLooksFor(t *testing.T) {
	for line, want := range map[string]bool{
		"CASAOS_DOCKER_UPDATE_SUCCESS " + testNonce + " 2026-08-13T01:05:00Z": true,
		"CASAOS_DOCKER_UPDATE_":             true,
		"casaos_docker_update_x":            false,
		" CASAOS_DOCKER_UPDATE_":            false,
		"zz CASAOS_DOCKER_UPDATE_SUCCESS x": false,
		"container: web always":             false,
		"":                                  false,
	} {
		if got := IsMarkerLine(line); got != want {
			t.Errorf("IsMarkerLine(%q) = %v, want %v", line, got, want)
		}
	}
	// ... and the lines the core and the unit write are all of that shape
	if !IsMarkerLine(strings.TrimSuffix(QueuedMarker(testNonce, time.Now()), "\n")) || !IsMarkerLine(FailedMarker(testNonce, time.Now(), FailStart)) {
		t.Error("a marker the core writes is not a marker line")
	}
}
