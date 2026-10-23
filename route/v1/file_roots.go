package v1

import (
	"net/http"

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

// outsideRoots answers a path the file API may not touch.
func outsideRoots(ctx echo.Context) error {
	return ctx.JSON(http.StatusForbidden, model.Result{
		Success: common_err.INSUFFICIENT_PERMISSIONS,
		Message: fileroots.ErrOutside.Error(),
	})
}
