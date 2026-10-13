package osrun

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// samples is one valid marker of every kind, with the dashes that a kind can write as a word of its own.
func samples() []Marker {
	at := t0.Add(5 * time.Second)
	return []Marker{
		{Kind: MkQueued, At: at},
		{Kind: MkRun, RunID: testRun, Hop: "debian-11-12", PlanID: "p1-0123456789abcdef0123456789abcdef", RunKind: RunHop, Boot: bootA},
		{Kind: MkRun, RunID: testRun, Hop: "debian-11-12", RunKind: RunRefresh},
		{Kind: MkAttempt, N: 1, At: at, Phase: PhaseRun, Who: WhoAuto},
		{Kind: MkAttempt, N: 7, At: at, Phase: PhaseRecover, From: BlockFull, Who: WhoOwner},
		{Kind: MkStartFailed, N: 2, At: at},
		{Kind: MkRebootRequested, At: at},
		{Kind: MkRebootFailed, At: at},
		{Kind: MkTakeover, At: at},
		{Kind: MkAck, At: at, Result: "discard"},
		{Kind: MkAck, At: at, Result: "acknowledge"},
		{Kind: MkBootSeen, At: at, Boot: bootB},
		{Kind: MkUnitStart, N: 1, At: at, Phase: PhaseRun, Boot: bootA, PID: 4242},
		{Kind: MkUnitStart, N: 1, At: at, Phase: PhaseRun, PID: 1},
		{Kind: MkBlockStart, N: 1, At: at, Index: 3, ID: BlockSources},
		{Kind: MkBlockDone, N: 1, At: at, Index: 3, ID: BlockSources},
		{Kind: MkBlockSkipped, N: 1, Index: 14, ID: BlockRDone},
		{Kind: MkStop, N: 1, At: at, Index: 6, ID: BlockMinimal, RC: 1, Class: ClassNetwork, PNR: true},
		{Kind: MkStop, N: 1, At: at, Index: 0, ID: StopDriver, RC: 1, Class: ClassIntegrity},
		{Kind: MkStop, N: 1, At: at, Index: 2, ID: StopList, RC: 1, Class: ClassIntegrity},
		{Kind: MkStop, N: 3, At: at, Index: 0, ID: StopUnwind, RC: 1, Class: ClassGuard, PNR: true},
		{Kind: MkUnwound, N: 1, At: at, Result: "ok"},
		{Kind: MkUnwound, N: 1, At: at, Result: "partial"},
		{Kind: MkRefreshDone, N: 1, At: at, Result: "yes", Why: "reboot-required"},
		{Kind: MkRefreshDone, N: 1, At: at, Result: "no", Why: "none"},
		{Kind: MkRehearsalReady, N: 1, At: at},
		{Kind: MkRebootPending, N: 1, At: at, Boot: bootA, Kernel: "6.1.0-37-amd64"},
		{Kind: MkRebootNotNeeded, N: 1, At: at, Kernel: "unknown"},
		{Kind: MkVerifyDone, N: 2, At: at, Boot: bootB},
		{Kind: MkUnitEnd, N: 1, At: at, Result: "ok"},
		{Kind: MkUnitEnd, N: 1, At: at, Result: "stopped"},
	}
}

func logOf(lines ...string) io.Reader {
	return strings.NewReader(strings.Join(lines, "\n") + "\n")
}

func queued() string {
	return "CASAOS_OSU_QUEUED " + testNonce + " 0 " + FormatTime(t0)
}

func TestEveryKindOfMarkerWritesAndReadsBack(t *testing.T) {
	seen := map[string]bool{}
	for _, m := range samples() {
		seen[m.Kind] = true
		line, err := m.Line(testNonce)
		if err != nil {
			t.Errorf("%+v: %v", m, err)
			continue
		}
		if !strings.HasSuffix(line, "\n") || strings.Count(line, "\n") != 1 || len(line) > markerLineMax+1 {
			t.Errorf("%+v: the line is %q", m, line)
		}
		got, nonce, ok := parseMarker(strings.TrimSuffix(line, "\n"))
		if !ok || nonce != testNonce || got != m {
			t.Errorf("%s: read back as %+v (ok %v), want %+v", line, got, ok, m)
		}
	}
	for _, kind := range MarkerKinds {
		if !seen[kind] {
			t.Errorf("no sample of %s", kind)
		}
	}
	if len(MarkerKinds) != 21 || len(schemas) != 21 {
		t.Errorf("%d kinds and %d schemas, want 21", len(MarkerKinds), len(schemas))
	}
	for _, kind := range MarkerKinds {
		if _, ok := schemas[kind]; !ok {
			t.Errorf("%s has no schema", kind)
		}
	}
}

func TestAMarkerLineIsTheShapeTheDriverWrites(t *testing.T) {
	// What the shell driver prints, word for word.
	got, nonce, ok := parseMarker("CASAOS_OSU_STOP " + testNonce + " 1 6 minimal 100 apt_failed 1 2026-10-13T10:00:03Z")
	want := Marker{Kind: MkStop, N: 1, At: t0.Add(3 * time.Second), Index: 6, ID: BlockMinimal, RC: 100, Class: ClassAptFailed, PNR: true}
	if !ok || nonce != testNonce || got != want {
		t.Errorf("read as %+v (ok %v, nonce %q), want %+v", got, ok, nonce, want)
	}
}

func TestAClassThatIsNotOneOfTheClassesReadsAsUnknown(t *testing.T) {
	line := "CASAOS_OSU_STOP " + testNonce + " 1 2 timers 1 made_up_word 0 " + FormatTime(t0)
	m, _, ok := parseMarker(line)
	if !ok || m.Class != ClassUnknown {
		t.Errorf("class %q (ok %v), want %q: a stop must never be lost for a word the core does not know", m.Class, ok, ClassUnknown)
	}
	// What is not a word of a class at all is not a marker.
	for _, class := range []string{"Busy", "busy2", "a-b", strings.Repeat("a", 33)} {
		if _, _, ok := parseMarker(strings.Replace(line, "made_up_word", class, 1)); ok {
			t.Errorf("class %q was taken", class)
		}
	}
}

func TestWhatIsNotAMarkerIsCountedAndLeftOut(t *testing.T) {
	good := "CASAOS_OSU_BLOCK_START " + testNonce + " 1 1 verify " + FormatTime(t0)
	forged := strings.Replace(good, testNonce, strings.Repeat("f", 32), 1)
	for name, bad := range map[string]string{
		"a forged nonce":                  forged,
		"a short nonce":                   strings.Replace(good, testNonce, "abc", 1),
		"an upper case nonce":             strings.Replace(good, testNonce, strings.ToUpper(testNonce), 1),
		"a marker in the middle of noise": "apt said: " + good,
		"noise after the marker":          good + " and then some",
		"a marker with a space before it": " " + good,
		"a carriage return":               good + "\r",
		"a NUL":                           good[:20] + "\x00" + good[21:],
		"an unknown kind":                 strings.Replace(good, "BLOCK_START", "BLOCK_BEGIN", 1),
		"a kind in lower case":            strings.Replace(good, "BLOCK_START", "block_start", 1),
		"a field too many":                good + " x",
		"a field too few":                 strings.TrimSuffix(good, " "+FormatTime(t0)),
		"a double space":                  strings.Replace(good, " 1 verify", "  1 verify", 1),
		"a tab":                           strings.Replace(good, " verify ", "\tverify ", 1),
		"a trailing space":                good + " ",
		"a block that is not one":         strings.Replace(good, "verify", "reformat", 1),
		"a block with a shell character":  strings.Replace(good, "verify", "verify;rm", 1),
		"an index with a sign":            strings.Replace(good, " 1 verify", " +1 verify", 1),
		"an index with a leading zero":    strings.Replace(good, " 1 verify", " 01 verify", 1),
		"an index that is not a number":   strings.Replace(good, " 1 verify", " x verify", 1),
		"an attempt of zero for a unit":   strings.Replace(good, testNonce+" 1", testNonce+" 0", 1),
		"an attempt of 1000":              strings.Replace(good, testNonce+" 1", testNonce+" 1000", 1),
		"a negative attempt":              strings.Replace(good, testNonce+" 1", testNonce+" -1", 1),
		"a time with no zone":             strings.TrimSuffix(good, "Z"),
		"a time in another zone":          strings.Replace(good, "Z", "+01:00", 1),
		"a month of 13":                   strings.Replace(good, "-10-", "-13-", 1),
		"a time of nothing":               strings.Replace(good, FormatTime(t0), "0001-01-01T00:00:00Z", 1),
		"an empty line":                   "",
	} {
		_, ms, ignored, err := ParseMarkers(logOf(queued(), bad, good))
		if err != nil || len(ms) != 2 || ignored != 1 {
			t.Errorf("%s: %d markers and %d ignored (err %v), want the queued one and the good one, and one ignored", name, len(ms), ignored, err)
		}
	}
}

func TestAttemptNumbersAreOnTheKindsThatTakeThem(t *testing.T) {
	for name, line := range map[string]string{
		"the core's run line with an attempt": "CASAOS_OSU_TAKEOVER " + testNonce + " 1 " + FormatTime(t0),
		"an attempt's line with none":         "CASAOS_OSU_ATTEMPT " + testNonce + " 0 run - auto " + FormatTime(t0),
		"an unit line with none":              "CASAOS_OSU_UNIT_END " + testNonce + " 0 ok " + FormatTime(t0),
	} {
		if _, _, ok := parseMarker(line); ok {
			t.Errorf("%s was taken", name)
		}
	}
}

func TestALineOfExactly256BytesIsTakenAndOneMoreIsNot(t *testing.T) {
	// A RUN line is the longest there is: a hop name of 64 characters and a plan id of 63 make 256 bytes.
	m := Marker{Kind: MkRun, RunID: testRun, Hop: strings.Repeat("h", 64), PlanID: strings.Repeat("p", 63), RunKind: RunHop, Boot: bootA}
	line, err := m.Line(testNonce)
	if err != nil || len(line)-1 != markerLineMax {
		t.Fatalf("a line of %d bytes (err %v), want %d", len(line)-1, err, markerLineMax)
	}
	if got, _, ok := parseMarker(strings.TrimSuffix(line, "\n")); !ok || got != m {
		t.Errorf("the line of 256 bytes was read as %+v (ok %v)", got, ok)
	}
	m.PlanID = strings.Repeat("p", 64)
	if _, err := m.Line(testNonce); err == nil {
		t.Error("a RUN line of 257 bytes was written")
	}
	long := strings.Replace(line, strings.Repeat("p", 63), strings.Repeat("p", 64), 1)
	if len(long)-1 != markerLineMax+1 {
		t.Fatalf("the test line is %d bytes", len(long)-1)
	}
	if _, _, ok := parseMarker(strings.TrimSuffix(long, "\n")); ok {
		t.Error("a RUN line of 257 bytes was read")
	}
}

func TestOnlyTheFirstLineGivesTheNonce(t *testing.T) {
	other := strings.Repeat("e", 32)
	text := strings.Join([]string{
		queued(),
		"CASAOS_OSU_QUEUED " + other + " 0 " + FormatTime(t0.Add(time.Second)),
		"CASAOS_OSU_QUEUED " + testNonce + " 0 " + FormatTime(t0.Add(time.Second)),
		"CASAOS_OSU_TAKEOVER " + other + " 0 " + FormatTime(t0.Add(2*time.Second)),
		"CASAOS_OSU_TAKEOVER " + testNonce + " 0 " + FormatTime(t0.Add(3*time.Second)),
	}, "\n") + "\n"
	nonce, ms, ignored, err := ParseMarkers(strings.NewReader(text))
	if err != nil || nonce != testNonce || ignored != 3 || len(ms) != 2 || ms[0].Kind != MkQueued || ms[1].Kind != MkTakeover {
		t.Errorf("nonce %q, %d markers, %d ignored (err %v)", nonce, len(ms), ignored, err)
	}
}

func TestALogWhoseFirstLineIsNotTheQueuedMarkerIsNoRun(t *testing.T) {
	run := "CASAOS_OSU_RUN " + testNonce + " 0 " + testRun + " debian-11-12 - hop -"
	for name, r := range map[string]io.Reader{
		"a log that begins with the run line": logOf(run, queued()),
		"a log that begins with a blank":      logOf("", queued(), run),
		"a log that begins with noise":        logOf("Reading package lists...", queued(), run),
		"a queued line with a short nonce":    logOf("CASAOS_OSU_QUEUED abc 0 "+FormatTime(t0), run),
		"a queued line with an attempt":       logOf("CASAOS_OSU_QUEUED "+testNonce+" 1 "+FormatTime(t0), run),
		"an empty file":                       strings.NewReader(""),
		"one newline":                         strings.NewReader("\n"),
	} {
		nonce, ms, _, err := ParseMarkers(r)
		if err != nil || nonce != "" || len(ms) != 0 {
			t.Errorf("%s: nonce %q and %d markers (err %v), want none", name, nonce, len(ms), err)
		}
	}
}

func TestALineThatWasNotFinishedIsNotAMarker(t *testing.T) {
	text := strings.Join([]string{queued(), "CASAOS_OSU_TAKEOVER " + testNonce + " 0 " + FormatTime(t0)}, "\n")
	nonce, ms, ignored, _ := ParseMarkers(strings.NewReader(text))
	if nonce != testNonce || len(ms) != 1 || ignored != 1 {
		t.Errorf("%d markers and %d ignored for a last line with no newline", len(ms), ignored)
	}
	_, ms, ignored, _ = ParseMarkers(strings.NewReader(text + "\n"))
	if len(ms) != 2 || ignored != 0 {
		t.Errorf("%d markers and %d ignored once the newline is there", len(ms), ignored)
	}
}

func TestAnyBytesAreOnlyEverCountedOrTaken(t *testing.T) {
	var all []byte
	for b := 0; b < 256; b++ {
		all = append(all, byte(b))
	}
	for _, in := range [][]byte{nil, {0}, {'\n'}, all, bytes.Repeat([]byte{'\n'}, 100), bytes.Repeat([]byte("CASAOS_OSU_"), 50), []byte("\xff\xfe CASAOS_OSU_QUEUED")} {
		nonce, ms, ignored, err := ParseMarkers(bytes.NewReader(in))
		if err != nil || nonce != "" || len(ms) != 0 || ignored < 0 {
			t.Errorf("%q: nonce %q, %d markers, %d ignored, err %v", in, nonce, len(ms), ignored, err)
		}
	}
}

// countingReader serves as many bytes of a line as it is asked for and says how many it gave.
type countingReader struct {
	line string
	read int
}

func (c *countingReader) Read(p []byte) (int, error) {
	n := 0
	for n < len(p) {
		p[n] = c.line[(c.read+n)%len(c.line)]
		n++
	}
	c.read += n
	return n, nil
}

func TestALogLongerThanOneMiBIsReadNoFurther(t *testing.T) {
	line := "CASAOS_OSU_REBOOT_FAILED " + testNonce + " 0 " + FormatTime(t0) + "\n"
	r := &countingReader{line: queued() + "\n" + strings.Repeat(line, 1)}
	// The reader repeats its first lines for ever: after the QUEUED line comes the same one again, which is a duplicate.
	nonce, ms, ignored, err := ParseMarkers(r)
	if !errors.Is(err, ErrMarkersTooLarge) {
		t.Errorf("err %v, want ErrMarkersTooLarge", err)
	}
	if r.read > markersMax+4096 {
		t.Errorf("%d bytes were read, which is more than the 1 MiB limit", r.read)
	}
	if nonce != testNonce || len(ms) == 0 || ms[0].Kind != MkQueued || ignored == 0 {
		t.Errorf("nonce %q, %d markers, %d ignored: what is before the limit must be given", nonce, len(ms), ignored)
	}

	// A file of exactly 1 MiB is not too long.
	body := queued() + "\n"
	for len(body)+len(line) <= markersMax {
		body += line
	}
	body += strings.Repeat("\n", markersMax-len(body))
	if _, _, _, err := ParseMarkers(strings.NewReader(body)); err != nil {
		t.Errorf("a log of %d bytes: %v", len(body), err)
	}
	if _, _, _, err := ParseMarkers(strings.NewReader(body + "\n")); !errors.Is(err, ErrMarkersTooLarge) {
		t.Errorf("a log of 1 MiB and a byte: %v", err)
	}
	if _, _, _, err := ParseMarkers(strings.NewReader(body + strings.Repeat("x", markersMax))); !errors.Is(err, ErrMarkersTooLarge) {
		t.Errorf("a log of 2 MiB: %v", err)
	}
}

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestAReadThatFailsIsAnError(t *testing.T) {
	if _, ms, _, err := ParseMarkers(failingReader{}); err == nil || len(ms) != 0 {
		t.Errorf("err %v and %d markers", err, len(ms))
	}
}

func TestTimeIsWrittenInUTCWithNoFraction(t *testing.T) {
	zone := time.FixedZone("x", 3600)
	at := time.Date(2026, 10, 13, 11, 0, 7, 999, zone)
	if got := FormatTime(at); got != "2026-10-13T10:00:07Z" {
		t.Errorf("FormatTime = %q", got)
	}
	if _, ok := parseTime("2026-10-13T10:00:07.5Z"); ok {
		t.Error("a fraction of a second was taken")
	}
}

func TestNonceAndUUIDShapes(t *testing.T) {
	for s, want := range map[string]bool{testNonce: true, strings.Repeat("a", 32): true, strings.Repeat("A", 32): false, strings.Repeat("a", 31): false, strings.Repeat("g", 32): false, "": false} {
		if got := ValidNonce(s); got != want {
			t.Errorf("ValidNonce(%q) = %v", s, got)
		}
	}
	for s, want := range map[string]bool{testRun: true, bootA: true, strings.ToUpper(testRun): false, testRun[:35]: false, strings.Replace(testRun, "-", "x", 1): false, "": false, testRun + "0": false} {
		if got := ValidUUID(s); got != want {
			t.Errorf("ValidUUID(%q) = %v", s, got)
		}
	}
}

func TestNextAttemptIsOneMoreThanTheAttemptsThatCount(t *testing.T) {
	l := newLog(t, RunHop)
	if got := NextAttempt(l.input()); got != 1 {
		t.Errorf("no attempt: %d", got)
	}
	l.attempt(1, PhaseRun, "", WhoAuto)
	if got := NextAttempt(l.input()); got != 2 {
		t.Errorf("one attempt: %d", got)
	}
	// An attempt that skips a number is not one, and does not move the number on.
	l.attempt(5, PhaseRecover, "", WhoOwner)
	if got := NextAttempt(l.input()); got != 2 {
		t.Errorf("an attempt that skipped numbers: %d", got)
	}
}

func FuzzParseMarkers(f *testing.F) {
	var valid strings.Builder
	valid.WriteString(queued() + "\n")
	for _, m := range samples()[1:] {
		line, err := m.Line(testNonce)
		if err != nil {
			f.Fatal(err)
		}
		valid.WriteString(line)
	}
	f.Add([]byte(valid.String()))
	f.Add([]byte(queued() + "\r\nCASAOS_OSU_TAKEOVER " + testNonce + " 0 " + FormatTime(t0) + "\r\n"))
	f.Add([]byte(queued() + "\nCASAOS_OSU_ACK " + testNonce + " 0 discard " + FormatTime(t0)))
	f.Add([]byte("\x00\xff\n\n\nCASAOS_OSU_"))
	f.Fuzz(func(t *testing.T, data []byte) {
		nonce, ms, ignored, err := ParseMarkers(bytes.NewReader(data))
		if err != nil {
			t.Fatalf("a reader of bytes failed: %v", err)
		}
		lines := bytes.Count(data, []byte("\n"))
		if !bytes.HasSuffix(data, []byte("\n")) && len(data) > 0 {
			lines++
		}
		if len(ms)+ignored != lines {
			t.Fatalf("%d markers and %d ignored for %d lines", len(ms), ignored, lines)
		}
		if nonce == "" {
			if len(ms) != 0 {
				t.Fatalf("%d markers with no nonce", len(ms))
			}
			return
		}
		if !ValidNonce(nonce) || len(ms) == 0 || ms[0].Kind != MkQueued {
			t.Fatalf("nonce %q and first marker %+v", nonce, ms)
		}
		for _, m := range ms {
			line, err := m.Line(nonce)
			if err != nil {
				t.Fatalf("%+v was read and cannot be written: %v", m, err)
			}
			if again, got, ok := parseMarker(strings.TrimSuffix(line, "\n")); !ok || got != nonce || again != m {
				t.Fatalf("%+v written as %q is read as %+v", m, line, again)
			}
		}
		// Whatever was read can be reduced, and what is said of it is a state of the run.
		now := t0.Add(time.Hour)
		in := Input{Markers: ms, Unit: Unit{Answered: true}, Boot: Boot{Now: bootA, OK: true}, Now: now}
		s := Reduce(in)
		if !isOneOf(States, s.Name) {
			t.Fatalf("state %q", s.Name)
		}
		_ = NextAction(s, in)
		_ = ActionsFor(s)
	})
}
