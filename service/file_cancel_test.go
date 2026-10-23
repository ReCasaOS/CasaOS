package service

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/model"
)

// holdCopy makes the copy of the folder dir/src (a.txt, c.txt) wait once begun, until release is called; then it goes on, or sees that it was cancelled.
// The copies of other sources run as they are.
func holdCopy(t *testing.T, dir string) (src string, begun <-chan struct{}, release func()) {
	t.Helper()
	src = filepath.Join(dir, "src")
	writeOperateFile(t, filepath.Join(src, "a.txt"), "a")
	writeOperateFile(t, filepath.Join(src, "c.txt"), "c")
	started, held := make(chan struct{}), make(chan struct{})
	var once sync.Once
	was := fileCopyDir
	fileCopyDir = func(ctx context.Context, from, to, style string) error {
		if from == src {
			close(started)
			select {
			case <-held:
			case <-time.After(10 * time.Second):
			}
		}
		return was(ctx, from, to, style)
	}
	t.Cleanup(func() { fileCopyDir = was })
	return src, started, func() { once.Do(func() { close(held) }) }
}

func waitBegun(t *testing.T, begun <-chan struct{}) {
	t.Helper()
	select {
	case <-begun:
	case <-time.After(5 * time.Second):
		t.Fatal("the held copy never began")
	}
}

// noOperationRuns is true once no goroutine runs a copy or a move of the queue.
func noOperationRuns() bool {
	buf := make([]byte, 1<<20)
	stacks := string(buf[:runtime.Stack(buf, true)])
	return !strings.Contains(stacks, "CasaOS/service.FileOperate(") && !strings.Contains(stacks, "CasaOS/service.fileOperate(")
}

func exists(path string) bool {
	_, err := os.Lstat(path)
	return err == nil
}

// unblockFifo lets a copy held at the open of the FIFO path go on (a test of a copy that should not open it, run on code that does).
func unblockFifo(path string) {
	if f, err := os.OpenFile(path, os.O_RDWR, 0); err == nil { // never blocks
		f.Close()
	}
}

func taskOutcome(id string) (finished bool, code string) {
	task, ok := FileQueue.Load(id)
	if !ok {
		return false, ""
	}
	return task.(model.FileOperate).Finished, task.(model.FileOperate).Error
}

func TestCancellingTheRunningTaskStopsItsCopyAndRunsTheNext(t *testing.T) {
	root := t.TempDir()
	src, begun, release := holdCopy(t, root)
	writeOperateFile(t, filepath.Join(root, "n.txt"), "n")
	headTo, nextTo := filepath.Join(root, "head"), filepath.Join(root, "next")
	for _, dir := range []string{headTo, nextTo} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	useFileQueue(t, []string{"head", "next"}, map[string]model.FileOperate{
		"head": {Type: "copy", Style: "overwrite", To: headTo, TotalSize: 2, Item: []model.FileItem{{From: src}}},
		"next": {Type: "copy", Style: "overwrite", To: nextTo, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(root, "n.txt")}}},
	})
	ExecOpFile()
	waitBegun(t, begun)

	CancelFileOperate("head")
	release()

	waitFor(t, func() bool { return exists(filepath.Join(nextTo, "n.txt")) }, "the next task runs")
	waitFor(t, noOperationRuns, "every operation ends")
	if exists(filepath.Join(headTo, "src")) {
		t.Error("the cancelled copy went on, or its half copy is still there")
	}
}

// The copy of a cancelled task, done again at once into the same folder: what the cancelled one leaves behind must not take the new copy with it.
func TestACopyRedoneRightAfterItsCancelIsKept(t *testing.T) {
	root := t.TempDir()
	src, begun, release := holdCopy(t, filepath.Join(root, "one"))
	again := filepath.Join(root, "two", "src")
	for _, name := range []string{"a.txt", "c.txt", "d.txt"} {
		writeOperateFile(t, filepath.Join(again, name), name)
	}
	to := filepath.Join(root, "to")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}
	useFileQueue(t, []string{"first"}, map[string]model.FileOperate{
		"first": {Type: "copy", Style: "overwrite", To: to, TotalSize: 2, Item: []model.FileItem{{From: src}}},
	})
	ExecOpFile()
	waitBegun(t, begun)

	CancelFileOperate("first")
	FileQueueLock.Lock() // what the route does for the copy asked again
	FileQueue.Store("again", model.FileOperate{Type: "copy", Style: "overwrite", To: to, TotalSize: 3, Item: []model.FileItem{{From: again}}})
	OpStrArr = append(OpStrArr, "again")
	FileQueueLock.Unlock()
	ExecOpFile()
	time.Sleep(200 * time.Millisecond) // were the queue to start the copy asked again now, it would have copied by then
	release()

	waitFor(t, noOperationRuns, "both operations end")
	for _, name := range []string{"a.txt", "c.txt", "d.txt"} {
		if got, err := os.ReadFile(filepath.Join(to, "src", name)); err != nil || string(got) != name {
			t.Errorf("the copy done again lost %s: %q, %v", name, got, err)
		}
	}
}

func TestTwoQuickCancelsStartTheNextTaskOnce(t *testing.T) {
	root := t.TempDir()
	src, begun, release := holdCopy(t, root)
	writeOperateFile(t, filepath.Join(root, "n1.txt"), "1")
	writeOperateFile(t, filepath.Join(root, "n2.txt"), "2")
	headTo, n1To, n2To := filepath.Join(root, "head"), filepath.Join(root, "n1"), filepath.Join(root, "n2")
	for _, dir := range []string{headTo, n1To, n2To} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	useFileQueue(t, []string{"head", "n1", "n2"}, map[string]model.FileOperate{
		"head": {Type: "copy", Style: "overwrite", To: headTo, TotalSize: 2, Item: []model.FileItem{{From: src}}},
		"n1":   {Type: "copy", Style: "overwrite", To: n1To, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(root, "n1.txt")}}},
		"n2":   {Type: "move", Style: "overwrite", To: n2To, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(root, "n2.txt")}}}, // a second run of it fails
	})
	ExecOpFile()
	waitBegun(t, begun)

	CancelFileOperate("head")
	CancelFileOperate("n1")
	release()

	waitFor(t, func() bool { finished, _ := taskOutcome("n2"); return finished }, "n2 runs")
	waitFor(t, noOperationRuns, "every operation ends")
	if _, code := taskOutcome("n2"); code != "" {
		t.Errorf("n2 ended with %q: it ran twice", code)
	}
	if exists(filepath.Join(n1To, "n1.txt")) {
		t.Error("the cancelled n1 ran")
	}
	if got, err := os.ReadFile(filepath.Join(n2To, "n2.txt")); err != nil || string(got) != "2" {
		t.Errorf("n2 was not moved: %q, %v", got, err)
	}
}

// A link that leads nowhere was at the destination before the copy: the cancel does not take it away.
func TestACancelledCopyKeepsWhatWasAtItsDestination(t *testing.T) {
	root := t.TempDir()
	src, begun, release := holdCopy(t, root)
	to := filepath.Join(root, "to")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(to, "src")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Fatal(err)
	}
	useFileQueue(t, []string{"held"}, map[string]model.FileOperate{
		"held": {Type: "copy", Style: "overwrite", To: to, TotalSize: 2, Item: []model.FileItem{{From: src}}},
	})
	ExecOpFile()
	waitBegun(t, begun)

	CancelFileOperate("held")
	release()

	waitFor(t, noOperationRuns, "the operation ends")
	if info, err := os.Lstat(link); err != nil || info.Mode()&os.ModeSymlink == 0 {
		t.Errorf("the link that was there is gone (%v)", err)
	}
}

// The route, the notifier and a cancel may each ask for the next task at the same moment: it runs once.
func TestStartingTheQueueTwiceRunsItsTaskOnce(t *testing.T) {
	root := t.TempDir()
	writeOperateFile(t, filepath.Join(root, "a.txt"), "a")
	to := filepath.Join(root, "to")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}
	useFileQueue(t, []string{"move"}, map[string]model.FileOperate{
		"move": {Type: "move", Style: "overwrite", To: to, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(root, "a.txt")}}}, // a second run of it fails
	})

	ExecOpFile()
	ExecOpFile()

	waitFor(t, func() bool { finished, _ := taskOutcome("move"); return finished }, "the move runs")
	waitFor(t, noOperationRuns, "every operation ends")
	if _, code := taskOutcome("move"); code != "" {
		t.Errorf("the move ended with %q: it ran twice", code)
	}
	if got, err := os.ReadFile(filepath.Join(to, "a.txt")); err != nil || string(got) != "a" {
		t.Errorf("the moved file holds %q (%v), want a", got, err)
	}
}

// A FIFO (or a device, a socket) in a copy would hold it at its open until a writer came: it is skipped, and the queue goes on.
func TestACopyOfAFolderHoldingAFifoSkipsItAndTheQueueGoesOn(t *testing.T) {
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeOperateFile(t, filepath.Join(src, "a.txt"), "a")
	if err := syscall.Mkfifo(filepath.Join(src, "b.fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	writeOperateFile(t, filepath.Join(src, "c.txt"), "c")
	writeOperateFile(t, filepath.Join(root, "n.txt"), "n")
	to, nextTo := filepath.Join(root, "to"), filepath.Join(root, "next")
	for _, dir := range []string{to, nextTo} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	useFileQueue(t, []string{"fifo", "next"}, map[string]model.FileOperate{
		"fifo": {Type: "copy", Style: "overwrite", To: to, TotalSize: 2, Item: []model.FileItem{{From: src}}},
		"next": {Type: "copy", Style: "overwrite", To: nextTo, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(root, "n.txt")}}},
	})
	t.Cleanup(func() { unblockFifo(filepath.Join(src, "b.fifo")) })
	useMessageBus(t, true)

	ExecOpFile()

	waitFor(t, func() bool { finished, _ := taskOutcome("fifo"); return finished }, "the copy of the folder with a FIFO ends")
	if _, code := taskOutcome("fifo"); code != "not_regular" {
		t.Errorf("the copy ended with %q, want not_regular", code)
	}
	for _, name := range []string{"a.txt", "c.txt"} {
		if !exists(filepath.Join(to, "src", name)) {
			t.Errorf("%s was not copied", name)
		}
	}
	if exists(filepath.Join(to, "src", "b.fifo")) {
		t.Error("the FIFO was copied")
	}
	NewNotifyService(nil).SendFileOperateNotify(true) // the notifier takes the ended task out, and the next one starts
	waitFor(t, func() bool { return exists(filepath.Join(nextTo, "n.txt")) }, "the next task runs")
}

// Across disks, a folder whose copy skips something is not a whole copy: the source stays, and no half copy is left.
func TestAMoveAcrossDisksOfAFolderHoldingAFifoKeepsTheSource(t *testing.T) {
	acrossDisks(t)
	root := t.TempDir()
	src := filepath.Join(root, "src")
	writeOperateFile(t, filepath.Join(src, "a.txt"), "a")
	if err := syscall.Mkfifo(filepath.Join(src, "b.fifo"), 0o644); err != nil {
		t.Fatal(err)
	}
	to := filepath.Join(root, "other-disk")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { unblockFifo(filepath.Join(src, "b.fifo")) })

	done := make(chan struct{})
	go func() { runOperation(t, move(src, to)); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the move is held at the FIFO")
	}

	for _, name := range []string{"a.txt", "b.fifo"} {
		if !exists(filepath.Join(src, name)) {
			t.Errorf("the source lost %s", name)
		}
	}
	if exists(filepath.Join(to, "src")) {
		t.Error("a half copy was left at the destination")
	}
}

// A name of 250 bytes (83 characters of Chinese) is a name a disk takes: the copy over an older file of that name works, and the older one is not lost.
func TestAMoveAcrossDisksOverAFileWithALongNameReplacesIt(t *testing.T) {
	acrossDisks(t)
	root := t.TempDir()
	name := strings.Repeat("长", 83) + "a" // 3*83+1 bytes
	writeOperateFile(t, filepath.Join(root, "src", name), "new")
	to := filepath.Join(root, "other-disk")
	writeOperateFile(t, filepath.Join(to, name), "old")

	runOperation(t, move(filepath.Join(root, "src", name), to))

	if got, err := os.ReadFile(filepath.Join(to, name)); err != nil || string(got) != "new" {
		t.Errorf("the destination holds %q (%v), want new", got, err)
	}
}

// An operation that panics fails its task, and the queue goes on.
func TestAnOperationThatPanicsFailsItsTaskAndTheQueueGoesOn(t *testing.T) {
	root := t.TempDir()
	writeOperateFile(t, filepath.Join(root, "boom", "a.txt"), "a")
	writeOperateFile(t, filepath.Join(root, "n.txt"), "n")
	to, nextTo := filepath.Join(root, "to"), filepath.Join(root, "next")
	for _, dir := range []string{to, nextTo} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	was := fileCopyDir
	fileCopyDir = func(ctx context.Context, from, dst, style string) error {
		if filepath.Base(from) == "boom" {
			panic("a copy that panics")
		}
		return was(ctx, from, dst, style)
	}
	t.Cleanup(func() { fileCopyDir = was })
	useMessageBus(t, true)
	useFileQueue(t, []string{"boom", "next"}, map[string]model.FileOperate{
		"boom": {Type: "copy", Style: "overwrite", To: to, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(root, "boom")}}},
		"next": {Type: "copy", Style: "overwrite", To: nextTo, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(root, "n.txt")}}},
	})

	ExecOpFile()

	waitFor(t, func() bool { finished, _ := taskOutcome("boom"); return finished }, "the task that panicked ends")
	if _, code := taskOutcome("boom"); code != "internal" {
		t.Errorf("the task that panicked ended with %q, want internal", code)
	}
	NewNotifyService(nil).SendFileOperateNotify(true)
	waitFor(t, func() bool { return exists(filepath.Join(nextTo, "n.txt")) }, "the next task runs")
}
