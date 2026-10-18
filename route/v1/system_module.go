package v1

import (
	"errors"
	"net/http"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/service"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"
)

// The routes of the modules: the list of what this release can install, the plan of an install, the install and the remove, which start a unit, and how the job of a module is going.
// They answer and refuse as the routes of the upgrade of the operating system do (osPlanAnswer: the envelope, and no-store; a refusal is a 409 with the code in data.error_code), with
// two statuses of their own: a field that is not valid is 400, and a unit that could not be started is 500. The body of a request is read as strictly as theirs (readOSUpgradeBody).

// moduleFailed says what went wrong with a request about a module: a refusal of the service is a 409 with its code (400 for a field that is not valid, 500 for a unit that could not be
// started), the refusal of a body that is not valid is the engine's bad_request, and anything else a 500 that says only that it is internal.
func moduleFailed(ctx echo.Context, err error) error {
	var refusal *service.ModuleRefusal
	if errors.As(err, &refusal) {
		status := http.StatusConflict
		switch refusal.Code {
		case service.ModuleBadRequest:
			status = http.StatusBadRequest
		case service.ModuleStartFailed:
			status = http.StatusInternalServerError
		}
		return osPlanAnswer(ctx, status, refusal.Detail, osPlanRefusalData{ErrorCode: refusal.Code, Error: refusal.Detail, Data: refusal.Data})
	}
	var bad *service.OSUpgradeRefusal
	if errors.As(err, &bad) && bad.Code == service.OSUpgradeBadRequest {
		return osUpgradeFailed(ctx, err)
	}
	logger.Error("a request about a module failed", zap.String("path", ctx.Path()), zap.Error(err))
	return osPlanAnswer(ctx, http.StatusInternalServerError, osUpgradeInternal, osPlanRefusalData{ErrorCode: "internal", Error: osUpgradeInternal})
}

// moduleName is the name in the address, which has passed the rule of the name of a module, or the answer to a request whose name has not.
func moduleName(ctx echo.Context) (string, error) {
	name := ctx.Param("name")
	if !osmodule.ValidName(name) {
		return "", &service.ModuleRefusal{Code: service.ModuleBadRequest, Detail: "a field of the request is not valid: name", Data: map[string]any{"field": "name"}}
	}
	return name, nil
}

// moduleInstallRequest is the body of an install: the plan that the user read.
type moduleInstallRequest struct {
	PlanID string `json:"plan_id"`
}

// @Summary  the modules that this release can install, and what the box says of each
// @Description The catalogue of this release joined with what is installed: catalogue (ok, and when it is not, error: none, unreadable or invalid, with the place in detail; the list is then empty, never partial), and modules, each with name, title, description, tier (core or community), tag, whether the catalogue has it, installed (null, or its version), system (supported, and the codes of the checks that say no: os, arch, recasaos) and broken when its manifest cannot be read. It only reads. A module that is installed and no longer in the catalogue is listed all the same, with catalogue false.
// @Produce  application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/modules [get]
func GetSystemModules(ctx echo.Context) error {
	list, err := service.MyService.System().ListSystemModules()
	if err != nil {
		return moduleFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), list)
}

// @Summary  the plan of installing a module: what the box says of it and what apt would do, with the machine unchanged
// @Description Runs the module assistant's plan in the core, for at most 60 seconds: the checks in their order (catalogue, os, arch, recasaos, already_installed, foreign_unit, busy, disk, apt, each with code, ok and detail), refused (null, or the code of the first check that failed), the packages apt would install and upgrade with the sizes it gives, the archive, and plan_id, the id that the install must be given back. A plan that is refused is still a plan: 200 with refused set. 400 bad_request (data.data.field is name) for a name that is not a module's; 409 with data.error_code plan_timeout when the plan could not be made in its time. It changes nothing. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param    name path string true "the name of the module"
// @Success 200 {object} model.Result
// @Failure 400 {object} model.Result "the name is not a module's: data.error_code is bad_request"
// @Failure 409 {object} model.Result "plan_timeout"
// @Router /sys/modules/{name}/plan [post]
func PlanSystemModule(ctx echo.Context) error {
	name, err := moduleName(ctx)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	plan, err := service.MyService.System().PlanSystemModule(name)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), plan)
}

// @Summary  install a module
// @Description Starts the unit that installs the module, for the plan that the user read (plan_id): the plan is made again now and must be the same, and nothing else may be changing the box. The body is strict JSON, {"plan_id": "m1-..."}, with no field besides it. 202 with the status (data.job.state is running; follow it with GET /sys/modules/{name}/status); 400 bad_request (data.data.field) for a name, a body or a field that is not valid; 409 with data.error_code when it is refused: not_root, unsupported, busy (data.data.reason), plan_changed, plan_timeout, and the codes of the plan's checks (catalogue, os, arch, recasaos, already_installed, foreign_unit, disk_short, disk_unknown, apt_missing, apt_failed, apt_removes, apt_downgrades, held_package, package_not_exact, with data.data.checks); 500 start_failed when the unit could not be started: nothing was changed. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Accept   application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param    name path string true "the name of the module"
// @Success 202 {object} model.Result "the unit was started: data is the status"
// @Failure 400 {object} model.Result "the name, the body or a field is not valid: data.error_code is bad_request"
// @Failure 409 {object} model.Result "refused: data.error_code says why"
// @Failure 500 {object} model.Result "start_failed: nothing was changed"
// @Router /sys/modules/{name}/install [post]
func InstallSystemModule(ctx echo.Context) error {
	name, err := moduleName(ctx)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	var req moduleInstallRequest
	if err := readOSUpgradeBody(ctx, &req); err != nil {
		return moduleFailed(ctx, err)
	}
	if req.PlanID == "" {
		return moduleFailed(ctx, badOSUpgradeRequest("plan_id"))
	}
	status, err := service.MyService.System().StartSystemModuleInstall(name, req.PlanID)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusAccepted, "accepted", status)
}

// @Summary  remove a module
// @Description Starts the unit that removes the module: its unit is stopped and disabled, and exactly the files it put down are deleted, with its manifest. It never purges packages and never touches the module's data. The body is {}. 202 with the status (data.job.state is running); 400 bad_request for a name or a body that is not valid; 409 with data.error_code when it is refused: not_root, unsupported, busy (data.data.reason), not_installed. 500 start_failed when the unit could not be started: nothing was changed. The token goes in the Authorization header, and is not the refresh token.
// @Produce  application/json
// @Accept   application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param    name path string true "the name of the module"
// @Success 202 {object} model.Result "the unit was started: data is the status"
// @Failure 400 {object} model.Result "the name or the body is not valid: data.error_code is bad_request"
// @Failure 409 {object} model.Result "refused: data.error_code says why"
// @Failure 500 {object} model.Result "start_failed: nothing was changed"
// @Router /sys/modules/{name}/remove [post]
func RemoveSystemModule(ctx echo.Context) error {
	name, err := moduleName(ctx)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	var nothing struct{}
	if err := readOSUpgradeBody(ctx, &nothing); err != nil {
		return moduleFailed(ctx, err)
	}
	status, err := service.MyService.System().StartSystemModuleRemove(name)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusAccepted, "accepted", status)
}

// @Summary  how the job of a module is going, and the module
// @Description data.job is the job that the core started for the module, or the last that it started: state (idle, running, succeeded, refused, failed or interrupted), step (where it is, or where it failed), code (why it was refused or failed), undo (rolled_back or rollback_incomplete for an install that failed after it changed the box), kept_packages (the packages an install that failed left on the box, null when it is not known), started_at, ended_at and log_tail (the last 20 lines of the log, as text). data.status is the module as the assistant says it: installed, version, unit (active, enabled) and route (answers). It runs no apt and takes no lock of a job: it is safe to ask every few seconds. 400 bad_request for a name that is not a module's.
// @Produce  application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param    name path string true "the name of the module"
// @Success 200 {object} model.Result
// @Failure 400 {object} model.Result "the name is not a module's: data.error_code is bad_request"
// @Router /sys/modules/{name}/status [get]
func GetSystemModuleStatus(ctx echo.Context) error {
	name, err := moduleName(ctx)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	status, err := service.MyService.System().GetSystemModuleStatus(name)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), status)
}
