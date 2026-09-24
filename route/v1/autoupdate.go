package v1

import (
	"errors"
	"net/http"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/autoupdate"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// GetAutoUpdate answers whether automatic updates are on, their window, their
// state, the release they aim at and their last attempt.
//
// @Summary automatic updates: settings and state
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/autoupdate [get]
func GetAutoUpdate(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    autoupdate.Default.Status(),
	})
}

// PutAutoUpdate sets enabled, window_start and window_end, and resumes a paused
// release with resume, each optional; other fields are ignored. A window that is
// not HH:MM, or shorter than an hour, is a 400. It answers the new state, as GET
// does.
//
// @Summary automatic updates: turn them on or off, set the window, resume
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/autoupdate [put]
func PutAutoUpdate(ctx echo.Context) error {
	var change autoupdate.Change
	if err := ctx.Bind(&change); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	status, err := autoupdate.Default.Update(change)
	if errors.Is(err, autoupdate.ErrWindow) {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    status,
	})
}
