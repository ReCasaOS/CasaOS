package service

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/config"
)

// useMessageBus points the service at a message bus that answers, or at none when up is false. It returns what was published.
func useMessageBus(t *testing.T, up bool) <-chan string {
	t.Helper()
	runtimePath := t.TempDir()
	events := make(chan string, 16)
	if up {
		bus := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			select {
			case events <- string(body):
			default:
			}
		}))
		t.Cleanup(bus.Close)
		if err := os.WriteFile(filepath.Join(runtimePath, external.MessageBusAddressFilename), []byte(bus.URL), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	previousConfig, previousService := config.CommonInfo, MyService
	config.CommonInfo = &model.CommonModel{RuntimePath: runtimePath}
	MyService = &store{}
	t.Cleanup(func() { config.CommonInfo, MyService = previousConfig, previousService })
	return events
}

// useFileQueue puts tasks in the copy and move queue, ids in their order.
func useFileQueue(t *testing.T, ids []string, tasks map[string]model.FileOperate) {
	t.Helper()
	previous := OpStrArr
	OpStrArr = ids
	for id, task := range tasks {
		FileQueue.Store(id, task)
	}
	t.Cleanup(func() {
		OpStrArr = previous
		FileQueue.Range(func(k, _ interface{}) bool {
			FileQueue.Delete(k)
			return true
		})
	})
}

func queuedTask(id string) bool {
	_, ok := FileQueue.Load(id)
	return ok
}

func TestTheNotifierRemovesTheFinishedTaskNotTheRunningOne(t *testing.T) {
	events := useMessageBus(t, true)
	useFileQueue(t, []string{"running", "done"}, map[string]model.FileOperate{
		"running": {Type: "copy", TotalSize: 10, ProcessedSize: 5},
		"done":    {Type: "copy", TotalSize: 10, ProcessedSize: 10, Finished: true},
	})

	NewNotifyService(nil).SendFileOperateNotify(true)

	if !reflect.DeepEqual(OpStrArr, []string{"running"}) {
		t.Errorf("the queue is %v, want [running]", OpStrArr)
	}
	if !queuedTask("running") || queuedTask("done") {
		t.Errorf("running queued: %v, done queued: %v; want true, false", queuedTask("running"), queuedTask("done"))
	}
	select {
	case event := <-events:
		if !strings.Contains(event, "FINISHED") || !strings.Contains(event, "PROCESSING") {
			t.Errorf("published %s, want the finished task and the running one", event)
		}
	case <-time.After(5 * time.Second):
		t.Error("nothing was published")
	}
}

func TestTheNotifierStartsTheNextTaskOnceTheRunningOneIsDone(t *testing.T) {
	useMessageBus(t, true)
	useFileQueue(t, []string{"done", "next"}, map[string]model.FileOperate{
		"done": {Type: "copy", TotalSize: 10, ProcessedSize: 10, Finished: true},
		"next": {Type: "copy", TotalSize: 1, To: t.TempDir(), Item: []model.FileItem{{From: filepath.Join(t.TempDir(), "missing")}}},
	})

	NewNotifyService(nil).SendFileOperateNotify(true)

	if !reflect.DeepEqual(OpStrArr, []string{"next"}) {
		t.Errorf("the queue is %v, want [next]", OpStrArr)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		if task, ok := FileQueue.Load("next"); ok && task.(model.FileOperate).Finished {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("the next task was not run")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestTheNotifierSkipsAnIdWhoseTaskIsGone(t *testing.T) {
	useMessageBus(t, true)
	useFileQueue(t, []string{"gone", "running"}, map[string]model.FileOperate{
		"running": {Type: "copy", TotalSize: 10, ProcessedSize: 5},
	})

	NewNotifyService(nil).SendFileOperateNotify(true)

	if !queuedTask("running") {
		t.Error("the running task was removed")
	}
}

func TestTheNotifierKeepsGoingWhenTheMessageBusIsDown(t *testing.T) {
	useMessageBus(t, false)

	NewNotifyService(nil).SendFileOperateNotify(true) // nothing queued

	useFileQueue(t, []string{"done"}, map[string]model.FileOperate{
		"done": {Type: "copy", TotalSize: 10, ProcessedSize: 10, Finished: true},
	})
	NewNotifyService(nil).SendFileOperateNotify(true)

	if len(OpStrArr) != 0 || queuedTask("done") {
		t.Errorf("the finished task is still queued (%v)", OpStrArr)
	}
}

// A task whose destination already has all its size (what CheckFileStatus counts) has not necessarily run: it ends when its operation says so.
func TestTheNotifierWaitsForTheOperationNotForTheSizes(t *testing.T) {
	useMessageBus(t, true)
	useFileQueue(t, []string{"counted"}, map[string]model.FileOperate{
		"counted": {Type: "copy", TotalSize: 10, ProcessedSize: 10},
	})

	NewNotifyService(nil).SendFileOperateNotify(true)

	if !queuedTask("counted") {
		t.Error("the task was taken out of the queue before its operation ended")
	}
}

func TestTheNotifierReportsAFailedTaskAsFailed(t *testing.T) {
	events := useMessageBus(t, true)
	root := t.TempDir()
	src := filepath.Join(root, "Photos")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "missing"), filepath.Join(src, "broken.jpg")); err != nil {
		t.Fatal(err)
	}
	useFileQueue(t, []string{"failing"}, map[string]model.FileOperate{
		"failing": {Type: "copy", TotalSize: 1, To: t.TempDir(), Item: []model.FileItem{{From: src}}},
	})
	FileOperate("failing")

	NewNotifyService(nil).SendFileOperateNotify(true)

	select {
	case event := <-events:
		if !strings.Contains(event, `\"status\":\"FAILED\"`) || !strings.Contains(event, `\"error\":\"copy_failed\"`) || !strings.Contains(event, `\"finished\":true`) {
			t.Errorf("published %s, want the task finished, FAILED, with the error copy_failed", event)
		}
	case <-time.After(5 * time.Second):
		t.Error("nothing was published")
	}
}
