package v1

import (
	"errors"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

var (
	errShareMemberTwice   = errors.New("an account is listed twice for the same share")
	errShareWithoutMember = errors.New("a protected share needs at least one account: delete the share, or open it to everyone from Files")
	errShareIsForGuests   = errors.New("this share is open to everyone: protect it from Files first")
)

// resolveShareMembers reads who may mount a share from a request. A client that
// predates members sends Username alone, which keeps meaning one account with
// write access; an empty result means a guest share.
func resolveShareMembers(request model.Shares) ([]model2.ShareMember, error) {
	members := request.Members
	if members == nil {
		members = []model2.ShareMember{}

		if request.Username != "" {
			members = append(members, model2.ShareMember{Username: request.Username})
		}
	}

	if err := validateShareMembers(members); err != nil {
		return nil, err
	}

	return members, nil
}

// validateShareMembers checks every name before any of them reaches an
// smb.conf line or the account tooling.
func validateShareMembers(members []model2.ShareMember) error {
	seen := map[string]bool{}

	for _, member := range members {
		if err := service.ValidateSambaUsername(member.Username); err != nil {
			return err
		}

		if _, err := user.Lookup(member.Username); err != nil {
			return errors.New(common_err.GetMsg(common_err.USER_NOT_EXIST))
		}

		if seen[member.Username] {
			return errShareMemberTwice
		}

		seen[member.Username] = true
	}

	return nil
}

// chooseShareOwner picks the account a protected share's directory belongs to.
//
// The current owner stays for as long as its account exists, even once it is no
// longer a member: changing owner means re-owning every file in the share, so it
// only happens when it has to. A new owner is preferably a member who can write.
func chooseShareOwner(current string, members []model2.ShareMember) string {
	if len(members) == 0 {
		return ""
	}

	if current != "" {
		if _, err := user.Lookup(current); err == nil {
			return current
		}
	}

	for _, member := range members {
		if !member.ReadOnly {
			return member.Username
		}
	}

	return members[0].Username
}

// moveShareOwnership hands a shared directory from one owner to the next.
//
// Guest to account and back only touch the directory itself, as they always
// have. Account to account also re-owns everything inside, since the files keep
// the old owner and a 0660 mask that would lock the new one out; that walk can
// be long on a media share, so it runs after the request has been answered.
//
// path must already have been through service.ValidateSharePath.
func moveShareOwnership(path, from, to string) error {
	switch {
	case from == to:
		return nil
	case to == "":
		return releaseShareFromUser(path)
	case from == "":
		return restrictShareToUser(path, to)
	}

	if err := restrictShareToUser(path, to); err != nil {
		return err
	}

	go reownShareContents(path, to)

	return nil
}

func reownShareContents(root, username string) {
	account, err := user.Lookup(username)
	if err != nil {
		logger.Error("failed to find the new owner of a share", zap.String("path", root), zap.Error(err))
		return
	}

	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)

	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		// Lchown, so a symlink inside the share cannot hand its target over.
		return os.Lchown(path, uid, gid)
	})
	if err != nil {
		logger.Error("failed to hand a share's files to its new owner", zap.String("path", root), zap.Error(err))
	}
}

// applyShareMembers changes who may mount one share, moving the directory to a
// new owner first when needed and putting it back if Samba refuses the result.
func applyShareMembers(share model2.SharesDBModel, members []model2.ShareMember, timeMachine bool) (model2.SharesDBModel, error) {
	path, err := service.ValidateSharePath(share.Path)
	if err != nil {
		return share, err
	}

	owner := chooseShareOwner(share.Username, members)

	if err := moveShareOwnership(path, share.Username, owner); err != nil {
		return share, err
	}

	id := strconv.FormatUint(uint64(share.ID), 10)
	if err := service.MyService.Shares().UpdateShare(id, owner, members, timeMachine); err != nil {
		if owner != share.Username {
			if share.Username == "" {
				_ = releaseShareFromUser(path)
			} else {
				_ = restrictShareToUser(path, share.Username)
			}
		}

		return share, err
	}

	share.Username = owner
	share.Members = model2.FormatMembers(members)
	share.Anonymous = owner == ""
	share.TimeMachine = timeMachine

	return share, nil
}

// withMember returns members with username in or out as asked, keeping the
// order of the others so an unchanged share compares equal.
func withMember(members []model2.ShareMember, username string, in bool, readOnly bool) []model2.ShareMember {
	result := []model2.ShareMember{}
	found := false

	for _, member := range members {
		if member.Username != username {
			result = append(result, member)
			continue
		}

		found = true

		if in {
			result = append(result, model2.ShareMember{Username: username, ReadOnly: readOnly})
		}
	}

	if in && !found {
		result = append(result, model2.ShareMember{Username: username, ReadOnly: readOnly})
	}

	return result
}

// PutSambaUserShares sets which protected shares one account may mount, and
// whether it may write there: the member's view of the same data the share
// dialog edits. Shares left out of the list lose the account.
//
// Guest shares are refused rather than quietly protected, and so is a change
// that would leave a share with nobody at all: an empty "valid users" would let
// every account in, and turning it back into a guest share would publish a
// private folder.
func PutSambaUserShares(ctx echo.Context) error {
	username := ctx.Param("username")

	if err := service.ValidateSambaUsername(username); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	if _, err := user.Lookup(username); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.USER_NOT_EXIST, Message: common_err.GetMsg(common_err.USER_NOT_EXIST)})
	}

	request := []model.UserShare{}
	if err := ctx.Bind(&request); err != nil {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: err.Error()})
	}

	wanted := map[uint]model.UserShare{}
	for _, entry := range request {
		wanted[entry.ID] = entry
	}

	type change struct {
		share   model2.SharesDBModel
		members []model2.ShareMember
	}

	changes := []change{}

	// Everything is checked before anything is written, so a refused request
	// leaves every share as it was.
	for _, share := range service.MyService.Shares().GetSharesList() {
		entry, listed := wanted[share.ID]
		delete(wanted, share.ID)

		current := share.MemberList()
		if len(current) == 0 {
			if listed {
				return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: errShareIsForGuests.Error() + " (" + share.Path + ")"})
			}

			continue
		}

		members := withMember(current, username, listed, entry.ReadOnly)

		if len(members) == 0 {
			return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: errShareWithoutMember.Error() + " (" + share.Path + ")"})
		}

		if model2.FormatMembers(members) != model2.FormatMembers(current) {
			changes = append(changes, change{share: share, members: members})
		}
	}

	if len(wanted) > 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: "no such share"})
	}

	for _, c := range changes {
		if _, err := applyShareMembers(c.share, c.members, c.share.TimeMachine); err != nil {
			return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
		}
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: username})
}
