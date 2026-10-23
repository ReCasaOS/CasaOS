/*
 * @Author: LinkLeong link@icewhale.com
 * @Date: 2021-12-20 14:15:46
 * @LastEditors: LinkLeong
 * @LastEditTime: 2022-07-04 16:18:23
 * @FilePath: /CasaOS/service/file.go
 * @Description:
 * @Website: https://www.casaos.io
 * Copyright (c) 2022 by icewhale, All Rights Reserved.
 */
package service

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"github.com/moby/sys/mountinfo"
	"go.uber.org/zap"
)

var FileQueue sync.Map

var OpStrArr []string

// FileQueueLock guards FileQueue and OpStrArr together (a task is in FileQueue while its id is in OpStrArr), and fileOperateCancels.
var FileQueueLock sync.Mutex

// fileOperateCancels stops the operation of a running task, by its id: the queue runs one at a time, and starts none while one is in it.
var fileOperateCancels = map[string]context.CancelFunc{}

// fileMoveRename is how a move is first tried, a var for the tests that make it cross two disks.
var fileMoveRename = os.Rename

// fileCopyDir is how a task copies, a var for the tests that hold a copy while it runs.
var fileCopyDir = file.CopyDirContext

var errSamePath = errors.New("the destination is the source or holds it")

type reader struct {
	ctx context.Context
	r   io.Reader
}

// NewReader wraps an io.Reader to handle context cancellation.
//
// Context state is checked BEFORE every Read.
func NewReader(ctx context.Context, r io.Reader) io.Reader {
	if r, ok := r.(*reader); ok && ctx == r.ctx {
		return r
	}
	return &reader{ctx: ctx, r: r}
}

func (r *reader) Read(p []byte) (n int, err error) {
	select {
	case <-r.ctx.Done():
		return 0, r.ctx.Err()
	default:
		return r.r.Read(p)
	}
}

type writer struct {
	ctx context.Context
	w   io.Writer
}

type copier struct {
	writer
}

func NewWriter(ctx context.Context, w io.Writer) io.Writer {
	if w, ok := w.(*copier); ok && ctx == w.ctx {
		return w
	}
	return &copier{writer{ctx: ctx, w: w}}
}

// Write implements io.Writer, but with context awareness.
func (w *writer) Write(p []byte) (n int, err error) {
	select {
	case <-w.ctx.Done():
		return 0, w.ctx.Err()
	default:
		return w.w.Write(p)
	}
}

// FileOperate runs the queued task k; the queue runs its tasks through ExecOpFile.
func FileOperate(k string) {
	ctx, cancel := context.WithCancel(context.Background())
	FileQueueLock.Lock()
	fileOperateCancels[k] = cancel
	FileQueueLock.Unlock()
	fileOperate(ctx, k)
}

// fileOperate runs the task k, registered in fileOperateCancels with the cancel of ctx; once it has stopped, the next task starts.
func fileOperate(ctx context.Context, k string) {
	finished := false
	var temp model.FileOperate
	defer func() {
		panicked := recover()
		if panicked != nil {
			logger.Error("file operation panicked", zap.String("id", k), zap.Any("panic", panicked), zap.Stack("stack"))
		}
		FileQueueLock.Lock()
		if cancel := fileOperateCancels[k]; cancel != nil {
			cancel()
		}
		delete(fileOperateCancels, k)
		if task, ok := FileQueue.Load(k); ok && panicked != nil { // what it did is not known: it failed
			failed := task.(model.FileOperate)
			failed.Finished, failed.Error = true, "internal"
			FileQueue.Store(k, failed)
		} else if ok && finished { // a task cancelled meanwhile stays out of the queue
			FileQueue.Store(k, temp)
		}
		FileQueueLock.Unlock()
		ExecOpFile()
	}()
	list, ok := FileQueue.Load(k)
	if !ok {
		return
	}

	temp = list.(model.FileOperate)
	// fail logs every failure and keeps the first one's code for the notifier
	fail := func(from string, err error) {
		if ctx.Err() != nil {
			logger.Info("file "+temp.Type+" cancelled", zap.String("from", from), zap.String("to", temp.To))
			return
		}
		logger.Error("file "+temp.Type+" failed", zap.String("from", from), zap.String("to", temp.To), zap.Error(err))
		switch {
		case temp.Error != "":
		case errors.Is(err, errSamePath):
			temp.Error = "same_path"
		case errors.Is(err, file.ErrHoldsMountPoint):
			temp.Error = "mount_point"
		case errors.Is(err, file.ErrNotRegular):
			temp.Error = "not_regular"
		default:
			temp.Error = temp.Type + "_failed"
		}
	}
	for i := 0; i < len(temp.Item) && ctx.Err() == nil; i++ {
		v := temp.Item[i]
		target := temp.To + "/" + v.From[strings.LastIndex(v.From, "/")+1:]
		if file.Overlap(v.From, temp.To) { // the route refuses these: never remove or overwrite the source
			fail(v.From, errSamePath)
			continue
		}
		if temp.Type == "move" {
			if !file.CheckNotExist(target) {
				if temp.Style == "skip" {
					temp.Item[i].Finished = true
					continue
				}
				if err := file.RemoveAllNoCross(target); err != nil {
					fail(v.From, err)
					continue
				}
			}
			// a rename on the same disk; on another one a copy, and the source is removed only once all of it is copied
			err := fileMoveRename(v.From, target)
			if errors.Is(err, syscall.EXDEV) {
				// a disk mounted in the source stops its removal: then it is not copied either, rather than left whole in two places
				if err = file.CheckNoMountPoint(v.From); err == nil {
					if err = fileCopyDir(ctx, v.From, temp.To, temp.Style); err == nil {
						err = file.RemoveAllNoCross(v.From)
					} else {
						file.RemoveAllNoCross(target)
					}
				}
			}
			if err != nil {
				fail(v.From, err)
			}
		} else if temp.Type == "copy" {
			_, statErr := os.Lstat(target) // a link to nothing there is there too
			existed := statErr == nil
			if err := fileCopyDir(ctx, v.From, temp.To, temp.Style); err != nil {
				if ctx.Err() != nil && !existed {
					file.RemoveAllNoCross(target) // the half copy of a cancelled task
				}
				fail(v.From, err)
			}
		}
	}
	temp.Finished = true
	finished = true
}

// CancelFileOperate takes the task id ("0": every task) out of the queue and stops its operation; the next task starts once that operation has stopped.
func CancelFileOperate(id string) {
	FileQueueLock.Lock()
	defer FileQueueLock.Unlock()
	running := ""
	if len(OpStrArr) > 0 {
		running = OpStrArr[0]
	}
	waiting := []string{}
	for _, v := range OpStrArr {
		if id != "0" && v != id {
			waiting = append(waiting, v)
		}
	}
	OpStrArr = waiting
	FileQueue.Range(func(k, _ interface{}) bool {
		if id == "0" || k == id {
			FileQueue.Delete(k)
			if cancel := fileOperateCancels[k.(string)]; cancel != nil {
				cancel()
			}
		}
		return true
	})
	if len(OpStrArr) > 0 && OpStrArr[0] != running {
		go ExecOpFile()
	}
}

// ExecOpFile starts the task at the head of the queue, unless an operation is still in it (a cancelled one may still be stopping: two at once could write
// into the same folder) or that task has already run.
func ExecOpFile() {
	FileQueueLock.Lock()
	defer FileQueueLock.Unlock()
	if len(OpStrArr) == 0 || len(fileOperateCancels) > 0 {
		return
	}
	k := OpStrArr[0]
	if task, ok := FileQueue.Load(k); !ok || task.(model.FileOperate).Finished {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	fileOperateCancels[k] = cancel // before the goroutine: no second start in between
	go fileOperate(ctx, k)
}

// file move or copy and send notify
func CheckFileStatus() {
	for {
		FileQueueLock.Lock()
		ids := append([]string(nil), OpStrArr...)
		FileQueueLock.Unlock()
		if len(ids) == 0 {
			return
		}
		for _, v := range ids {
			var total int64 = 0
			item, ok := FileQueue.Load(v)
			if !ok {
				continue
			}
			temp := item.(model.FileOperate)
			for i := 0; i < len(temp.Item); i++ {
				if !temp.Item[i].Finished {
					size, err := file.GetFileOrDirSize(temp.To + "/" + filepath.Base(temp.Item[i].From))
					if err != nil {
						continue
					}
					temp.Item[i].ProcessedSize = size
					if size == temp.Item[i].Size {
						temp.Item[i].Finished = true
					}
					total += size
				} else {
					total += temp.Item[i].ProcessedSize
				}
			}
			temp.ProcessedSize = total
			FileQueueLock.Lock()
			if current, ok := FileQueue.Load(v); ok { // still queued: only the progress, FileOperate's outcome stays
				task := current.(model.FileOperate)
				task.Item, task.ProcessedSize = temp.Item, temp.ProcessedSize
				FileQueue.Store(v, task)
			}
			FileQueueLock.Unlock()
		}
		time.Sleep(time.Second * 3)
	}
}
func IsMounted(path string) bool {
	mounted, _ := mountinfo.Mounted(path)
	if mounted {
		return true
	}
	connections := MyService.Connections().GetConnectionsList()
	for _, v := range connections {
		if v.MountPoint == path {
			return true
		}
	}
	return false
}
