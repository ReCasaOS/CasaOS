package osplan

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// The owner's text and the driver work on the same machine, the same backup folder and the same files that say how far the hop is: the owner can take over from the unit, at
// any block, and the unit can go on from what the owner did. Both ways are run here, on the real blocks.

// doneFiles are the names of the files that say how far a backup folder is, and the other files that blocks write there for the next ones.
func (f *flowBox) filesOfTheBackupFolder() []string {
	m, _ := filepath.Glob(filepath.FromSlash(f.bk + "/*"))
	var out []string
	for _, p := range m {
		out = append(out, filepath.Base(p))
	}
	slices.Sort(out)
	return out
}

// After each block that the driver ran, the next block of the owner's text, typed with its three words, goes through its gate and ends: the head of a block asks for the done file
// of the one before it, and the driver leaves it. The driver is lost as it ends the block (right after its BLOCK_DONE), which is how the machine is left by a unit that is stopped
// there.
func TestAfterEachBlockOfTheDriverTheNextBlockOfTheOwnersTextRuns(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		manual := manualOf(t)
		for i, id := range hopList {
			f := newFlowBox(t, sh)
			f.hookAfter(2*(i+1)+1, "kill -9 $$")
			f.launch(1, osrun.PhaseRun, hopList...)
			if !f.exists(f.bk + "/done-" + id) {
				t.Fatalf("the driver did not end block %s", id)
			}
			if got := f.story(1); got[len(got)-1] != "BLOCK_DONE "+itoa(i+1)+" "+id {
				t.Fatalf("the driver was not lost as it ended block %s: %q", id, got)
			}
			next := osrun.BlockAfter
			if i+1 < len(hopList) {
				next = hopList[i+1]
			}
			// the last step is the restart, which the owner does by hand and the box does by its files
			if next == osrun.BlockAfter {
				restartFlowBox(f.sb)
			}
			out, code := f.manualBlock(manual, next)
			if code != 0 || !f.exists(f.bk+"/done-"+next) {
				t.Errorf("after the driver ran %s, the owner's block %s: exit %d, %q", id, next, code, out)
			}
		}
	})
}

// A block of the driver that stops leaves the machine as the table at the end of the owner's text says for that block: undone, when the upgrade has not begun (the owner starts
// again from the first block, on a machine that is the one that the plan was made from), and where it was, when it has (the owner runs the same block again once what stopped it is
// mended, as the table says).
func TestAfterABlockOfTheDriverThatStoppedTheOwnersTextTakesOver(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		manual := manualOf(t)
		for _, tc := range stopsOfTheBlocks() {
			f := newFlowBox(t, sh)
			fix := tc.set(f.sb)
			f.launch(1, osrun.PhaseRun, hopList...)
			fix()
			c := f.clone()
			if !tc.pnr {
				// the machine is the one of the plan: the owner's blocks 0, 1 and 2 go through, and a stop of block 1 did not leave a state of the timers that spoils them
				if !c.sourcesAreTheOriginals() {
					t.Errorf("%s: the sources are not the original ones", tc.name)
				}
				// the folder that the unwind put aside is made again by block 0 (mkdir -m 700), which a Windows file system cannot do: there the boxes of all the tests have it
				if runtime.GOOS == "windows" {
					os.MkdirAll(filepath.FromSlash(c.bk), 0o755)
				}
				for _, id := range hopList[:3] {
					if out, code := c.manualBlock(manual, id); code != 0 || !c.exists(c.bk+"/done-"+id) {
						t.Errorf("%s: after the unwind, the owner's block %s: exit %d, %q", tc.name, id, code, out)
						break
					}
				}
				continue
			}
			// the upgrade has begun: the block that stopped is run again, by hand
			if out, code := c.manualBlock(manual, tc.id); code != 0 || !c.exists(c.bk+"/done-"+tc.id) {
				t.Errorf("%s: the owner's block %s again: exit %d, %q", tc.name, tc.id, code, out)
			}
		}
	})
}

// The other way: the owner's blocks are run by hand up to any block, and the unit is started with the list of all the blocks of the hop (the core's list for a run that has
// to go on): it passes the blocks that have their done file, runs the others, and ends as a run that the unit made from the start.
func TestAfterTheBlocksOfTheOwnersTextTheDriverGoesOnAndEndsTheSame(t *testing.T) {
	eachDriverShell(t, false, func(t *testing.T, sh driverShell) {
		manual := manualOf(t)
		// the end of a hop that the unit ran alone
		alone := newFlowBox(t, sh)
		alone.launch(1, osrun.PhaseRun, hopList...)
		wantFiles := alone.filesOfTheBackupFolder()
		f := newFlowBox(t, sh)
		for k, id := range hopList {
			if out, code := f.manualBlock(manual, id); code != 0 || !f.exists(f.bk+"/done-"+id) {
				t.Fatalf("the owner's block %s: exit %d, %q", id, code, out)
			}
			c := f.clone()
			c.launch(1, osrun.PhaseRun, hopList...)
			want := []string{"UNIT_START run"}
			for i, id := range hopList {
				if i <= k {
					want = append(want, "BLOCK_SKIPPED "+itoa(i+1)+" "+id)
				} else {
					want = append(want, blockStory(i+1, id)...)
				}
			}
			want = append(want, "REBOOT_PENDING 6.1.0-53-cloud-amd64", "UNIT_END ok")
			equalStory(t, "the unit after the owner's block "+id, c.story(1), want)
			if got := c.filesOfTheBackupFolder(); !slices.Equal(got, wantFiles) {
				t.Errorf("after the owner's block %s and the unit: the backup folder has %v, and the unit's own run %v", id, got, wantFiles)
			}
			if c.aptUpgrades() != 1 {
				t.Errorf("after the owner's block %s: apt upgrade ran %d times", id, c.aptUpgrades())
			}
			wantState(t, c.driverBox, "after the owner's block "+id, osrun.StateAwaitingReboot, "", true, osrun.DoBarrier)

			// the same with the list of the blocks that are left, which is what a core that reads the files of the folder would give it
			if rest := hopList[k+1:]; len(rest) > 0 {
				c = f.clone()
				c.launch(1, osrun.PhaseRun, rest...)
				want := append([]string{"UNIT_START run"}, blockStory(1, rest...)...)
				equalStory(t, "the unit with the blocks left after the owner's block "+id, c.story(1), append(want, "REBOOT_PENDING 6.1.0-53-cloud-amd64", "UNIT_END ok"))
				if got := c.filesOfTheBackupFolder(); !slices.Equal(got, wantFiles) {
					t.Errorf("after the owner's block %s and the unit with the blocks left: the backup folder has %v, and the unit's own run %v", id, got, wantFiles)
				}
			}
		}
	})
}
