package v1

import (
	"errors"
	"net/http"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/fileaccess"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// scope is what this request may reach (pkg/fileaccess): the box's roots for the owner and
// the box's own services, an account's folders for a user. Read once per request.
func scope(ctx echo.Context) (*fileaccess.Scope, error) {
	if s, ok := ctx.Get("fileScope").(*fileaccess.Scope); ok {
		return s, nil
	}
	internal := external.IsInternalRequest(ctx.RealIP(), ctx.Request().Header.Get(echo.HeaderAuthorization), config.CommonInfo.RuntimePath)
	s, err := fileaccess.For(config.CommonInfo.RuntimePath, ctx.Request().Header.Get("user_id"), internal)
	if err != nil {
		return nil, err
	}
	ctx.Set("fileScope", s)
	return s, nil
}

// managed: every client path of a file request, checked against [file]
// ManagementRoots (pkg/fileroots) and, for an account, its folders (a writable one
// for fileaccess.Write), and replaced with its canonical spelling.
//
// An account is refused here: what it reaches goes through the root of its folder
// (pinned, rooted), never by a path a later rename can move.
func managed(ctx echo.Context, intent fileaccess.Intent, paths ...*string) error {
	s, err := scope(ctx)
	if err != nil {
		return err
	}
	if s.User() {
		return fileaccess.ErrPathNotRooted
	}
	return s.Check(intent, paths...)
}

// policy: the paths of a request held to what the account may reach (Scope.Check), for a
// request whose work then goes through the root of its folders (a queued copy or move).
func policy(ctx echo.Context, intent fileaccess.Intent, paths ...*string) error {
	s, err := scope(ctx)
	if err != nil {
		return err
	}
	return s.Check(intent, paths...)
}

// managedEntry: paths deleted, renamed or moved as entries: their folder is checked,
// not what they lead to (a link out is removed as a link); a root, or an account's
// folder itself, is refused. A write.
func managedEntry(ctx echo.Context, paths ...*string) error {
	s, err := scope(ctx)
	if err != nil {
		return err
	}
	if s.User() {
		return fileaccess.ErrPathNotRooted
	}
	return s.CheckEntries(paths...)
}

// managedTemp: a folder the server derives (an upload's chunks), checked like a
// client path written to; tempDir keeps its spelling.
func managedTemp(ctx echo.Context, tempDir string) error {
	temp := strings.TrimSuffix(tempDir, "/")
	return managed(ctx, fileaccess.Write, &temp)
}

// GetFileRoots answers the configured [file] ManagementRoots (empty: the file API is
// unconfined and the dashboard keeps /DATA). For an account, its folders, and the ones
// it may write.
func GetFileRoots(ctx echo.Context) error {
	s, err := scope(ctx)
	if err != nil {
		return outsideRoots(ctx, err)
	}
	data := map[string][]string{"roots": {}}
	if s.User() {
		data["roots"], data["writable"] = s.Readable(), s.Writable()
	} else if fileroots.Default != nil {
		if p := fileroots.Default.Paths(); len(p) > 0 {
			data["roots"] = p
		}
	}
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    data,
	})
}

// outsideRoots answers a path the file API may not touch; the reason in Data
// too (the dashboard shows that one). An account whose role is not known yet is
// told to try again; one that is gone, to sign in.
func outsideRoots(ctx echo.Context, err error) error {
	status := http.StatusForbidden
	switch {
	case errors.Is(err, fileaccess.ErrRoleUnknown):
		status = http.StatusServiceUnavailable
	case errors.Is(err, fileaccess.ErrAccountGone):
		status = http.StatusUnauthorized
	}
	return ctx.JSON(status, model.Result{
		Success: common_err.INSUFFICIENT_PERMISSIONS,
		Message: err.Error(),
		Data:    err.Error(),
	})
}
