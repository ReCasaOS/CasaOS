package v1

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
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

// moduleInstallRequest is the body of an install, or of an update when mode says so: the plan that the user read. A mode that is not there is an install.
type moduleInstallRequest struct {
	PlanID string  `json:"plan_id"`
	Mode   *string `json:"mode"`
}

// moduleMode is the mode a request asks for: install when the field is not there, and install or update when it is. Anything else is a field that is not valid.
func moduleMode(mode *string) (string, error) {
	switch {
	case mode == nil || *mode == osmodule.ModeInstall:
		return osmodule.ModeInstall, nil
	case *mode == osmodule.ModeUpdate:
		return osmodule.ModeUpdate, nil
	}
	return "", badOSUpgradeRequest("mode")
}

// planModeOf is the mode of a plan. The route took no body and ignored whatever it was sent, and still does: a body that is not a JSON object, or that has no "mode" key, is the plan of an
// install. When "mode" is there it is strict: a string that is install or update, once, and anything else is a 400 on the field mode.
func planModeOf(ctx echo.Context) (string, error) {
	raw, err := io.ReadAll(io.LimitReader(ctx.Request().Body, osUpgradeBodyMax+1))
	if err != nil || len(raw) > osUpgradeBodyMax {
		return osmodule.ModeInstall, nil
	}
	var object map[string]json.RawMessage
	if json.Unmarshal(raw, &object) != nil || object == nil {
		return osmodule.ModeInstall, nil
	}
	value, there := object["mode"]
	if !there {
		return osmodule.ModeInstall, nil
	}
	// (a key that comes twice is not read as the last one)
	decoder := json.NewDecoder(bytes.NewReader(raw))
	_, _ = decoder.Token()
	seen := 0
	for decoder.More() {
		key, _ := decoder.Token()
		if key == "mode" {
			seen++
		}
		var skip json.RawMessage
		if decoder.Decode(&skip) != nil {
			break
		}
	}
	var mode *string
	if seen != 1 || json.Unmarshal(value, &mode) != nil || mode == nil {
		return "", badOSUpgradeRequest("mode")
	}
	return moduleMode(mode)
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

// @Summary  the plan of installing a module, or of updating one that is installed: what the box says of it and what apt would do, with the machine unchanged
// @Description Runs the module assistant's plan in the core, for at most 60 seconds. The body is optional and, as it always was, ignored when it is not a JSON object or has no "mode" key: that is the plan of an install. {"mode": "update"} is the plan of the update of a module that is installed to the tag the catalogue names; when "mode" is there it is install or update, once, and anything else is a 400 bad_request (data.data.field is mode). The checks of an install in their order (catalogue, os, arch, recasaos, already_installed, foreign_unit, busy, dpkg_state, disk, apt, each with code, ok and detail); those of an update (catalogue, os, arch, recasaos, installed, newer, foreign_unit, busy, dpkg_state, disk, apt), with mode "update" and update (installed and latest tags, added: the packages apt says it would install (those of the new manifest that are not installed), dropped: those it no longer names, which are never removed), and refused with not_installed, manifest_invalid, files_unreadable, files_unverified, version_unknown, up_to_date, downgrade or unit_changed besides the codes of an install; then refused (null, or the code of the first check that failed), the packages apt would install and upgrade with the sizes it gives, the archive, and plan_id, the id that the install or the update must be given back. A plan that is refused is still a plan: 200 with refused set. 400 bad_request (data.data.field is name or mode) for a name or a mode that is not valid; 409 with data.error_code plan_timeout when the plan could not be made in its time. It changes nothing. The token goes in the Authorization header, and is not the refresh token.
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
	mode, err := planModeOf(ctx)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	var plan osmodule.PlanResult
	if mode == osmodule.ModeUpdate {
		plan, err = service.MyService.System().PlanSystemModuleUpdate(name)
	} else {
		plan, err = service.MyService.System().PlanSystemModule(name)
	}
	if err != nil {
		return moduleFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusOK, common_err.GetMsg(common_err.SUCCESS), plan)
}

// @Summary  install a module, or update one that is installed
// @Description Starts the unit that installs the module, for the plan that the user read (plan_id): the plan is made again now and must be the same, and nothing else may be changing the box. The body is strict JSON, {"plan_id": "m1-..."} for an install and {"plan_id": "m1-...", "mode": "update"} for the update of a module that is installed (the plan_id is the one of the plan of that mode, and a mode that is not there is an install), with no field besides them; mode is install or update, any other value is a 400 bad_request (data.data.field is mode). 202 with the status (data.job.kind is install, or update for an update, and data.job.state is running; follow it with GET /sys/modules/{name}/status); 400 bad_request (data.data.field) for a name, a body or a field that is not valid; 409 with data.error_code when it is refused: not_root, unsupported, busy (data.data.reason), plan_changed, plan_timeout, and the codes of the plan's checks (catalogue, os, arch, recasaos, already_installed, foreign_unit, dpkg_state, disk_short, disk_unknown, apt_missing, apt_failed, apt_removes, apt_downgrades, held_package, package_not_exact, with data.data.checks; for an update: catalogue, os, arch, recasaos, not_installed, manifest_invalid, files_unreadable, files_unverified, version_unknown, up_to_date, downgrade, foreign_unit, unit_changed, busy, dpkg_state, disk_short, disk_unknown, apt_missing, apt_failed, apt_removes, apt_downgrades, held_package, package_not_exact); 500 start_failed when the unit could not be started (the log could not be written, systemd did not start it, or /usr/bin/casaos is not a program that can be run): nothing was changed. Every busy carries data.data.reason. The token goes in the Authorization header, and is not the refresh token.
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
	mode, err := moduleMode(req.Mode)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	var status service.SystemModuleStatus
	if mode == osmodule.ModeUpdate {
		status, err = service.MyService.System().StartSystemModuleUpdate(ctx.Request().Context(), name, req.PlanID)
	} else {
		status, err = service.MyService.System().StartSystemModuleInstall(ctx.Request().Context(), name, req.PlanID)
	}
	if err != nil {
		return moduleFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusAccepted, "accepted", status)
}

// @Summary  remove a module
// @Description Starts the unit that removes the module: its unit is stopped and disabled, and exactly the files it put down are deleted, with its manifest. It never purges packages and never touches the module's data. The body is {}. 202 with the status (data.job.kind is remove and data.job.state is running); 400 bad_request for a name or a body that is not valid; 409 with data.error_code when it is refused: not_root, unsupported, busy (data.data.reason), not_installed. 500 start_failed when the unit could not be started (the log could not be written, systemd did not start it, or /usr/bin/casaos is not a program that can be run): nothing was changed. The token goes in the Authorization header, and is not the refresh token.
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
	status, err := service.MyService.System().StartSystemModuleRemove(ctx.Request().Context(), name)
	if err != nil {
		return moduleFailed(ctx, err)
	}
	return osPlanAnswer(ctx, http.StatusAccepted, "accepted", status)
}

// @Summary  how the job of a module is going, and the module
// @Description data.job is the job that the core started for the module, or the last that it started: kind (install, remove or update, as the core started it; "" when the log does not say, as for a job that was started by hand), state (idle, running, succeeded, refused, failed or interrupted), step (where it is, or where it failed), code (why it was refused or failed), undo (rolled_back or rollback_incomplete for an install or an update that failed after it changed the box), kept_packages (the packages an install that failed left on the box, null when it is not known), started_at, ended_at and log_tail (the last 20 lines of the log that are not marker lines, as text). data.status is the module as the assistant says it: installed, version, unit (active, enabled) and route (answers). It runs no apt and takes no lock of a job: it is safe to ask every few seconds. 400 bad_request for a name that is not a module's.
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
