package osplan

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The driver run under dash and bash, with stub blocks: blocks that leave the files that a block of the unit's text leaves, and that a test makes stop. What the driver does of its
// own is here (the markers, the unwind, the abort flag, the integrity checks, the end of each phase), with every marker read back by the core's reader and reduced by the
// core's reducer; what it does with the real blocks is in driver_hop_test.go, driver_crash_test.go and driver_handoff_test.go.

// stubParts is what the stub of a block does: what it leaves before it can stop, and what it leaves when it ends. The words in capitals are the box's.
var stubParts = map[string][2]string{
	osrun.BlockVerify:   {"", `: > "$BK/done-verify"`},
	osrun.BlockTimers:   {`printf 'apt-daily.timer active\napt-daily-upgrade.timer inactive\nunattended-upgrades.service active\n' > ROOT/timers.state; : > "$BK/timers-stopping"`, `: > "$BK/done-timers"`},
	osrun.BlockSnapshot: {"", `: > "$BK/done-snapshot"`},
	osrun.BlockSources: {`mkdir -p "$BK/apt-orig"
cat > "$BK/restore-sources.sh" <<'CASAOS_EOS'
#!/bin/sh
HERE="${0%/*}"
printf '%s\n' restore >> ROOT/trace.log
[ ! -f ROOT/restore-fails ] || exit 1
rm -f "$HERE/done-sources" "$HERE/done-download"
mv "$HERE/apt-orig" "$HERE/apt-orig.restored.$$"
CASAOS_EOS`, `: > "$BK/done-sources"`},
	osrun.BlockDownload:  {"", `: > "$BK/done-download"`},
	osrun.BlockMinimal:   {`: > "$BK/begun-upgrade"`, `: > "$BK/done-minimal"`},
	osrun.BlockFull:      {"", `: > "$BK/done-full"`},
	osrun.BlockPrereboot: {"", `printf '%s\n' 6.1.0-53-cloud-amd64 > "$BK/kernel"; if [ ! -f ROOT/restarted ]; then cat ROOT/bootid > "$BK/boot_id"; fi; : > "$BK/done-prereboot"`},
	osrun.BlockAfter:     {"", `: > "$BK/done-after"`},
	osrun.BlockForward:   {"", ""},
	osrun.BlockRCheck:    {`printf 'apt-daily.timer active\n' > ROOT/timers.state`, `: > "$BK/done-timers"; : > "$BK/done-rcheck"`},
	osrun.BlockRSources: {`mkdir -p "$BK/apt-orig"
cat > "$BK/restore-sources.sh" <<'CASAOS_EOS'
#!/bin/sh
HERE="${0%/*}"
printf '%s\n' restore >> ROOT/trace.log
mv "$HERE/apt-orig" "$HERE/apt-orig.restored.$$"
CASAOS_EOS`, `: > "$BK/done-rsources"`},
	osrun.BlockRUpgrade: {`: > "$BK/begun-rupgrade"`, `: > "$BK/done-rupgrade"`},
	osrun.BlockRDone:    {"", `if [ -f ROOT/refresh-reboot ]; then cat ROOT/refresh-reboot > "$BK/refresh-reboot"; fi; : > "$BK/done-rdone"`},
}

// stubBlock is the text of the stub of a block: it traces that it ran, leaves what a block leaves, and stops the way that the file fail-<id> of the box says ("<status> <class>")
// at the place between what it leaves on its way and what it leaves at its end.
func (b *driverBox) stubBlock(id string) string {
	parts := stubParts[id]
	text := "BK='" + b.bk + "'\nprintf '%s\\n' 'ran " + id + "' >> ROOT/trace.log\n" + parts[0] + "\n" +
		"if [ -f ROOT/fail-" + id + " ]; then read -r rc cls < ROOT/fail-" + id + "; [ -z \"$cls\" ] || printf '%s\\n' \"$cls\" > \"$CASAOS_OSU_DIR/stop-class\"; exit \"$rc\"; fi\n" + parts[1] + "\n"
	return strings.ReplaceAll(text, "ROOT", "'"+b.root+"'")
}

// newStubBox is a box of stub blocks and stub commands, for one shell. The backup folder is named as the real ones are.
func newStubBox(t *testing.T, sh driverShell, kind string) *driverBox {
	t.Helper()
	root := filepath.ToSlash(t.TempDir())
	bk := root + "/var/backups/casaos-os-upgrade-" + driverPlanID
	if kind == osrun.RunRefresh {
		bk = root + "/var/backups/casaos-os-upgrade-refresh-" + driverRunID
	}
	b := newDriverBox(t, sh, root, root+"/run", bk, "*/var/backups/casaos-os-upgrade-*", "*/var/backups/casaos-os-upgrade-refresh-*", kind)
	if err := os.MkdirAll(filepath.FromSlash(bk), 0o700); err != nil {
		t.Fatal(err)
	}
	b.write(root+"/bootid", driverBootA+"\n", 0o644)
	b.write(root+"/trace.log", "", 0o644)
	cmd := func(name, body string) {
		b.write(root+"/bin/"+name, "#!/bin/sh\n"+strings.ReplaceAll(body, "ROOT", "'"+root+"'")+"\n", 0o755)
	}
	cmd("systemctl", `printf '%s\n' "systemctl $*" >> ROOT/trace.log
[ ! -f ROOT/systemctl-fails-"$2" ] || exit 1`)
	cmd("dpkg", `printf '%s\n' "dpkg $*" >> ROOT/trace.log
if [ "$1" = --audit ]; then cat ROOT/audit.txt 2> /dev/null || true; fi`)
	cmd("sync", `printf '%s\n' "sync begun=$([ -e '`+bk+`/begun-upgrade' ] && echo yes || echo no)" >> ROOT/trace.log`)
	for id := range stubParts {
		b.blocks[id] = b.stubBlock(id)
	}
	return b
}

// trace is what the stubs wrote of what they ran, in order.
func (b *driverBox) trace() []string {
	var out []string
	for _, l := range strings.Split(b.read(b.root+"/trace.log"), "\n") {
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

// fail makes the stub of a block stop with a status and a class, after what it leaves on its way.
func (b *driverBox) fail(id string, rc int, class string) {
	b.t.Helper()
	b.write(b.root+"/fail-"+id, strings.TrimSpace(itoa(rc)+" "+class)+"\n", 0o644)
}

var hopList = osrun.BlockList(osrun.PhaseRun, osrun.Files{})

func equalStory(t *testing.T, where string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s:\n got  %q\n want %q", where, got, want)
	}
}

// blockStory is the markers of blocks that end well, from the first of ids.
func blockStory(first int, ids ...string) []string {
	var out []string
	for i, id := range ids {
		n := itoa(first + i)
		out = append(out, "BLOCK_START "+n+" "+id, "BLOCK_DONE "+n+" "+id)
	}
	return out
}

func TestTheDriverRunsTheBlocksOfAHopAndEndsWithTheRestartPending(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunHop)
		if code := b.launch(1, osrun.PhaseRun, hopList...); code != 0 {
			t.Fatalf("the driver ended with %d", code)
		}
		want := append(append([]string{"UNIT_START run"}, blockStory(1, hopList...)...), "REBOOT_PENDING 6.1.0-53-cloud-amd64", "UNIT_END ok")
		equalStory(t, "the markers of a hop", b.story(1), want)
		var pending osrun.Marker
		for _, m := range b.markers() {
			if m.Kind == osrun.MkRebootPending {
				pending = m
			}
			if m.Kind == osrun.MkUnitStart && (m.Boot != driverBootA || m.PID < 2) {
				t.Errorf("UNIT_START: %+v", m)
			}
		}
		if pending.Boot != driverBootA {
			t.Errorf("the boot that the restart is waited from: %q", pending.Boot)
		}
		// the blocks ran in order, and nothing else did but the sync before the minimal upgrade
		var ran []string
		for _, l := range b.trace() {
			if id, ok := strings.CutPrefix(l, "ran "); ok {
				ran = append(ran, id)
			}
		}
		if !slices.Equal(ran, hopList) {
			t.Errorf("the blocks that ran: %v", ran)
		}
		s, act := b.state()
		checkInvariants(t, "a hop that ended", s, b.files())
		if s.Name != osrun.StateAwaitingReboot || !s.Busy || !s.PNR || s.HalfUpgraded || act.Kind != osrun.DoBarrier {
			t.Errorf("a hop that ended: %+v, %+v", s, act)
		}
	})
}

func TestTheDriverSaysThatNoRestartIsNeededWhenBlock7LeftNoBootID(t *testing.T) {
	eachDriverShell(t, true, func(t *testing.T, sh driverShell) {
		b := newStubBox(t, sh, osrun.RunHop)
		b.write(b.root+"/restarted", "", 0o644) // the early path of block 7: the machine restarted already, and no boot id is written
		b.launch(1, osrun.PhaseRun, hopList...)
		got := b.story(1)
		if want := "REBOOT_NOT_NEEDED 6.1.0-53-cloud-amd64"; got[len(got)-2] != want || got[len(got)-1] != "UNIT_END ok" {
			t.Errorf("the end of the hop: %q", got[len(got)-3:])
		}
		if s, _ := b.state(); s.Name != osrun.StateResuming {
			t.Errorf("%+v", s)
		}
	})
}

func TestTheKernelAndTheBootOfTheMarkersAreTheirWordsOrNothing(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct {
			name, kernel, boot string // what the files say
			wantKernel         string
			wantBoot           string // the boot id of REBOOT_PENDING: "" is the dash
		}{
			{"words of a kernel", "6.1.0-53-cloud-amd64", driverBootA, "6.1.0-53-cloud-amd64", driverBootA},
			{"a kernel with a character that no marker holds", "6.1.0 53", driverBootA, "unknown", driverBootA},
			{"a kernel that is too long for a marker", strings.Repeat("a", 65), driverBootA, "unknown", driverBootA},
			{"a kernel of 64 characters", strings.Repeat("a", 64), driverBootA, strings.Repeat("a", 64), driverBootA},
			{"no kernel", "", driverBootA, "unknown", driverBootA},
			{"a boot id that is not a UUID", "6.1.0", "boot-1", "6.1.0", ""},
			{"a boot id in capitals", "6.1.0", strings.ToUpper(driverBootC), "6.1.0", ""},
			{"a boot id that is a UUID of another shape", "6.1.0", "1111111111111111111111111111111111111", "6.1.0", ""},
		} {
			b := newStubBox(t, sh, osrun.RunHop)
			b.blocks[osrun.BlockPrereboot] = "BK='" + b.bk + "'\nprintf '%s\\n' '" + tc.kernel + "' > \"$BK/kernel\"\nprintf '%s\\n' '" + tc.boot + "' > \"$BK/boot_id\"\n: > \"$BK/done-prereboot\"\n"
			if tc.kernel == "" {
				b.blocks[osrun.BlockPrereboot] = "BK='" + b.bk + "'\nprintf '%s\\n' '" + tc.boot + "' > \"$BK/boot_id\"\n: > \"$BK/done-prereboot\"\n"
			}
			b.launch(1, osrun.PhaseRun, hopList...)
			var got *osrun.Marker
			for _, m := range b.markers() {
				if m.Kind == osrun.MkRebootPending {
					got = &m
				}
			}
			if got == nil || got.Kernel != tc.wantKernel || got.Boot != tc.wantBoot {
				t.Errorf("%s: %+v", tc.name, got)
			}
		}
	})
}

// A boot id that cannot be read, or that is not one, is a dash in UNIT_START and VERIFY_DONE, and the line is still a marker (an empty field would make it none).
func TestTheBootOfTheUnitIsAUUIDOrADash(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		for _, tc := range []struct{ name, boot, want string }{
			{"a boot id", driverBootB + "\n", driverBootB},
			{"no file", "", ""},
			{"an empty file", "\n", ""},
			{"words", "not a boot id\n", ""},
			{"a UUID in capitals", strings.ToUpper(driverBootC) + "\n", ""},
			{"a UUID and more", driverBootB + "x\n", ""},
		} {
			b := newStubBox(t, sh, osrun.RunHop)
			if tc.name == "no file" {
				os.Remove(filepath.FromSlash(b.root + "/bootid"))
			} else {
				b.write(b.root+"/bootid", tc.boot, 0o644)
			}
			b.launch(1, osrun.PhaseVerify, osrun.BlockAfter)
			var start, done *osrun.Marker
			for _, m := range b.markers() {
				switch m.Kind {
				case osrun.MkUnitStart:
					start = &m
				case osrun.MkVerifyDone:
					done = &m
				}
			}
			if start == nil || done == nil || start.Boot != tc.want || done.Boot != tc.want || start.At.IsZero() {
				t.Errorf("%s: UNIT_START %+v, VERIFY_DONE %+v", tc.name, start, done)
			}
		}
	})
}
