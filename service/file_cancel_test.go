package service

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"

	"github.com/ReCasaOS/CasaOS/model"
)

// heldCopy is a folder src/ whose copy waits at src/b.fifo, between a.txt and c.txt, until release is called.
func heldCopy(t *testing.T, dir string) (src string, release func()) {
	t.Helper()
	src = filepath.Join(dir, "src")
	writeOperateFile(t, filepath.Join(src, "a.txt"), "a")
	writeOperateFile(t, filepath.Join(src, "c.txt"), "c")
	fifo := filepath.Join(src, "b.fifo")
	if err := syscall.Mkfifo(fifo, 0o644); err != nil {
		t.Fatal(err)
	}
	return src, func() {
		writer, err := os.OpenFile(fifo, os.O_RDWR, 0) // never blocks, and lets the copy's open of b.fifo return
		if err != nil {
			t.Fatal(err)
		}
		writer.Write([]byte("b"))
		writer.Close()
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

// The copy of a cancelled task, done again at once into the same folder: what the cancelled one leaves behind must not take the new copy with it.
func TestACopyRedoneRightAfterItsCancelIsKept(t *testing.T) {
	root := t.TempDir()
	src, release := heldCopy(t, filepath.Join(root, "one"))
	again := filepath.Join(root, "two", "src")
	for _, name := range []string{"a.txt", "c.txt", "d.txt"} {
		writeOperateFile(t, filepath.Join(again, name), name)
	}
	to := filepath.Join(root, "to")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}
	useFileQueue(t, []string{"first"}, map[string]model.FileOperate{
		"first": {Type: "copy", Style: "overwrite", To: to, TotalSize: 3, Item: []model.FileItem{{From: src}}},
	})
	ExecOpFile()
	waitFor(t, func() bool { return exists(filepath.Join(to, "src", "a.txt")) }, "the first copy reaches b.fifo")

	CancelFileOperate("first")
	FileQueueLock.Lock() // what the route does for the copy asked again
	FileQueue.Store("again", model.FileOperate{Type: "copy", Style: "overwrite", To: to, TotalSize: 3, Item: []model.FileItem{{From: again}}})
	OpStrArr = append(OpStrArr, "again")
	FileQueueLock.Unlock()
	ExecOpFile()
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
	src, release := heldCopy(t, root)
	writeOperateFile(t, filepath.Join(root, "n1.txt"), "1")
	writeOperateFile(t, filepath.Join(root, "n2.txt"), "2")
	headTo, n1To, n2To := filepath.Join(root, "head"), filepath.Join(root, "n1"), filepath.Join(root, "n2")
	for _, dir := range []string{headTo, n1To, n2To} {
		if err := os.Mkdir(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	useFileQueue(t, []string{"head", "n1", "n2"}, map[string]model.FileOperate{
		"head": {Type: "copy", Style: "overwrite", To: headTo, TotalSize: 3, Item: []model.FileItem{{From: src}}},
		"n1":   {Type: "copy", Style: "overwrite", To: n1To, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(root, "n1.txt")}}},
		"n2":   {Type: "move", Style: "overwrite", To: n2To, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(root, "n2.txt")}}}, // a second run of it fails
	})
	ExecOpFile()
	waitFor(t, func() bool { return exists(filepath.Join(headTo, "src", "a.txt")) }, "the head copy reaches b.fifo")

	CancelFileOperate("head")
	CancelFileOperate("n1")
	release()

	waitFor(t, func() bool {
		task, ok := FileQueue.Load("n2")
		return ok && task.(model.FileOperate).Finished
	}, "n2 runs")
	waitFor(t, noOperationRuns, "every operation ends")
	if task, _ := FileQueue.Load("n2"); task.(model.FileOperate).Error != "" {
		t.Errorf("n2 ended with %q: it ran twice", task.(model.FileOperate).Error)
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
	src, release := heldCopy(t, root)
	to := filepath.Join(root, "to")
	if err := os.Mkdir(to, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(to, "b.fifo")
	if err := os.Symlink(filepath.Join(root, "missing"), link); err != nil {
		t.Fatal(err)
	}
	useFileQueue(t, []string{"held"}, map[string]model.FileOperate{
		"held": {Type: "copy", Style: "overwrite", To: to, TotalSize: 1, Item: []model.FileItem{{From: filepath.Join(src, "b.fifo")}}},
	})
	ExecOpFile()
	waitFor(t, func() bool {
		buf := make([]byte, 1<<20)
		return strings.Contains(string(buf[:runtime.Stack(buf, true)]), "utils/file.copyFile(")
	}, "the copy is at the open of b.fifo")

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

	waitFor(t, func() bool {
		task, ok := FileQueue.Load("move")
		return ok && task.(model.FileOperate).Finished
	}, "the move runs")
	waitFor(t, noOperationRuns, "every operation ends")
	if task, _ := FileQueue.Load("move"); task.(model.FileOperate).Error != "" {
		t.Errorf("the move ended with %q: it ran twice", task.(model.FileOperate).Error)
	}
	if got, err := os.ReadFile(filepath.Join(to, "a.txt")); err != nil || string(got) != "a" {
		t.Errorf("the moved file holds %q (%v), want a", got, err)
	}
}
