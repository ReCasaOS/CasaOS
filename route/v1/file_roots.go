package v1

import (
	"net/http"
	"strings"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// managed: every client path of a file request, checked against
// [file] ManagementRoots (pkg/fileroots) and replaced with its canonical
// spelling. Unconfined: unchanged.
func managed(paths ...*string) error {
	return fileroots.Default.CheckEach(paths...)
}

// managedEntry: paths deleted or renamed as entries: their folder is checked,
// not what they lead to (a link out is removed as a link); a root is refused.
func managedEntry(paths ...*string) error {
	return fileroots.Default.CheckEntries(paths...)
}

// managedTemp: a folder the server derives (an upload's chunks), checked like
// a client path; tempDir keeps its spelling.
func managedTemp(tempDir string) error {
	temp := strings.TrimSuffix(tempDir, "/")
	return managed(&temp)
}

// GetFileRoots answers the configured [file] ManagementRoots. Empty: the
// file API is unconfined and the dashboard keeps /DATA.
func GetFileRoots(ctx echo.Context) error {
	roots := []string{}
	if fileroots.Default != nil {
		if p := fileroots.Default.Paths(); len(p) > 0 {
			roots = p
		}
	}
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    map[string][]string{"roots": roots},
	})
}

// outsideRoots answers a path the file API may not touch; the reason in Data
// too (the dashboard shows that one).
func outsideRoots(ctx echo.Context, err error) error {
	return ctx.JSON(http.StatusForbidden, model.Result{
		Success: common_err.INSUFFICIENT_PERMISSIONS,
		Message: err.Error(),
		Data:    err.Error(),
	})
}
