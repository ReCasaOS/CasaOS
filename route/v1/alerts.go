package v1

import (
	"errors"
	"net/http"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/alerts"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// GetAlerts answers the push alerts' channels, each by name, service and host
// and never by its URL, the categories, the disk threshold and the last send
// that failed.
//
// @Summary push alerts: channels and settings
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/alerts [get]
func GetAlerts(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    alerts.Default.Status(),
	})
}

// PutAlerts sets channels, categories and disk_threshold, each optional; other
// fields are ignored. channels replaces the list: an entry with an id and no
// url keeps its stored URL, a new entry needs a url. A URL Shoutrrr cannot
// parse, an unknown id or a threshold outside 50 to 99 is a 400. It answers
// the new settings, as GET does.
//
// @Summary push alerts: set the channels, categories and disk threshold
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/alerts [put]
func PutAlerts(ctx echo.Context) error {
	var change alerts.Change
	if err := ctx.Bind(&change); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	status, err := alerts.Default.Update(change)
	if errors.Is(err, alerts.ErrInvalid) {
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

// PostAlertsTest sends a test message to channel_id, or to every channel
// without it, and answers per channel {id, ok, error} once each has answered.
// An unknown channel_id is a 400.
//
// @Summary push alerts: send a test message
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/alerts/test [post]
func PostAlertsTest(ctx echo.Context) error {
	var body struct {
		ChannelID string `json:"channel_id"`
	}
	if err := ctx.Bind(&body); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	results, err := alerts.Default.Test(body.ChannelID)
	if err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    results,
	})
}
