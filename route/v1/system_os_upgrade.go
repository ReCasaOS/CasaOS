package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// The routes of the upgrade of the operating system itself: its status, the owner's start and buttons, the switch of the feature and the owner's own text of the run that is open.
// They answer as the plan's do (osPlanAnswer: the envelope, and no-store, since an answer is about the machine as it is now and may hold what its files say), and they refuse as
// the plan's do (a 409 with the code in data.error_code), with two statuses of their own: the owner's mistake of a field is 400, and what the machine could not do is 500.

// osUpgradeBodyMax is the most of a body that the routes read: the lists of a start hold two hundred names each.
const osUpgradeBodyMax = 64 << 10

// osUpgradeInternal is what a failure that is no refusal says: the error may hold a path of the machine or a line of what its files say, and it is in the log.
const osUpgradeInternal = "the request could not be carried out"

// osUpgradeFailed says what went wrong with a request: a refusal of the engine is a 409 with its code (400 for a field that is not valid, 500 for a start, a restart or a close that the
// machine could not carry out), anything else a 500 that says only that it is internal.
func osUpgradeFailed(ctx echo.Context, err error) error {
	var refusal *service.OSUpgradeRefusal
	if errors.As(err, &refusal) {
		status := http.StatusConflict
		switch refusal.Code {
		case service.OSUpgradeBadRequest:
			status = http.StatusBadRequest
		case service.OSUpgradeStartFailed, service.OSUpgradeRebootFailed, service.OSUpgradeCloseFailed:
			status = http.StatusInternalServerError
		}
		return osPlanAnswer(ctx, status, refusal.Detail, osPlanRefusalData{ErrorCode: refusal.Code, Error: refusal.Detail, Data: refusal.Data})
	}
	logger.Error("a request of the upgrade of the operating system failed", zap.String("path", ctx.Path()), zap.Error(err))
	return osPlanAnswer(ctx, http.StatusInternalServerError, osUpgradeInternal, osPlanRefusalData{ErrorCode: "internal", Error: osUpgradeInternal})
}

// badOSUpgradeRequest is a field of a request that is not valid, as the engine says it of the fields it checks.
func badOSUpgradeRequest(field string) *service.OSUpgradeRefusal {
	return &service.OSUpgradeRefusal{Code: service.OSUpgradeBadRequest, Detail: "a field of the request is not valid: " + field, Data: map[string]any{"field": field}}
}

// readOSUpgradeBody reads the body of a request into a value, strictly: one JSON object and nothing after it, no field that the value does not have, no name spelled otherwise than
// the value spells it, no name twice, and no more than osUpgradeBodyMax bytes. What is wrong is a bad_request that names the field when it was a field of the body, and says "body"
// when it was the body; it never repeats what the owner sent.
func readOSUpgradeBody(ctx echo.Context, into any) error {
	raw, err := io.ReadAll(io.LimitReader(ctx.Request().Body, osUpgradeBodyMax+1))
	if err != nil || len(raw) > osUpgradeBodyMax {
		return badOSUpgradeRequest("body")
	}
	if trimmed := bytes.TrimLeft(raw, " \t\r\n"); len(trimmed) == 0 || trimmed[0] != '{' {
		return badOSUpgradeRequest("body")
	}
	if err := checkOSUpgradeBodyKeys(raw, into); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		var kind *json.UnmarshalTypeError
		if errors.As(err, &kind) && kind.Field != "" {
			return badOSUpgradeRequest(kind.Field)
		}
		return badOSUpgradeRequest("body")
	}
	if _, err := decoder.Token(); err != io.EOF {
		return badOSUpgradeRequest("body")
	}
	return nil
}

// checkOSUpgradeBodyKeys holds the keys of the object to what a client that reads it by name sees: each is one of the names of the fields of the value, spelled exactly (Go's decoder
// would take ENABLED for enabled), and none comes twice (it would take the last). It is the body as a whole that is wrong then, and the answer does not repeat the key.
func checkOSUpgradeBodyKeys(raw []byte, into any) error {
	allowed := map[string]bool{}
	if t := reflect.TypeOf(into); t != nil && t.Kind() == reflect.Pointer && t.Elem().Kind() == reflect.Struct {
		for i := 0; i < t.Elem().NumField(); i++ {
			name, _, _ := strings.Cut(t.Elem().Field(i).Tag.Get("json"), ",")
			if name != "" && name != "-" {
				allowed[name] = true
			}
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		return badOSUpgradeRequest("body")
	}
	seen := map[string]bool{}
	for decoder.More() {
		token, err := decoder.Token()
		key, isString := token.(string)
		if err != nil || !isString || !allowed[key] || seen[key] {
			return badOSUpgradeRequest("body")
		}
		seen[key] = true
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			return badOSUpgradeRequest("body")
		}
	}
	return nil
}

// osUpgradeButton is the handler of a button that has nothing to say but its click: the body is the empty object, and the answer is 202 with the status that follows.
func osUpgradeButton(ctx echo.Context, press func() (service.OSUpgradeStatus, error)) error {
	var nothing struct{}
	if err := readOSUpgradeBody(ctx, &nothing); err != nil {
		return osUpgradeFailed(ctx, err)
	}
	status, err := press()
	if err != nil {
		return osUpgradeFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusAccepted, "accepted", status)
}

// @Summary  how the upgrade of the operating system is going
// @Description The state of the upgrade that is open or the last that was, as a code the page puts in words (state, reason, cause, class of what stopped it), the block that runs, whether the point of no return is behind it, whether the machine is held (busy), what the owner can do now (actions, which the page shows as they are), the checks that stand before the restart, the findings of the check afterwards, where the owner stands if they take the run over (manual) and the end of the unit's output with the addresses of the sources reduced to their host. It runs no apt and takes no lock of a run: it is safe to ask every few seconds. beta_enabled says whether the switch of the feature is on; the upgrade that is open is told whatever the switch says.
// @Produce  application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/os/upgrade/status [get]
func GetSystemOSUpgradeStatus(ctx echo.Context) error {
	return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), service.MyService.System().GetSystemOSUpgradeStatus())
}

// @Summary  start the upgrade of the operating system, or its rehearsal
// @Description Starts the unit that runs the blocks of the hop to the next release for the plan that the owner read: its plan_id, what they said stands between them and a machine that does not come back (safety_net: snapshot, console_and_backup or reinstall_ok), the name of the machine typed again (hostname), the removals and the sources put aside that they accepted (accept_removals, paused_sources, as the plan lists them) and the acknowledgements that the plan asks for (acks: no_interruption and no_way_back always, sources_paused when the plan puts sources aside, removals_read when it has removals to accept). With rehearsal true it runs only the half that can be undone, holds the sources of the next release for half an hour and puts the machine back; it needs neither safety_net nor no_way_back. The body is strict JSON with no field besides these. 202 with the status when the unit was started; 400 bad_request (data.data.field) for a body or a field that is not valid; 409 with data.error_code when it is refused (beta_off, not_root, identity_unsupported, hop_not_planned, run_open, manual_run_found, no_plan, plan_changed, plan_blocked, plan_older_than_refresh, bad_safety_net, hostname_mismatch, acks_missing, removals_mismatch, paused_mismatch, sources_changed, readiness_refuses, busy, apps_busy, no_room, runbook_unsafe, runbook_too_large); 500 start_failed when nothing could be started, and nothing was changed. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Accept   application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 202 {object} model.Result "the unit was started: data is the status"
// @Failure 400 {object} model.Result "the body or a field is not valid: data.error_code is bad_request"
// @Failure 409 {object} model.Result "refused: data.error_code says why"
// @Failure 500 {object} model.Result "start_failed: nothing was changed"
// @Router /sys/os/upgrade/start [post]
func StartSystemOSUpgrade(ctx echo.Context) error {
	var req service.OSUpgradeStartRequest
	if err := readOSUpgradeBody(ctx, &req); err != nil {
		return osUpgradeFailed(ctx, err)
	}
	status, err := service.MyService.System().StartSystemOSUpgrade(req)
	if err != nil {
		return osUpgradeFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusAccepted, "accepted", status)
}

// @Summary  bring the old release up to its last state
// @Description Starts the unit that brings the release that the machine runs to its last state (apt-get update and apt-get dist-upgrade with the phased updates, removing nothing, with the sources that are not Debian's put aside and put back), which the plan is made after: a plan that was made before it is not good for a start. It has no point of no return: when it stops, the unit puts the sources and the timers back. It ends as refreshed, or as reboot_first when the machine wants a restart (the restart button is then valid). accept_releaseinfo_change, false when it is not said, is the owner's word that a repository whose release information changed (the suite that went from stable to oldstable when Debian 12 came out) is to be taken: a refresh that apt stopped for it is failed with failure.hint releaseinfo_change, and is asked again with it said. The body is strict JSON with no field besides that. 202 with the status when the unit was started; 400 bad_request (data.data.field) for a body or a field that is not valid; 409 with data.error_code when it is refused (beta_off, not_root, identity_unsupported, hop_not_planned, run_open, manual_run_found, readiness_refuses, busy, apps_busy, no_room, source_unrecognized with data.data.file and line, runbook_unsafe, runbook_too_large); 500 start_failed when nothing could be started, and nothing was changed. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Accept   application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 202 {object} model.Result "the unit was started: data is the status"
// @Failure 400 {object} model.Result "the body or a field is not valid: data.error_code is bad_request"
// @Failure 409 {object} model.Result "refused: data.error_code says why"
// @Failure 500 {object} model.Result "start_failed: nothing was changed"
// @Router /sys/os/upgrade/refresh [post]
func StartSystemOSUpgradeRefresh(ctx echo.Context) error {
	var req service.OSUpgradeRefreshRequest
	if err := readOSUpgradeBody(ctx, &req); err != nil {
		return osUpgradeFailed(ctx, err)
	}
	status, err := service.MyService.System().StartSystemOSUpgradeRefresh(req)
	if err != nil {
		return osUpgradeFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusAccepted, "accepted", status)
}

// @Summary  restart the machine that waits for its restart
// @Description The owner's click on "restart now". Valid when the machine waits for its restart (awaiting_reboot, reboot_stalled, or a refresh that asks for one), with no unit of the upgrade running, no other package manager at work and, for a hop, what stands before the restart green (asked again now). Works whatever the switch says. The body is {}. 202 with the status (rebooting); 409 with data.error_code: no_run, not_awaiting_reboot (data.data.state), barrier_failed (data.data.checks), unit_active, dpkg_locked; 500 reboot_failed when systemd would not restart the machine. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Accept   application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 202 {object} model.Result "the restart was asked for: data is the status"
// @Failure 409 {object} model.Result "refused: data.error_code says why"
// @Failure 500 {object} model.Result "reboot_failed"
// @Router /sys/os/upgrade/reboot [post]
func RebootSystemOSUpgrade(ctx echo.Context) error {
	return osUpgradeButton(ctx, service.MyService.System().RebootSystemOSUpgrade)
}

// @Summary  go on with an upgrade that stopped half way
// @Description The owner's click on "go on": starts a unit of its own that mends what the stop left and runs the blocks that remain. Valid for a run that stopped or was interrupted after its point of no return, for a class of stop that can be gone on from. Works whatever the switch says. The body is {}. 202 with the status; 409 with data.error_code: no_run, not_resumable (data.data.state, data.data.class), unit_active, busy. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Accept   application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 202 {object} model.Result "a unit was started: data is the status"
// @Failure 409 {object} model.Result "refused: data.error_code says why"
// @Router /sys/os/upgrade/resume [post]
func ResumeSystemOSUpgrade(ctx echo.Context) error {
	return osUpgradeButton(ctx, service.MyService.System().ResumeSystemOSUpgrade)
}

// @Summary  call off an upgrade before its point of no return
// @Description The owner's click on "stop": the unit is stopped and the machine is put back as it was. Valid while the unit runs the blocks that can be undone (refreshing, rehearsing, running before block 5); after that nothing can be called off. Works whatever the switch says. The body is {}. 202 with the status; 409 with data.error_code: no_run, not_abortable (data.data.reason: point_of_no_return, past_block or not_running). The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Accept   application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 202 {object} model.Result "the unit is stopped and is put back: data is the status"
// @Failure 409 {object} model.Result "refused: data.error_code says why"
// @Router /sys/os/upgrade/abort [post]
func AbortSystemOSUpgrade(ctx echo.Context) error {
	return osUpgradeButton(ctx, service.MyService.System().AbortSystemOSUpgrade)
}

// @Summary  the owner's word on an upgrade that is not running
// @Description action is acknowledge (close a run that ended or stopped, which lets backups and automatic updates go on), discard (the same, and drop the plan it was made for; for a rehearsal, put the machine back) or manual (take the run over: the core stops acting, and the owner finishes the hop by hand from its text). A machine that is half upgraded is let go of only with the word that the status gives in ack_word, typed in word. Works whatever the switch says. 200 with the status; 400 bad_request for a field that is not valid; 409 with data.error_code: no_run, run_unreadable, not_terminal (data.data.state), word_required, bad_word, unit_active. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Accept   application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result "data is the status"
// @Failure 400 {object} model.Result "the body or a field is not valid: data.error_code is bad_request"
// @Failure 409 {object} model.Result "refused: data.error_code says why"
// @Router /sys/os/upgrade/ack [post]
func AckSystemOSUpgrade(ctx echo.Context) error {
	var req service.OSUpgradeAckRequest
	if err := readOSUpgradeBody(ctx, &req); err != nil {
		return osUpgradeFailed(ctx, err)
	}
	status, err := service.MyService.System().AckSystemOSUpgrade(req)
	if err != nil {
		return osUpgradeFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), status)
}

// osUpgradeBetaRequest is the body of the switch: enabled has to be said, since a body that says nothing must not be read as "off".
type osUpgradeBetaRequest struct {
	Enabled *bool `json:"enabled"`
}

// @Summary  switch the upgrade of the operating system on or off
// @Description The owner's switch of the feature, which gates what begins something (a start); what is open goes on whatever it says, so that a machine that is half upgraded is never trapped behind it. Turning it off while a run is open is refused. The same as creating or removing the file /etc/casaos/os-upgrade.beta. 200 with data.beta_enabled; 400 bad_request when enabled is not true or false; 409 run_open (data.data.state, data.data.run_id) when turning it off with a run open. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Accept   application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result "data.beta_enabled is what the switch is now"
// @Failure 400 {object} model.Result "the body has no enabled, or it is not true or false: data.error_code is bad_request"
// @Failure 409 {object} model.Result "run_open"
// @Router /sys/os/upgrade/beta [post]
func SetSystemOSUpgradeBeta(ctx echo.Context) error {
	var req osUpgradeBetaRequest
	if err := readOSUpgradeBody(ctx, &req); err != nil {
		return osUpgradeFailed(ctx, err)
	}
	if req.Enabled == nil {
		return osUpgradeFailed(ctx, badOSUpgradeRequest("enabled"))
	}
	if err := service.MyService.System().SetSystemOSUpgradeBeta(*req.Enabled); err != nil {
		return osUpgradeFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), map[string]bool{"beta_enabled": *req.Enabled})
}
