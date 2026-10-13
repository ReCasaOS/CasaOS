package osrun

import (
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"
)

func noDuplicates(t *testing.T, name string, list []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, s := range list {
		if seen[s] {
			t.Errorf("%s: %q is there twice", name, s)
		}
		seen[s] = true
	}
}

func TestTheVocabularyIsTheClosedSetsOfTheDesign(t *testing.T) {
	for name, c := range map[string]struct {
		list []string
		n    int
	}{
		"phases": {Phases, 6}, "run kinds": {RunKinds, 3}, "blocks": {BlockIDs, 14}, "classes": {Classes, 20}, "states": {States, 23},
		"marker kinds": {MarkerKinds, 21}, "buttons": {ActionNames, 6},
	} {
		if len(c.list) != c.n {
			t.Errorf("%s: %d, want %d", name, len(c.list), c.n)
		}
		noDuplicates(t, name, c.list)
	}
	if got := strings.Join(Phases, " "); got != "refresh rehearse run recover verify unwind" {
		t.Errorf("phases: %s", got)
	}
	if got := strings.Join(BlockIDs, " "); got != "verify timers snapshot sources download minimal full prereboot after forward rcheck rsources rupgrade rdone" {
		t.Errorf("blocks: %s", got)
	}
	if got := strings.Join(Classes, " "); got != "busy network apt_failed dpkg_broken unknown disk plan guard converge barrier_kernel barrier_initrd barrier_grub barrier_sshd barrier_resolver barrier_other identity tools unit integrity abort" {
		t.Errorf("classes: %s", got)
	}
	if got := strings.Join(States, " "); got != "idle refreshing reboot_first refreshed rehearsing rehearsed rehearsal_done running unwinding recovering awaiting_reboot rebooting reboot_stalled resuming verifying succeeded succeeded_with_findings failed interrupted stopped_at_waypoint needs_attention manual manual_done" {
		t.Errorf("states: %s", got)
	}
	if got := strings.Join(MarkerKinds, " "); got != "QUEUED RUN ATTEMPT START_FAILED REBOOT_REQUESTED REBOOT_FAILED TAKEOVER ACK BOOT_SEEN UNIT_START BLOCK_START BLOCK_DONE BLOCK_SKIPPED STOP UNWOUND REFRESH_DONE REHEARSAL_READY REBOOT_PENDING REBOOT_NOT_NEEDED VERIFY_DONE UNIT_END" {
		t.Errorf("marker kinds: %s", got)
	}
}

// The driver of the unit lets a word through to the core by pattern, and runs a block only if it is one of a closed set:
// these are those words as the driver spells them. The sets here and the driver's are one, and the driver's tests read them
// out of the driver's own text.
func TestTheWordsThatTheDriverLetsThroughAreTheVocabulary(t *testing.T) {
	if got := strings.Join(Phases, "|"); got != "refresh|rehearse|run|recover|verify|unwind" {
		t.Errorf("the driver's phases are refresh|rehearse|run|recover|verify|unwind, the package's are %s", got)
	}
	if got := strings.Join(BlockIDs, "|"); got != "verify|timers|snapshot|sources|download|minimal|full|prereboot|after|forward|rcheck|rsources|rupgrade|rdone" {
		t.Errorf("the driver's blocks are not the package's: %s", got)
	}
	// A stop's class passes the driver's check only if it is lower case letters and underscores.
	letters := regexp.MustCompile(`^[a-z_]+$`)
	for _, class := range Classes {
		if !letters.MatchString(class) {
			t.Errorf("class %q would be turned into unknown by the driver", class)
		}
	}
	for _, id := range []string{StopDriver, StopList, StopUnwind} {
		if isOneOf(BlockIDs, id) {
			t.Errorf("%q is a block", id)
		}
	}
}

func TestTheConstantsAreTheShippedOnes(t *testing.T) {
	want := Timing{
		GraceQuiet: 30 * time.Second, StartLost: 30 * time.Second, StartLostSilent: 30 * time.Minute, SystemdSilence: 30 * time.Minute,
		RebootStall: 15 * time.Minute, RehearsalTTL: 30 * time.Minute, ResumeWait: 10 * time.Minute, SettleEvery: 30 * time.Second,
		MaxVerifyAttempts: 3, MaxUnwindAttempts: 3, AutoRecoverTries: 0, AutoRecoverWave: 1,
		Backoff:    [2]time.Duration{2 * time.Minute, 10 * time.Minute},
		Reminders:  [3]time.Duration{time.Hour, 24 * time.Hour, 24 * time.Hour},
		MarkersMax: 1024 * 1024, MarkerLineMax: 256, LogTail: 128 * 1024,
	}
	if DefaultTiming != want {
		t.Errorf("%+v, want %+v", DefaultTiming, want)
	}
	// The limit that is not to be loosened by a mistake is the one the reader has.
	if markersMax != DefaultTiming.MarkersMax || markerLineMax != DefaultTiming.MarkerLineMax {
		t.Error("the reader's limits are not the timing's")
	}
}

func TestNoAutomaticRepairIsShipped(t *testing.T) {
	// A hop that stopped after its point of no return is repaired by the owner's click or not at all, until the repair has
	// been seen to work on a real machine.
	if DefaultTiming.AutoRecoverTries != 0 {
		t.Fatalf("AutoRecoverTries = %d", DefaultTiming.AutoRecoverTries)
	}
}

func TestWhatEachClassMeansAfterThePointOfNoReturn(t *testing.T) {
	type behaviour struct {
		resumable bool
		wave      int
	}
	want := map[string]behaviour{
		ClassBusy: {true, 1}, ClassNetwork: {true, 1},
		ClassAptFailed: {true, 2}, ClassDpkgBroken: {true, 2}, ClassUnknown: {true, 2},
		ClassDisk: {true, 0}, ClassBarrierKernel: {true, 0}, ClassBarrierInitrd: {true, 0}, ClassBarrierGrub: {true, 0},
		ClassBarrierSSHD: {true, 0}, ClassBarrierResolver: {true, 0}, ClassBarrierOther: {true, 0}, ClassIdentity: {true, 0},
		ClassTools: {true, 0}, ClassAbort: {true, 0},
		ClassGuard: {false, 0}, ClassConverge: {false, 0}, ClassUnit: {false, 0}, ClassIntegrity: {false, 0}, ClassPlan: {false, 0},
	}
	if len(want) != len(Classes) {
		t.Fatalf("%d rows for %d classes", len(want), len(Classes))
	}
	for _, class := range Classes {
		w, ok := want[class]
		if !ok {
			t.Errorf("class %s has no row", class)
			continue
		}
		got := ClassBehavior(class)
		if got.Resumable != w.resumable || got.Wave != w.wave {
			t.Errorf("%s: %+v, want resumable %v and wave %d", class, got, w.resumable, w.wave)
		}
	}
	// A class nobody foresaw is the owner's.
	if got := ClassBehavior("made_up"); got.Resumable || got.Wave != 0 {
		t.Errorf("an unknown class: %+v", got)
	}
}

func TestTheWavesOfRepairsByThemselves(t *testing.T) {
	for _, c := range []struct {
		wave int
		in   []string
	}{
		{0, nil},
		{1, []string{ClassBusy, ClassNetwork}},
		{2, []string{ClassBusy, ClassNetwork, ClassAptFailed, ClassDpkgBroken, ClassUnknown}},
		{3, []string{ClassBusy, ClassNetwork, ClassAptFailed, ClassDpkgBroken, ClassUnknown}},
		{-1, nil},
	} {
		for _, class := range Classes {
			if got, want := Retryable(class, c.wave), slices.Contains(c.in, class); got != want {
				t.Errorf("wave %d, class %s: %v", c.wave, class, got)
			}
		}
	}
}

func TestTheHintForAnUpdateThatAptRefused(t *testing.T) {
	for line, want := range map[string]string{
		"E: Repository 'http://deb.debian.org/debian bullseye InRelease' changed its 'Suite' value from 'stable' to 'oldstable'":        HintReleaseInfoChange,
		"E: Repository 'http://deb.debian.org/debian bookworm InRelease' changed its 'Version' value from '12.14' to '12.15'":           HintReleaseInfoChange,
		"E: Repository 'http://deb.debian.org bookworm InRelease' changed its 'Label' value from 'Debian' to 'Debian2'":                 HintReleaseInfoChange,
		"E: Repository 'http://deb.debian.org bookworm InRelease' changed its 'Origin' value from 'Debian' to 'Else'":                   HintReleaseInfoChange,
		"E: Repository 'http://deb.debian.org bookworm InRelease' changed its 'Codename' value from 'bookworm' to 'trixie'":             HintReleaseInfoChange,
		"N: This must be accepted explicitly before updates for this repository can be applied. See apt-secure(8) manpage for details.": HintReleaseInfoChange,
		"E: Failed to fetch http://deb.debian.org/debian/dists/bookworm/InRelease  Temporary failure resolving 'deb.debian.org'":        "",
		"E: The repository 'http://x bookworm Release' does not have a Release file.":                                                   "",
		"E: Repository 'http://x bookworm InRelease' changed its 'Colour' value from 'a' to 'b'":                                        "",
		"E: Repository 'http://x bookworm InRelease' changed its 'Suite'":                                                               "",
		"": "",
	} {
		if got := HintOf(line); got != want {
			t.Errorf("%q: %q, want %q", line, got, want)
		}
	}
}

func TestWhatAHopIsAskedToRunForEachPhase(t *testing.T) {
	all := Files{DoneMinimal: true, DoneFull: true, DonePrereboot: true}
	for _, c := range []struct {
		phase string
		files Files
		want  string
	}{
		{PhaseRun, Files{}, "verify timers snapshot sources download minimal full prereboot"},
		{PhaseRehearse, Files{}, "verify timers snapshot sources download"},
		{PhaseVerify, all, "after"},
		{PhaseRefresh, Files{}, "rcheck rsources rupgrade rdone"},
		{PhaseUnwind, Files{}, ""},
		{"nonsense", Files{}, ""},
		// The repair runs the block that repairs first when the machine may be half way through dpkg.
		{PhaseRecover, Files{}, "forward minimal full prereboot"},
		{PhaseRecover, Files{Begun: true}, "forward minimal full prereboot"},
		{PhaseRecover, Files{DoneMinimal: true}, "forward full prereboot"},
		{PhaseRecover, Files{DoneMinimal: true, DoneFull: true}, "prereboot"},
		{PhaseRecover, all, ""},
	} {
		got := strings.Join(BlockList(c.phase, c.files), " ")
		if got != c.want {
			t.Errorf("%s with %+v: %q, want %q", c.phase, c.files, got, c.want)
		}
		for _, id := range BlockList(c.phase, c.files) {
			if !ValidBlock(id) {
				t.Errorf("%s: %q is not a block", c.phase, id)
			}
		}
	}
	if got := FirstMissing(Files{DoneMinimal: true}); got != BlockFull {
		t.Errorf("FirstMissing = %q", got)
	}
	// A list is never shared: a caller that changes it changes nothing else.
	a := BlockList(PhaseRun, Files{})
	a[0] = "changed"
	if b := BlockList(PhaseRun, Files{}); b[0] != BlockVerify {
		t.Errorf("the list of blocks was changed through a caller's copy: %v", b)
	}
}

func TestTheWayOfStartingAUnitIsOnlyOfTheShapeItIsMeantFor(t *testing.T) {
	ok := Attempt{RunID: testRun, Nonce: testNonce, Phase: PhaseVerify, N: 1}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*Attempt){
		"a run id that is not a UUID": func(a *Attempt) { a.RunID = "../../etc" },
		"a run id in upper case":      func(a *Attempt) { a.RunID = strings.ToUpper(testRun) },
		"a nonce that is short":       func(a *Attempt) { a.Nonce = testNonce[:31] },
		"a nonce with a dollar":       func(a *Attempt) { a.Nonce = testNonce[:31] + "$" },
		"a phase that is not one":     func(a *Attempt) { a.Phase = "reboot" },
		"a phase with a space":        func(a *Attempt) { a.Phase = "run " },
		"no phase":                    func(a *Attempt) { a.Phase = "" },
		"attempt 0":                   func(a *Attempt) { a.N = 0 },
		"attempt 1000":                func(a *Attempt) { a.N = 1000 },
		"a negative attempt":          func(a *Attempt) { a.N = -3 },
	} {
		a := ok
		mutate(&a)
		if a.Validate() == nil {
			t.Errorf("%s was taken", name)
		}
	}
	for _, phase := range Phases {
		for _, n := range []int{1, 999} {
			a := ok
			a.Phase, a.N = phase, n
			if err := a.Validate(); err != nil {
				t.Errorf("%s %d: %v", phase, n, err)
			}
		}
	}
}
