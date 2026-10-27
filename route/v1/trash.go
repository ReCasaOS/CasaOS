package v1

import (
	"errors"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
	"github.com/ReCasaOS/CasaOS/pkg/trash"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"github.com/labstack/echo/v4"
)

// trashRef names an item of a trash, as GET /v1/file/trash gives it.
type trashRef struct {
	Trash string `json:"trash"`
	Name  string `json:"name"`
}

// trashRefused is a path that cannot go to the trash, and why.
type trashRefused struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// inRoots: the trash shows, restores and deletes only what was below the
// file API's roots ([file] ManagementRoots); unconfined, everything.
func inRoots(path string) bool {
	r := fileroots.Default
	if !r.Confined() {
		return true
	}
	for _, root := range r.Paths() {
		if path == root || strings.HasPrefix(path, root+"/") {
			return true
		}
	}
	return false
}

// trashAllowed is inRoots for the trash package; nil (everything) unconfined.
func trashAllowed() trash.Allowed {
	if !fileroots.Default.Confined() {
		return nil
	}
	return inRoots
}

// moveToTrash puts every path in the trash of its disk. One that cannot go
// there (a read-only disk, a network one, a trash folder that is not ours)
// stays where it is, and the answer lists them (TRASH_UNAVAILABLE) with the
// others moved, so that the dashboard can offer a permanent delete of those
// only. A path already gone counts as done; any other error is answered once
// the other paths are moved. A folder that holds a mounted disk refuses the
// whole call first, as the permanent delete does.
func moveToTrash(ctx echo.Context, paths []string) error {
	for _, p := range paths {
		if err := file.CheckNoMountPoint(p); err != nil {
			if errors.Is(err, file.ErrHoldsMountPoint) {
				return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.MOUNTED_DIRECTIORIES, Message: common_err.GetMsg(common_err.MOUNTED_DIRECTIORIES), Data: err.Error()})
			}
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_DELETE_ERROR, Message: common_err.GetMsg(common_err.FILE_DELETE_ERROR), Data: err.Error()})
		}
	}
	refused := []trashRefused{}
	var failed error
	now := time.Now()
	for _, p := range paths {
		err := trash.Put(p, now)
		switch {
		case err == nil, errors.Is(err, fs.ErrNotExist): // gone already (another client, a list read earlier)
		case errors.Is(err, trash.ErrUnavailable):
			refused = append(refused, trashRefused{Path: p, Reason: err.Error()})
		case failed == nil:
			failed = err
		}
	}
	if failed != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.FILE_DELETE_ERROR, Message: common_err.GetMsg(common_err.FILE_DELETE_ERROR), Data: failed.Error()})
	}
	if len(refused) > 0 {
		return ctx.JSON(http.StatusConflict, model.Result{Success: common_err.TRASH_UNAVAILABLE, Message: common_err.GetMsg(common_err.TRASH_UNAVAILABLE), Data: refused})
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// GetTrash lists the trash of every disk, the last deleted first, with the
// space it takes.
func GetTrash(ctx echo.Context) error {
	items := []trash.Item{}
	var size int64
	for _, it := range trash.List() {
		if inRoots(it.Path) {
			items = append(items, it)
			size += it.Size
		}
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: map[string]any{
		"items":          items,
		"size":           size,
		"retention_days": int(trash.Retention / (24 * time.Hour)),
	}})
}

// trashRefs reads the items of the body; false when it has answered the body is wrong.
func trashRefs(ctx echo.Context) ([]trashRef, bool) {
	refs := []trashRef{}
	if err := ctx.Bind(&refs); err != nil || len(refs) == 0 {
		_ = ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.INVALID_PARAMS, Message: common_err.GetMsg(common_err.INVALID_PARAMS)})
		return nil, false
	}
	return refs, true
}

func trashError(ctx echo.Context, err error) error {
	switch {
	case errors.Is(err, trash.ErrNotFound):
		return ctx.JSON(http.StatusNotFound, model.Result{Success: common_err.FILE_DOES_NOT_EXIST, Message: err.Error(), Data: err.Error()})
	case errors.Is(err, fileroots.ErrOutside), errors.Is(err, fileroots.ErrRoot), errors.Is(err, fileroots.ErrRootUnavailable):
		return outsideRoots(ctx, err)
	default:
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error(), Data: err.Error()})
	}
}

// RestoreTrash puts items back where they were and answers where they are now
// (another name when theirs is taken).
func RestoreTrash(ctx echo.Context) error {
	refs, ok := trashRefs(ctx)
	if !ok {
		return nil
	}
	check := func(dest string) (string, error) {
		if !inRoots(dest) {
			return "", fileroots.ErrOutside
		}
		return fileroots.Default.CheckEntry(dest)
	}
	restored := []string{}
	for _, r := range refs {
		to, err := trash.Restore(r.Trash, r.Name, trashAllowed(), check)
		if err != nil {
			return trashError(ctx, err)
		}
		restored = append(restored, to)
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: restored})
}

// DeleteTrash removes items for good.
func DeleteTrash(ctx echo.Context) error {
	refs, ok := trashRefs(ctx)
	if !ok {
		return nil
	}
	for _, r := range refs {
		if err := trash.Delete(r.Trash, r.Name, trashAllowed()); err != nil {
			return trashError(ctx, err)
		}
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS)})
}

// EmptyTrash removes everything in the trash.
func EmptyTrash(ctx echo.Context) error {
	n, err := trash.Empty(trashAllowed())
	if err != nil {
		return trashError(ctx, err)
	}
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: n})
}
