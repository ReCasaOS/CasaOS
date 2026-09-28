package v1

import (
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
	"github.com/labstack/echo/v4"
)

// GetSambaUsersList returns the share accounts CasaOS created. Accounts that
// belong to the operating system are not listed and cannot be managed here.
func GetSambaUsersList(ctx echo.Context) error {
	users, err := service.ListSambaUsers()
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: users})
}

// PostSambaUserCreate adds an account that can mount protected shares. It has no
// home directory and no login shell, so it cannot be used to reach the host.
func PostSambaUserCreate(ctx echo.Context) error {
	request := model.SambaUser{}
	if err := ctx.Bind(&request); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	if err := service.CreateSambaUser(request.Username, request.Password); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	// Deliberately echoes only the name back.
	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: request.Username})
}

// PutSambaUserPassword replaces the password of an existing share account.
func PutSambaUserPassword(ctx echo.Context) error {
	username := ctx.Param("username")

	request := model.SambaUser{}
	if err := ctx.Bind(&request); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	if err := service.SetSambaPassword(username, request.Password); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: username})
}

// DeleteSambaUser removes a share account, taking it out of every share it may
// mount first. A share it owns passes to another member, files included.
//
// It refuses while the account is the only member of a share. Removing it
// anyway would leave that share with a "valid users" entry that no longer
// resolves, which smbd treats as nobody being allowed in — the share would
// simply stop working with no explanation.
func DeleteSambaUser(ctx echo.Context) error {
	username := ctx.Param("username")

	type change struct {
		share   model2.SharesDBModel
		members []model2.ShareMember
	}

	changes := []change{}

	for _, share := range service.MyService.Shares().GetSharesList() {
		current := share.MemberList()
		members := withMember(current, username, false, false)

		if len(members) == len(current) && share.Username != username {
			continue
		}

		if len(members) == 0 {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{
				Success: common_err.CLIENT_ERROR,
				Message: "this account is the only one allowed into the share " + share.Path,
			})
		}

		changes = append(changes, change{share: share, members: members})
	}

	// The account goes first: once it no longer exists, applyShareMembers sees
	// the owner is gone and hands its shares, files included, to a member.
	if err := service.DeleteSambaUser(username); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	for _, c := range changes {
		if _, err := applyShareMembers(c.share, c.members, c.share.TimeMachine); err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: username})
}
