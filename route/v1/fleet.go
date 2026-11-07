package v1

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

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
	return ok(ctx, fleetStatus())
}

// statusView is GET /v1/fleet: fleet.Status and, over a relay, the relay and
// this box's place on its private network. Both are absent otherwise.
type statusView struct {
	fleet.Status
	Relay string   `json:"relay,omitempty"`
	Net   *netView `json:"net,omitempty"`
	// DefaultRelay is always there: it tells a core that knows the relay from an
	// older one, even for a box with no role.
	DefaultRelay string `json:"default_relay"`
}

type netView struct {
	Connected bool   `json:"connected"`
	Addr      string `json:"addr,omitempty"`
	Error     string `json:"error,omitempty"`
}

// fleetStatus is the status the fleet routes answer; the network is the box's
// cached live state, with a master's recorded address.
func fleetStatus() statusView {
	s := statusView{Status: fleet.Default.Status(), DefaultRelay: fleet.DefaultRelayURL}
	switch {
	case s.Master != nil && s.Master.Relay != "":
		s.Relay, s.Net = s.Master.Relay, &netView{Connected: s.Master.NetConnected, Addr: s.Master.NetAddr, Error: s.Master.NetError}
	case s.Member != nil && s.Member.Relay != "":
		s.Relay, s.Net = s.Member.Relay, &netView{Connected: s.Member.NetConnected, Error: s.Member.NetError}
	}
	return s
}

// relayWork bounds a change of private network: up to 45 s to join, 30 s for
// an address, and a relay change does both.
const relayWork = 2 * time.Minute

// detached is the request's context without its cancellation: the gateway's
// proxy cancels the request when the browser goes away, and a switch cut there
// would leave the box halfway into a network.
func detached(ctx echo.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx.Request().Context()), relayWork)
}

// PutFleetMaster turns the master role on, or off. On: over the relay given
// (the dashboard names the official one too), or in advanced mode at an
// address typed by hand; neither is a 400. Disabling closes every tunnel, forgets the
// boxes and, over a relay, deletes the fleet there and leaves its network.
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
		Relay   string `json:"relay"`
	}
	if err := ctx.Bind(&body); err != nil {
		return bad(ctx, err)
	}
	if !body.Enabled {
		if err := fleet.Default.DisableMaster(); err != nil {
			return oops(ctx, err)
		}
		return ok(ctx, fleetStatus())
	}
	var err error
	switch {
	case body.Address != "" && body.Relay != "":
		err = errors.New("give either a relay or a master address (advanced mode), not both")
	case body.Address != "":
		err = fleet.Default.EnableMaster(body.Address)
	case body.Relay == "":
		err = errors.New("give a relay or a master address (advanced mode)")
	default:
		c, cancel := detached(ctx)
		defer cancel()
		err = fleet.Default.EnableMasterRelay(c, body.Relay)
	}
	if err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, fleetStatus())
}

// PutFleetRelay moves the master to another relay: a new fleet there, this box
// on its network, and every box asked for a new code.
//
// @Summary fleet: change the master's relay
// @Accept application/json
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/relay [put]
func PutFleetRelay(ctx echo.Context) error {
	var body struct {
		Relay string `json:"relay"`
	}
	if err := ctx.Bind(&body); err != nil {
		return bad(ctx, err)
	}
	if body.Relay == "" {
		return bad(ctx, errors.New("the relay address is required"))
	}
	c, cancel := detached(ctx)
	defer cancel()
	if err := fleet.Default.ChangeRelay(c, body.Relay); err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, fleetStatus())
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

// PutFleetMember joins (or re-joins) a fleet from a pasted code. A box with
// remote access answers 409 until leave_remote confirms that joining turns it off.
//
// @Summary fleet: join a fleet
// @Accept application/json
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Param code body string true "the code pasted from the master"
// @Param leave_remote body bool false "confirms that joining turns this box's remote access off"
// @Success 200 {object} model.Result
// @Failure 409 {object} model.Result "this box has remote access: send leave_remote true to join anyway"
// @Router /fleet/member [put]
func PutFleetMember(ctx echo.Context) error {
	var body struct {
		Code        string `json:"code"`
		LeaveRemote bool   `json:"leave_remote"`
	}
	if err := ctx.Bind(&body); err != nil {
		return bad(ctx, err)
	}
	err := fleet.Default.Join(body.Code)
	if errors.Is(err, fleet.ErrRemoteOn) && body.LeaveRemote {
		// confirmed: remote access goes, then the join. Only now, so a bad code costs nothing.
		c, cancel := detached(ctx)
		defer cancel()
		if err = fleet.Default.DisableRemote(c); err == nil {
			err = fleet.Default.Join(body.Code)
		}
	}
	if err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, fleetStatus())
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
	return ok(ctx, fleetStatus())
}

// PutFleetRemote turns remote access on, over the relay given (the official one
// when empty), or off.
//
// @Summary fleet: turn remote access on or off
// @Accept application/json
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/remote [put]
func PutFleetRemote(ctx echo.Context) error {
	var body struct {
		Enabled bool   `json:"enabled"`
		Relay   string `json:"relay"`
	}
	if err := ctx.Bind(&body); err != nil {
		return bad(ctx, err)
	}
	c, cancel := detached(ctx)
	defer cancel()
	var err error
	if body.Enabled {
		err = fleet.Default.EnableRemote(c, body.Relay)
	} else {
		err = fleet.Default.DisableRemote(c)
	}
	if err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, fleetStatus())
}

// GetFleetRemote asks the relay for the devices on this box's network and, on a
// master, its boxes' addresses. The dashboard calls it on demand (the remote
// access page opening, a refresh button), never on a timer: the relay limits
// Bearer calls to 60 an hour per IP, shared with join keys.
//
// @Summary fleet: remote access devices and addresses
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/remote [get]
func GetFleetRemote(ctx echo.Context) error {
	v, err := fleet.Default.RemoteView(ctx.Request().Context())
	if err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, v)
}

// PostFleetRemoteDevice gives a one-time code a phone or PC joins the network with.
//
// @Summary fleet: add a phone or PC, get its code
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/remote/devices [post]
func PostFleetRemoteDevice(ctx echo.Context) error {
	code, err := fleet.Default.NewDeviceCode(ctx.Request().Context())
	if err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, code)
}

// DeleteFleetRemoteDevice takes a phone or PC off the network.
//
// @Summary fleet: remove a phone or PC
// @Produce application/json
// @Tags fleet
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /fleet/remote/devices/{id} [delete]
func DeleteFleetRemoteDevice(ctx echo.Context) error {
	if err := fleet.Default.RemoveDevice(ctx.Request().Context(), ctx.Param("id")); err != nil {
		return bad(ctx, err)
	}
	return ok(ctx, nil)
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
	return ok(ctx, fleetStatus())
}

// boxStatus is the public view of a freshly added box: never its key.
func boxStatus(b fleet.Box) fleet.BoxStatus {
	return fleet.BoxStatus{ID: b.ID, Name: b.Name, URL: b.URL, Self: b.Self}
}

func ok(ctx echo.Context, data any) error {
	return ctx.JSON(http.StatusOK, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: data})
}

func bad(ctx echo.Context, err error) error { return fail(ctx, http.StatusBadRequest, err) }

func oops(ctx echo.Context, err error) error { return fail(ctx, http.StatusInternalServerError, err) }

// fail answers err, with the status of a private-network or relay error, else
// status. The message is err's own: none carries a secret, a key or what the
// relay sent back (pkg/fleet), and a wrapped one keeps its context.
func fail(ctx echo.Context, status int, err error) error {
	msg := err.Error()
	var busy *fleet.RelayBusyError
	switch {
	case errors.Is(err, fleet.ErrOtherNetwork), errors.Is(err, fleet.ErrRelayQuota), errors.Is(err, fleet.ErrRelayAuth), errors.Is(err, fleet.ErrRelayFleetGone),
		errors.Is(err, fleet.ErrRemoteOn), errors.Is(err, fleet.ErrRemoteOff), errors.Is(err, fleet.ErrRemoteGone), errors.Is(err, fleet.ErrTooManyDeviceCodes), errors.Is(err, fleet.ErrRelayNoDevices):
		status = http.StatusConflict // ErrFleetFull and ErrFleetGone wrap the relay's quota and gone; ErrRemoteGone wraps ErrRelayFleetGone
	case errors.Is(err, fleet.ErrRelayFull):
		status = http.StatusServiceUnavailable
	case errors.Is(err, fleet.ErrNoSystemctl): // its own message: tailscale is there
		status = http.StatusServiceUnavailable
	case errors.Is(err, fleet.ErrNoPrivateNet), errors.Is(err, exec.ErrNotFound):
		status, msg = http.StatusServiceUnavailable, "the private network is not available on this box: tailscale is not installed"
	case errors.As(err, &busy):
		status = http.StatusTooManyRequests
		if busy.RetryAfter > 0 {
			ctx.Response().Header().Set("Retry-After", strconv.Itoa(int(busy.RetryAfter/time.Second)))
		}
	case errors.Is(err, fleet.ErrRelayUnreachable), errors.Is(err, fleet.ErrRelayFailed):
		status = http.StatusBadGateway
	}
	success := common_err.CLIENT_ERROR
	if status >= http.StatusInternalServerError {
		success = common_err.SERVICE_ERROR
	}
	return ctx.JSON(status, model.Result{Success: success, Message: msg})
}
