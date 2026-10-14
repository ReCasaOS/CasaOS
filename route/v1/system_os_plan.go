package v1

import (
	"errors"
	"net/http"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
)

// osPlanRefusalData is the data of what is refused with a 409: the code in error_code, the English reason in error (and in the
// message: the page says the code in its own words, and a client that does not read the data still reads the message), and what
// makes the code precise in data.
type osPlanRefusalData struct {
	ErrorCode string         `json:"error_code"`
	Error     string         `json:"error"`
	Data      map[string]any `json:"data,omitempty"`
}

// osPlanAnswer is the answer to every request of the plan: about the box as it is now, and it may hold what the files of the box
// say: nothing is kept of it.
func osPlanAnswer(ctx echo.Context, status int, message string, data any) error {
	ctx.Response().Header().Set(echo.HeaderCacheControl, "no-store")
	return ctx.JSON(status, model.Result{Success: status, Message: message, Data: data})
}

// osPlanFailed says what went wrong with a request: a refusal of the service is a 409 with its code, anything else a 500.
func osPlanFailed(ctx echo.Context, err error) error {
	var refusal *service.OSPlanRefusal
	if errors.As(err, &refusal) {
		return osPlanAnswer(ctx, http.StatusConflict, refusal.Detail, osPlanRefusalData{ErrorCode: refusal.Code, Error: refusal.Detail, Data: refusal.Data})
	}
	return osPlanAnswer(ctx, http.StatusInternalServerError, err.Error(), nil)
}

// @Summary  start the plan of the upgrade of the operating system: what apt would do, with the machine unchanged
// @Description Starts a job that rewrites the sources to the next release in a temporary directory, lets apt plan the upgrade there (a real update of the target's package lists, a simulated transaction) and judges what it says. It changes nothing of the machine. 202 when a job began (data.state is running), 200 when one already runs (data.already_running), 409 with data.error_code (not_root, hop_not_planned, identity_unsupported, busy, no_room) when none can be started. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 202 {object} model.Result "a job began"
// @Success 200 {object} model.Result "a job already runs: data.already_running"
// @Failure 409 {object} model.Result "refused: data.error_code says why"
// @Router /sys/os/upgrade/plan [post]
func StartSystemOSPlan(ctx echo.Context) error {
	status, err := service.MyService.System().StartSystemOSPlan()
	if err != nil {
		return osPlanFailed(ctx, err)
	}
	if status.AlreadyRunning {
		return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), status)
	}
	return osPlanAnswer(ctx, http.StatusAccepted, "accepted", status)
}

// @Summary  how the plan of the upgrade of the operating system is going, or how it went
// @Description Whether a plan can be made (plannable, and reason when it cannot; unknown when that could not be told, and then it is asked again), the state of the job (idle, running, done, failed), its phase, its times, why it failed (error: code, phase, detail, data), the sandbox directory that a job that is over could not remove (sandbox_left_behind) and, when it is done and has not expired, the plan without the tree of the sources. It runs no apt, and takes no lock of the package updater; asking who the machine is takes a moment when no job runs, and the requests that come together share the answer.
// @Produce  application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/os/upgrade/plan [get]
func GetSystemOSPlan(ctx echo.Context) error {
	return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), service.MyService.System().GetSystemOSPlan())
}

// @Summary  the text with which to do the hop to the next release of the operating system by hand
// @Description Written from the plan that is there, for this machine: a shell file of blocks, each to be read and pasted into a console, with its blocks listed. 409 with data.error_code no_plan (data.data.reason: idle, running, failed, expired, blocked) when there is no plan to write it from, no_runbook when the text could not be written safely. With run=current it is the text of the upgrade that is open instead (the one that was written when it began, which does not need the plan to be alive), with from_run true, for the owner who takes the run over: 409 no_run when there is none, run_unreadable when its text cannot be read; any other value of run is a 400. It has what the files of the box say, so the token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param    run query string false "current: the text of the upgrade that is open, and not of the plan"
// @Success 200 {object} model.Result
// @Failure 400 {object} model.Result "run is asked for and is not current"
// @Failure 409 {object} model.Result "no plan, or no runbook, or no run: data.error_code says which"
// @Router /sys/os/upgrade/runbook [get]
func GetSystemOSRunbook(ctx echo.Context) error {
	if values, asked := ctx.QueryParams()["run"]; asked {
		if len(values) != 1 || values[0] != "current" {
			return osUpgradeFailed(ctx, badOSUpgradeRequest("run"))
		}
		runbook, err := service.MyService.System().GetSystemOSUpgradeRunbook()
		if err != nil {
			return osUpgradeFailed(ctx, err)
		}
		return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), runbook)
	}
	runbook, err := service.MyService.System().GetSystemOSRunbook()
	if err != nil {
		return osPlanFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), runbook)
}
