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
	var guard share.ErrGuard
	switch {
	case errors.Is(err, share.ErrBadName), errors.Is(err, share.ErrTOS), errors.Is(err, share.ErrBadLabel):
		status, msg = http.StatusBadRequest, err.Error()
	case errors.Is(err, share.ErrNameSet), errors.Is(err, share.ErrNameTaken), errors.Is(err, share.ErrNameReserved), errors.Is(err, share.ErrNameRevoked),
		errors.Is(err, share.ErrNameQuarantine), errors.Is(err, share.ErrNoName), errors.Is(err, share.ErrNoPort), errors.As(err, &taken):
		status, msg = http.StatusConflict, err.Error()
	case errors.As(err, &guard):
		status, msg = http.StatusBadRequest, guard.Code
	case errors.Is(err, share.ErrGuardPush):
		msg = err.Error() // a 500, with its plain message
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

// guardAnswer is a guard view, or the error shareFail maps.
func guardAnswer(ctx echo.Context, v share.GuardView, err error) error {
	if err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, v)
}

func badBody(ctx echo.Context) error { return shareFail(ctx, share.ErrGuard{Code: "bad_request"}) }

// GetShareGuard answers the protection of shared apps: the box's policy, each shared app's access, the people
// and what the Gateway has seen. No code and no hash of a code is in it.
//
// @Summary share: the protection of shared apps
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/guard [get]
func GetShareGuard(ctx echo.Context) error {
	c, cancel := detached(ctx)
	defer cancel()
	v, err := Share.GuardView(c)
	return guardAnswer(ctx, v, err)
}

// PutShareGuardProtection turns the protection of shared apps on or off.
//
// @Summary share: protection on or off
// @Accept application/json
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/guard/protection [put]
func PutShareGuardProtection(ctx echo.Context) error {
	var body struct {
		On *bool `json:"on"`
	}
	if err := ctx.Bind(&body); err != nil || body.On == nil { // an absent "on" must not mean off
		return badBody(ctx)
	}
	c, cancel := detached(ctx)
	defer cancel()
	v, err := Share.SetProtection(c, *body.On)
	return guardAnswer(ctx, v, err)
}

// PutShareGuardBox replaces the box's policy.
//
// @Summary share: the box's protection policy
// @Accept application/json
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/guard/box [put]
func PutShareGuardBox(ctx echo.Context) error {
	var p share.Policy
	if err := ctx.Bind(&p); err != nil {
		return badBody(ctx)
	}
	c, cancel := detached(ctx)
	defer cancel()
	v, err := Share.SetBoxPolicy(c, p)
	return guardAnswer(ctx, v, err)
}

// PutShareGuardApp sets who may enter a shared app, and its own settings.
//
// @Summary share: the protection of one app
// @Accept application/json
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/guard/apps/{name} [put]
func PutShareGuardApp(ctx echo.Context) error {
	var body struct {
		Access   string                `json:"access"`
		Override *share.PolicyOverride `json:"override"`
	}
	if err := ctx.Bind(&body); err != nil {
		return badBody(ctx)
	}
	c, cancel := detached(ctx)
	defer cancel()
	v, err := Share.SetAppGuard(c, ctx.Param("name"), body.Access, body.Override)
	return guardAnswer(ctx, v, err)
}

type guardPersonBody struct {
	Name string   `json:"name"`
	Apps []string `json:"apps"`
}

// PostShareGuardPerson adds a person and answers their access code, which is shown this once.
//
// @Summary share: add a person
// @Accept application/json
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/guard/people [post]
func PostShareGuardPerson(ctx echo.Context) error {
	var body guardPersonBody
	if err := ctx.Bind(&body); err != nil {
		return badBody(ctx)
	}
	c, cancel := detached(ctx)
	defer cancel()
	person, code, err := Share.AddPerson(c, body.Name, body.Apps)
	if err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, map[string]any{"person": person, "code": code})
}

// PutShareGuardPerson renames a person and sets the apps they may enter.
//
// @Summary share: change a person
// @Accept application/json
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/guard/people/{id} [put]
func PutShareGuardPerson(ctx echo.Context) error {
	var body guardPersonBody
	if err := ctx.Bind(&body); err != nil {
		return badBody(ctx)
	}
	c, cancel := detached(ctx)
	defer cancel()
	person, err := Share.UpdatePerson(c, ctx.Param("id"), body.Name, body.Apps)
	if err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, person)
}

// PostShareGuardPersonCode gives a person a new code: the old one stops working.
//
// @Summary share: a new code for a person
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/guard/people/{id}/code [post]
func PostShareGuardPersonCode(ctx echo.Context) error {
	c, cancel := detached(ctx)
	defer cancel()
	code, err := Share.NewPersonCode(c, ctx.Param("id"))
	if err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, map[string]string{"code": code})
}

// DeleteShareGuardPerson removes a person: their code stops working.
//
// @Summary share: remove a person
// @Tags share
// @Security ApiKeyAuth
// @Success 204
// @Router /share/guard/people/{id} [delete]
func DeleteShareGuardPerson(ctx echo.Context) error {
	c, cancel := detached(ctx)
	defer cancel()
	if err := Share.DeletePerson(c, ctx.Param("id")); err != nil {
		return shareFail(ctx, err)
	}
	return ctx.NoContent(http.StatusNoContent)
}

// GetShareGuardEvents relays the Gateway's journal of refused requests, filtered by app (compose name) and reason.
//
// @Summary share: the journal of the protection
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/guard/events [get]
func GetShareGuardEvents(ctx echo.Context) error {
	limit, _ := strconv.Atoi(ctx.QueryParam("limit"))
	c, cancel := detached(ctx)
	defer cancel()
	out, err := Share.GuardEvents(c, ctx.QueryParam("app"), ctx.QueryParam("reason"), limit)
	if err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, out)
}

// GetShareGuardBans relays the Gateway's active bans.
//
// @Summary share: the active bans
// @Produce application/json
// @Tags share
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /share/guard/bans [get]
func GetShareGuardBans(ctx echo.Context) error {
	c, cancel := detached(ctx)
	defer cancel()
	out, err := Share.GuardBans(c)
	if err != nil {
		return shareFail(ctx, err)
	}
	return ok(ctx, out)
}

// DeleteShareGuardBan lifts a ban now. The key (an address or a range) arrives unescaped once from the router.
//
// @Summary share: lift a ban
// @Tags share
// @Security ApiKeyAuth
// @Success 204
// @Router /share/guard/bans/{key} [delete]
func DeleteShareGuardBan(ctx echo.Context) error {
	c, cancel := detached(ctx)
	defer cancel()
	if err := Share.Unban(c, ctx.Param("key")); err != nil {
		return shareFail(ctx, err)
	}
	return ctx.NoContent(http.StatusNoContent)
}
