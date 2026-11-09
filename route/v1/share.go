package v1

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fleet"
	"github.com/ReCasaOS/CasaOS/pkg/share"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// Share is the public sharing the routes answer for; tests replace it.
var Share = share.Default

// GetShare answers the state of public sharing: the name, the apps and their addresses, the certificate and the tunnel.
//
// @Summary share: the state of public sharing
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share [get]
func GetShare(ctx echo.Context) error {
	return ok(ctx, Share.Status(ctx.Request().Context()))
}

// PutShareName claims this box's public name at the relay; accept_tos confirms the terms of Let's Encrypt.
//
// @Summary share: claim the public name
// @Accept application/json
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/name [put]
func PutShareName(ctx echo.Context) error {
	var body struct {
		Name      string `json:"name"`
		AcceptTOS bool   `json:"accept_tos"`
	}
	if err := ctx.Bind(&body); err != nil {
		return bad(ctx, err)
	}
	c, cancel := detached(ctx)
	defer cancel()
	if err := Share.SetName(c, body.Name, body.AcceptTOS); err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, Share.Status(c))
}

// PutShareApp publishes an installed app at its own address under the box's name.
//
// @Summary share: share an app
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/apps/{name} [put]
func PutShareApp(ctx echo.Context) error {
	c, cancel := detached(ctx)
	defer cancel()
	if err := Share.Share(c, ctx.Param("name")); err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, Share.Status(c))
}

// DeleteShareApp stops sharing an app.
//
// @Summary share: stop sharing an app
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/apps/{name} [delete]
func DeleteShareApp(ctx echo.Context) error {
	c, cancel := detached(ctx)
	defer cancel()
	if err := Share.Unshare(c, ctx.Param("name")); err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, Share.Status(c))
}

// DeleteShareApps stops sharing every app; the name stays.
//
// @Summary share: stop sharing every app
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/apps [delete]
func DeleteShareApps(ctx echo.Context) error {
	c, cancel := detached(ctx)
	defer cancel()
	if err := Share.StopAll(c); err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, Share.Status(c))
}

// shareFail maps a sharing error to its answer. The messages of the mapped errors are the package's own: none
// carries a key, a signature or what the relay sent back. Anything else is a 500 with a generic message.
func shareFail(ctx echo.Context, err error) error {
	status, msg, reason := http.StatusInternalServerError, "public sharing failed", ""
	var taken share.ErrLabelTaken
	var busy share.BusyError
	switch {
	case errors.Is(err, share.ErrBadName), errors.Is(err, share.ErrTOS), errors.Is(err, share.ErrBadLabel):
		status, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, share.ErrNameSet), errors.Is(err, share.ErrNameTaken), errors.Is(err, share.ErrNameReserved), errors.Is(err, share.ErrNameRevoked),
		errors.Is(err, share.ErrNameQuarantine), errors.Is(err, share.ErrNoName), errors.Is(err, share.ErrNoPort), errors.As(err, &taken):
		status, msg = http.StatusConflict, err.Error()
	case errors.Is(err, share.ErrUnknownApp):
		status, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, share.ErrNoRelay):
		status, msg, reason = http.StatusServiceUnavailable, err.Error(), "remote-access"
	case errors.Is(err, share.ErrRelayNoShare):
		status, msg, reason = http.StatusServiceUnavailable, err.Error(), "relay"
	case errors.Is(err, share.ErrGatewayNoShare):
		status, msg, reason = http.StatusServiceUnavailable, err.Error(), "gateway"
	case errors.Is(err, share.ErrWaitMaster):
		status, msg, reason = http.StatusServiceUnavailable, share.ErrWaitMaster.Error(), "master" // not err: it may wrap a cause
	case errors.As(err, &busy):
		status, msg = http.StatusTooManyRequests, busy.Error()
		if busy.RetryAfter > 0 {
			ctx.Response().Header().Set("Retry-After", strconv.Itoa(int(max(1, (busy.RetryAfter+time.Second-1)/time.Second)))) // rounded up
		}
	case errors.Is(err, fleet.ErrRelayUnreachable):
		status, msg = http.StatusBadGateway, err.Error()
	}
	success := common_err.CLIENT_ERROR
	if status >= http.StatusInternalServerError {
		success = common_err.SERVICE_ERROR
	}
	result := model.Result{Success: success, Message: msg}
	if reason != "" {
		result.Data = map[string]string{"reason": reason}
	}
	return ctx.JSON(status, result)
}
