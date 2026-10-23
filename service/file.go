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

// FileQueueLock guards FileQueue and OpStrArr together: a task is in FileQueue while its id is in OpStrArr.
var FileQueueLock sync.Mutex

// fileMoveRename is how a move is first tried, a var for the tests that make it cross two disks.
var fileMoveRename = os.Rename

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

func FileOperate(k string) {
	list, ok := FileQueue.Load(k)
	if !ok {
		return
	}

	temp := list.(model.FileOperate)
	// fail logs every failure and keeps the first one's code for the notifier
	fail := func(from string, err error) {
		logger.Error("file "+temp.Type+" failed", zap.String("from", from), zap.String("to", temp.To), zap.Error(err))
		switch {
		case temp.Error != "":
		case errors.Is(err, errSamePath):
			temp.Error = "same_path"
		case errors.Is(err, file.ErrHoldsMountPoint):
			temp.Error = "mount_point"
		default:
			temp.Error = temp.Type + "_failed"
		}
	}
	for i := 0; i < len(temp.Item); i++ {
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
				if err = file.CopyDir(v.From, temp.To, temp.Style); err == nil {
					err = file.RemoveAllNoCross(v.From)
				} else {
					file.RemoveAllNoCross(target)
				}
			}
			if err != nil {
				fail(v.From, err)
			}
		} else if temp.Type == "copy" {
			if err := file.CopyDir(v.From, temp.To, temp.Style); err != nil {
				fail(v.From, err)
			}
		}
	}
	temp.Finished = true
	FileQueueLock.Lock()
	defer FileQueueLock.Unlock()
	if _, ok := FileQueue.Load(k); ok { // a task cancelled meanwhile stays out of the queue
		FileQueue.Store(k, temp)
	}
}

func ExecOpFile() {
	FileQueueLock.Lock()
	defer FileQueueLock.Unlock()
	if len(OpStrArr) > 0 {
		go FileOperate(OpStrArr[0])
	}
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
