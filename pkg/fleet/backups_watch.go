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
			f.alertBackups(&b) // first: an alert it keeps up makes the box not green
			f.storeBackups(b)
		}()
	}
	all.Wait()
}

// alertBackups raises each problem of a box's fresh answer, every pass while
// it lasts (the hub says it once in six hours), and resolves the ones this box
// had once this answer shows them over: a backup alert when its couple is ok
// and no backup of the app is running (which would hide a late), a restore
// test's when one passes or is not_verifiable, a repository's when its check
// passes; any of them when its couple or its restic destination is gone. A
// state that says nothing of the problem (suspended, unknown, never, a test
// running, a route that failed) keeps it up without a word, and b is then not
// green. An answer that is not fresh (box offline, poll failed or cut short:
// pollBox sets Error for each) says nothing either way. A sentence is made of
// the box's name, the app, the destination and fixed words: never of a text
// the box sent.
func (f *Fleet) alertBackups(b *BoxBackups) {
	if b.Error != "" {
		return
	}
	type call struct {
		key, sentence string
		problem       bool
	}
	var calls []call
	now := map[string]string{}   // the keys up after this pass: their resolution
	carried := map[string]bool{} // the keys kept as they are, if up
	raise := func(key, sentence, resolution string) {
		if _, done := now[key]; !done {
			calls = append(calls, call{key, sentence, true})
			now[key] = resolution
		}
	}
	prefix := "fleet:" + b.BoxID + ":"
	for _, c := range b.Couples {
		what := c.App + " on " + b.Name + " to " + c.Destination
		backup := prefix + "backup:" + c.App + ":" + c.Destination + ":"
		switch {
		case c.State == "failed":
			raise(backup+"failed", "Backup of "+what+" failed. See the Fleet page.", "Resolved: the backup of "+what+" no longer fails.")
		case c.State == "late":
			raise(backup+"late", "Backup of "+what+" is late: its last scheduled slot has passed. See the Fleet page.", "Resolved: the backup of "+what+" is no longer late.")
		case c.State != "ok" || c.Running:
			carried[backup+"failed"], carried[backup+"late"] = true, true
		}
		repo := prefix + "verify:restic:" + c.Destination
		key := prefix + "verify:" + c.App + ":" + c.Destination
		if c.resticUnread { // its verify state comes from rows that may predate restic: say nothing of either
			carried[repo], carried[key] = true, true
			continue
		}
		if c.Restic {
			switch {
			case c.ResticCheck == nil:
				carried[repo] = true
			case !c.ResticCheck.OK:
				where := b.Name + " at " + c.Destination
				raise(repo, "The restic repository of "+where+" failed its check. See the Fleet page.", "Resolved: the restic repository of "+where+" no longer fails its check.")
			}
			continue
		}
		switch c.Verify.State {
		case "failed":
			raise(key, "Verification of the backup of "+what+" failed. See the Fleet page.", "Resolved: the verification of the backup of "+what+" no longer fails.")
		case "passed", "not_verifiable":
		case "none": // the destination answered that it keeps nothing in restic
			carried[key] = true
		default: // unknown or running: the restic state may not have been read
			carried[key], carried[repo] = true, true
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
	for key := range carried {
		if resolution, up := f.backupProblems[key]; up {
			if _, raised := now[key]; !raised {
				now[key] = resolution
				b.Green = false
			}
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
