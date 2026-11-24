package v1

import (
	"errors"
	"net/http"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/feeds"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// GetFeeds answers the footer's feeds: the switch, the feeds and their cached
// headlines. It never waits on a feed: a refresh that is due starts in the
// background (CasaOS#180).
//
// @Summary footer feeds: settings and headlines
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/feeds [get]
func GetFeeds(ctx echo.Context) error {
	return ctx.JSON(http.StatusOK, model.Result{
		Success: common_err.SUCCESS,
		Message: common_err.GetMsg(common_err.SUCCESS),
		Data:    feeds.Default.Status(),
	})
}

// PutFeeds sets {enabled, feeds}: at most ten http(s) feeds, else a 400. It
// answers as GET does.
//
// @Summary footer feeds: set the switch and the feeds
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/feeds [put]
func PutFeeds(ctx echo.Context) error {
	var change feeds.Config
	if err := ctx.Bind(&change); err != nil {
		return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	status, err := feeds.Default.Update(change)
	if errors.Is(err, feeds.ErrInvalid) {
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
