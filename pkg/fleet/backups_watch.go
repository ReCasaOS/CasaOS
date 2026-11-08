package fleet

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// backupsAtOnce is how many boxes one pass polls at the same time.
const backupsAtOnce = 4

// watchBackups polls the boxes until ctx ends. A node that is not a master has
// no boxes: each pass finds none.
func (f *Fleet) watchBackups(ctx context.Context) {
	first := time.NewTimer(f.backupFirst)
	defer first.Stop()
	tick := time.NewTicker(f.backupEvery)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-first.C:
			f.passBackups(ctx)
		case <-tick.C:
			f.passBackups(ctx)
		case id := <-f.backupWake:
			select {
			case <-ctx.Done():
				return
			case <-time.After(f.backupSettle):
			}
			f.pollBoxes(ctx, id)
		}
	}
}

// RefreshBackups asks for a pass over one box soon. It never blocks (safe under
// f.mu): a wake that finds the queue full is dropped, the next pass reads the box.
func (f *Fleet) RefreshBackups(boxID string) {
	select {
	case f.backupWake <- boxID:
	default:
	}
}

// passBackups polls every online box, backupsAtOnce at a time, in id order.
func (f *Fleet) passBackups(ctx context.Context) {
	f.pollBoxes(ctx, "")
}

// pollBoxes polls the online boxes, or only the box only when it is not "",
// stores each answer and raises or resolves its alerts.
func (f *Fleet) pollBoxes(ctx context.Context, only string) {
	var boxes []Box
	f.mu.Lock()
	st, on := f.master()
	for _, b := range st.Boxes {
		if on && f.tunnels[b.ID] != nil && (only == "" || b.ID == only) {
			boxes = append(boxes, Box{ID: b.ID, Name: b.Name})
		}
	}
	f.mu.Unlock()
	sort.Slice(boxes, func(i, j int) bool { return boxes[i].ID < boxes[j].ID })

	slots := make(chan struct{}, backupsAtOnce)
	var all sync.WaitGroup
	for _, box := range boxes {
		slots <- struct{}{}
		all.Add(1)
		go func() {
			defer func() { <-slots; all.Done() }()
			b := f.pollBox(ctx, box)
			f.storeBackups(b)
			f.alertBackups(b)
		}()
	}
	all.Wait()
}

// alertBackups raises each problem of a box's fresh answer, every pass while
// it lasts (the hub says it once in six hours), and resolves the ones this box
// had and no longer has. An answer that is not fresh (box offline, poll failed
// or cut short: pollBox sets Error for each) says nothing either way: the old
// problems stay as they were. A failed verification lasts until a restore test
// passes, is not_verifiable, or its couple goes: a test running or a state the
// pass could not read does not end it. A sentence is made of the box's name,
// the app, the destination and fixed words: never of a text the box sent.
func (f *Fleet) alertBackups(b BoxBackups) {
	if b.Error != "" {
		return
	}
	type call struct {
		key, sentence string
		problem       bool
	}
	var calls []call
	var carried []string       // verification keys kept as they are if up
	now := map[string]string{} // key: its resolution
	prefix := "fleet:" + b.BoxID + ":"
	for _, c := range b.Couples {
		what := c.App + " on " + b.Name + " to " + c.Destination
		backup := prefix + "backup:" + c.App + ":" + c.Destination + ":"
		switch c.State {
		case "failed":
			calls = append(calls, call{backup + "failed", "Backup of " + what + " failed. See the Fleet page.", true})
			now[backup+"failed"] = "Resolved: the backup of " + what + " no longer fails."
		case "late":
			calls = append(calls, call{backup + "late", "Backup of " + what + " is late: its last scheduled slot has passed. See the Fleet page.", true})
			now[backup+"late"] = "Resolved: the backup of " + what + " is no longer late."
		}
		key := prefix + "verify:" + c.App + ":" + c.Destination
		switch c.Verify.State {
		case "failed":
			calls = append(calls, call{key, "Verification of the backup of " + what + " failed. See the Fleet page.", true})
			now[key] = "Resolved: the verification of the backup of " + what + " no longer fails."
		case "passed", "not_verifiable":
		default:
			carried = append(carried, key)
		}
	}
	var gone []string
	f.backupMu.Lock()
	if f.backupGone[b.BoxID] {
		f.backupMu.Unlock()
		return // removed while it was polled
	}
	if f.backupProblems == nil {
		f.backupProblems = map[string]string{}
	}
	for _, key := range carried {
		if resolution, up := f.backupProblems[key]; up {
			now[key] = resolution
		}
	}
	for key := range f.backupProblems {
		if _, still := now[key]; strings.HasPrefix(key, prefix) && !still {
			gone = append(gone, key)
		}
	}
	sort.Strings(gone)
	for _, key := range gone {
		calls = append(calls, call{key, f.backupProblems[key], false})
		delete(f.backupProblems, key)
	}
	for key, resolution := range now {
		f.backupProblems[key] = resolution
	}
	f.backupMu.Unlock()
	for _, c := range calls {
		f.BackupProblem(c.key, c.sentence, c.problem)
	}
}

// forgetBackups drops a removed box's snapshot and its problems, without a
// word: like its offline alert, nothing came back, the owner took it out. The
// id stays known as gone, so a poll still on its way stores and says nothing
// (box ids are never reused).
func (f *Fleet) forgetBackups(boxID string) {
	f.backupMu.Lock()
	defer f.backupMu.Unlock()
	if f.backupGone == nil {
		f.backupGone = map[string]bool{}
	}
	f.backupGone[boxID] = true
	delete(f.backups, boxID)
	for key := range f.backupProblems {
		if strings.HasPrefix(key, "fleet:"+boxID+":") {
			delete(f.backupProblems, key)
		}
	}
}
