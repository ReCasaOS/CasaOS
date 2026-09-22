package v1

import (
	"net/http"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/telemetry"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// GetTelemetry answers whether the anonymous statistics are on, whether the
// dashboard's notice was seen, and the heartbeat exactly as it would be sent now.
//
// @Summary anonymous statistics: state and preview
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/telemetry [get]
func GetTelemetry(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    telemetry.Default.Status(),
	})
}

// PutTelemetry sets enabled and notice_seen, each optional; other fields are
// ignored. It answers the new state, as GET does.
//
// @Summary anonymous statistics: turn them on or off, mark the notice seen
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/telemetry [put]
func PutTelemetry(ctx echo.Context) error {
	var body struct {
		Enabled    *bool `json:"enabled"`
		NoticeSeen *bool `json:"notice_seen"`
	}
	if err := ctx.Bind(&body); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	status, err := telemetry.Default.Update(body.Enabled, body.NoticeSeen)
	if err != nil {
		return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    status,
	})
}
