package v1

import (
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fleet"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

// GetFleet answers the role of this box and the state of the role it plays.
//
// @Summary fleet: the role and its state
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet [get]
func GetFleet(ctx echo.Context) error {
	return ok(ctx, fleet.Default.Status())
}

// PutFleetMaster turns the master role on at an address, or off. Disabling
// closes every tunnel and forgets the boxes.
//
// @Summary fleet: become the master, or stop being one
// @Accept application/json
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/master [put]
func PutFleetMaster(ctx echo.Context) error {
	var body struct {
		Enabled bool   `json:"enabled"`
		Address string `json:"address"`
	}
	if err := ctx.Bind(&body); err != nil {
		return bad(ctx, err)
	}
	var err error
	if body.Enabled {
		err = fleet.Default.EnableMaster(body.Address)
	} else {
		err = fleet.Default.DisableMaster()
	}
	if err != nil && body.Enabled {
		return bad(ctx, err)
	}
	if err != nil {
		return oops(ctx, err)
	}
	return ok(ctx, fleet.Default.Status())
}

// PostFleetBoxes enrols a box and hands over its code once. self enrols this
// box itself.
//
// @Summary fleet: add a box, get its enrolment code
// @Accept application/json
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/boxes [post]
func PostFleetBoxes(ctx echo.Context) error {
	var body struct {
		Name string `json:"name"`
		URL  string `json:"url"`
		Self bool   `json:"self"`
	}
	if err := ctx.Bind(&body); err != nil {
		return bad(ctx, err)
	}
	box, code, err := fleet.Default.AddBox(body.Name, body.URL, body.Self)
	if errors.Is(err, fleet.ErrMasterOff) {
		return ctx.JSON(http.StatusConflict, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	if err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, map[string]any{"box": boxStatus(box), "code": code.String()})
}

// PatchFleetBox renames a box or changes its dashboard link.
//
// @Summary fleet: rename a box or change its address
// @Accept application/json
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/boxes/{id} [patch]
func PatchFleetBox(ctx echo.Context) error {
	var body struct {
		Name *string `json:"name"`
		URL  *string `json:"url"`
	}
	if err := ctx.Bind(&body); err != nil {
		return bad(ctx, err)
	}
	err := fleet.Default.RenameBox(ctx.Param("id"), body.Name, body.URL)
	return boxResult(ctx, err)
}

// DeleteFleetBox revokes a box.
//
// @Summary fleet: remove a box
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/boxes/{id} [delete]
func DeleteFleetBox(ctx echo.Context) error {
	return boxResult(ctx, fleet.Default.RemoveBox(ctx.Param("id")))
}

// PostFleetBoxCode gives a box a fresh code; the old one stops working.
//
// @Summary fleet: regenerate a box's code
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/boxes/{id}/code [post]
func PostFleetBoxCode(ctx echo.Context) error {
	code, err := fleet.Default.RotateCode(ctx.Param("id"))
	if err != nil {
		return boxResult(ctx, err)
	}
	return ok(ctx, map[string]any{"code": code.String()})
}

// FleetRelay forwards a request into a box's tunnel and answers what the box
// answered, as it is. Only the routes of the table pass; everything else is 403.
//
// @Summary fleet: relay one of a box's own routes
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/boxes/{id}/relay/{path} [get]
func FleetRelay(ctx echo.Context) error {
	req := ctx.Request()
	path := "/" + strings.TrimPrefix(ctx.Param("*"), "/")
	var body []byte
	if req.Body != nil {
		limited := http.MaxBytesReader(ctx.Response().Writer, req.Body, fleet.MaxBody)
		read, err := io.ReadAll(limited)
		if err != nil {
			return ctx.JSON(http.StatusRequestEntityTooLarge, model.Result{Success: common_err.CLIENT_ERROR, Message: "the request body is too big to relay"})
		}
		body = read
	}
	res, err := fleet.Default.Relay(req.Context(), ctx.Param("id"), req.Method, path, req.URL.RawQuery, req.Header.Get(echo.HeaderContentType), body)
	if err != nil {
		return ctx.JSON(relayStatus(err), model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}
	ctx.Response().Header().Set("Cache-Control", "no-store")
	// the Content-Type is the box's; do not let a browser sniff past it
	ctx.Response().Header().Set("X-Content-Type-Options", "nosniff")
	contentType := res.ContentType
	if contentType == "" {
		contentType = echo.MIMEOctetStream
	}
	return ctx.Blob(res.Status, contentType, res.Body)
}

// PutFleetMember joins (or re-joins) a fleet from a pasted code.
//
// @Summary fleet: join a fleet
// @Accept application/json
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/member [put]
func PutFleetMember(ctx echo.Context) error {
	var body struct {
		Code string `json:"code"`
	}
	if err := ctx.Bind(&body); err != nil {
		return bad(ctx, err)
	}
	if err := fleet.Default.Join(body.Code); err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, fleet.Default.Status())
}

// DeleteFleetMember leaves the fleet.
//
// @Summary fleet: leave the fleet
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/member [delete]
func DeleteFleetMember(ctx echo.Context) error {
	if err := fleet.Default.Leave(); err != nil {
		return oops(ctx, err)
	}
	return ok(ctx, fleet.Default.Status())
}

// FleetTunnel is the box's end of a tunnel: a WebSocket, outside the JWT group,
// authorised by the box's key.
func FleetTunnel(ctx echo.Context) error {
	fleet.Default.HandleTunnel(ctx.Response(), ctx.Request())
	return nil
}

// relayStatus maps a relay error to its HTTP status.
func relayStatus(err error) int {
	switch {
	case errors.Is(err, fleet.ErrNotAllowed):
		return http.StatusForbidden
	case errors.Is(err, fleet.ErrNoBox):
		return http.StatusNotFound
	case errors.Is(err, fleet.ErrOffline), errors.Is(err, fleet.ErrBusy), errors.Is(err, fleet.ErrMasterOff):
		return http.StatusServiceUnavailable
	case errors.Is(err, fleet.ErrTimeout):
		return http.StatusGatewayTimeout
	default:
		return http.StatusBadGateway
	}
}

// boxResult answers a box status, or maps a "no such box" to 404.
func boxResult(ctx echo.Context, err error) error {
	if errors.Is(err, fleet.ErrNoBox) {
		return ctx.JSON(http.StatusNotFound, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	if errors.Is(err, fleet.ErrMasterOff) {
		return ctx.JSON(http.StatusConflict, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}
	if err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, fleet.Default.Status())
}

// boxStatus is the public view of a freshly added box: never its key.
func boxStatus(b fleet.Box) fleet.BoxStatus {
	return fleet.BoxStatus{ID: b.ID, Name: b.Name, URL: b.URL, Self: b.Self}
}

func ok(ctx echo.Context, data any) error {
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}

func bad(ctx echo.Context, err error) error {
	return ctx.JSON(http.StatusBadRequest, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
}

func oops(ctx echo.Context, err error) error {
	return ctx.JSON(http.StatusInternalServerError, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
}
