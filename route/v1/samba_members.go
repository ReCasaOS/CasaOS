package v1

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/labstack/echo/v4"
	"go.uber.org/zap"

	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

// sharesMutex serialises every change to the shares. The duplicate checks read
// the table and the insert comes after; two requests for the same folder in
// flight together both passed the check, and the folder was shared twice.
var sharesMutex sync.Mutex

var (
	errShareMemberTwice   = errors.New("an account is listed twice for the same share")
	errShareWithoutMember = errors.New("a protected share needs at least one account: delete the share, or open it to everyone from Files")
	errShareIsForGuests   = errors.New("this share is open to everyone: protect it from Files first")

	errShareMemberNotShareAccount = errors.New("only a share account made in the dashboard can be a member of a share")
)

// What the share changes do to the system, as variables so the tests, which
// can neither create accounts nor chown, can watch the order they run in.
var (
	isShareAccount     = service.IsSambaShareAccount
	deleteShareAccount = service.DeleteSambaUser
	validateSharePath  = service.ValidateSharePath
	restrictShare      = service.RestrictShareToUser
	releaseShare       = service.ReleaseShareFromUser
)

// resolveShareMembers reads who may mount a share from a request. A client that
// predates members sends Username alone, which keeps meaning one account with
// write access; an empty result means a guest share. kept are the members the
// share has already, which stay allowed whatever account they are.
func resolveShareMembers(request model.Shares, kept ...model2.ShareMember) ([]model2.ShareMember, error) {
	members := request.Members
	if members == nil {
		members = []model2.ShareMember{}

		if request.Username != "" {
			members = append(members, model2.ShareMember{Username: request.Username})
		}
	}

	if err := validateShareMembers(members, kept...); err != nil {
		return nil, err
	}

	return members, nil
}

// validateShareMembers checks every name before any of them reaches an
// smb.conf line or the account tooling. A new member must be a share account;
// one of kept, the members a share has already (an older share made on
// another account), stays, so a change of anything else does not drop it.
func validateShareMembers(members []model2.ShareMember, kept ...model2.ShareMember) error {
	seen := map[string]bool{}
	already := map[string]bool{}

	for _, member := range kept {
		already[member.Username] = true
	}

	for _, member := range members {
		if err := service.ValidateSambaUsername(member.Username); err != nil {
			return err
		}

		if _, err := user.Lookup(member.Username); err != nil {
			return errors.New(common_err.GetMsg(common_err.USER_NOT_EXIST))
		}

		// a member may become the owner: never root, www-data or a person's login
		if !already[member.Username] && !isShareAccount(member.Username) {
			return errShareMemberNotShareAccount
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
// longer a member: changing owner means re-owning the owner's files in the
// share, so it only happens when it has to. leaving is an account about to be
// deleted, which no longer counts as existing. A new owner is preferably a
// member who can write.
func chooseShareOwner(current string, members []model2.ShareMember, leaving string) string {
	if len(members) == 0 {
		return ""
	}

	if current != "" && current != leaving {
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

// moveShareOwnership hands a shared directory from one owner to the next, and
// returns what is left to do once the share itself has been changed.
//
// Guest to account and back only touch the directory itself, as they always
// have. Account to account also re-owns the old owner's files inside, since
// they keep the old owner and a 0660 mask that would lock the new one out. Only
// those: a share on /DATA, AppData or a whole disk holds files of containers
// (postgres's 999, a PUID of 1000) that must stay as they are. The walk can be
// long on a media share, so it is returned to run once UpdateShare has worked:
// a share that Samba refused keeps the old owner's files as they were.
//
// path must already have been through service.ValidateSharePath; from must
// still exist (an account about to be deleted is deleted after this).
func moveShareOwnership(path, from, to string) (reown func(), err error) {
	nothing := func() {}

	switch {
	case from == to:
		return nothing, nil
	case to == "":
		return nothing, releaseShare(path)
	case from == "":
		return nothing, restrictShare(path, to)
	}

	old, err := user.Lookup(from)
	if err != nil {
		return nil, err
	}

	oldUID, err := strconv.Atoi(old.Uid)
	if err != nil {
		return nil, err
	}

	if err := restrictShare(path, to); err != nil {
		return nil, err
	}

	return func() { go reownShareContents(path, oldUID, to) }, nil
}

// shareWalks runs one re-owning walk at a time: two owner changes close
// together would otherwise walk the same tree at once.
var shareWalks sync.Mutex

// reownShareContents hands the files of oldUID under root to username. Files
// of anybody else are left alone.
func reownShareContents(root string, oldUID int, username string) {
	shareWalks.Lock()
	defer shareWalks.Unlock()

	account, err := user.Lookup(username)
	if err != nil {
		logger.Error("failed to find the new owner of a share", zap.String("path", root), zap.Error(err))
		return
	}

	uid, _ := strconv.Atoi(account.Uid)
	gid, _ := strconv.Atoi(account.Gid)

	err = walkOwnedBy(root, oldUID, func(path string) error {
		// Lchown, so a symlink inside the share cannot hand its target over.
		return os.Lchown(path, uid, gid)
	})
	if err != nil {
		logger.Error("failed to hand a share's files to its new owner", zap.String("path", root), zap.Error(err))
	}
}

// walkOwnedBy calls fn for root and everything under it that uid owns, without
// following symlinks. An entry that cannot be read is skipped.
func walkOwnedBy(root string, uid int, fn func(path string) error) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}

		info, err := entry.Info()
		if err != nil {
			return nil
		}

		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok || int(stat.Uid) != uid {
			return nil
		}

		return fn(path)
	})
}

// applyShareMembers changes who may mount one share, moving the directory to a
// new owner first when needed and putting it back if Samba refuses the result.
// leaving is an account about to be deleted, or "".
func applyShareMembers(share model2.SharesDBModel, members []model2.ShareMember, timeMachine bool, leaving string) (model2.SharesDBModel, error) {
	return applyShareMembersAs(share, members, timeMachine, chooseShareOwner(share.Username, members, leaving))
}

// applyShareMembersAs is applyShareMembers with the owner chosen already.
func applyShareMembersAs(share model2.SharesDBModel, members []model2.ShareMember, timeMachine bool, owner string) (model2.SharesDBModel, error) {
	path, err := validateSharePath(share.Path)
	if err != nil {
		return share, err
	}

	reown, err := moveShareOwnership(path, share.Username, owner)
	if err != nil {
		return share, err
	}

	id := strconv.FormatUint(uint64(share.ID), 10)
	if err := service.MyService.Shares().UpdateShare(id, owner, members, timeMachine); err != nil {
		if owner != share.Username {
			if share.Username == "" {
				_ = releaseShare(path)
			} else {
				_ = restrictShare(path, share.Username)
			}
		}

		return share, err
	}

	// Samba took it: now the old owner's files may follow
	reown()

	share.Username = owner
	share.Members = model2.FormatMembers(members)
	share.Anonymous = owner == ""
	share.TimeMachine = timeMachine

	return share, nil
}

// shareChange is one share and the members it is to have.
type shareChange struct {
	share   model2.SharesDBModel
	members []model2.ShareMember
}

// applyShareChanges applies changes in order. When one fails, the ones already
// applied are put back as they were, newest first, so a refused request leaves
// the shares as they were; what could not be put back is said in the error.
func applyShareChanges(changes []shareChange, leaving string) error {
	type done struct{ before, after model2.SharesDBModel }

	applied := []done{}

	for _, c := range changes {
		after, err := applyShareMembers(c.share, c.members, c.share.TimeMachine, leaving)
		if err == nil {
			applied = append(applied, done{before: c.share, after: after})
			continue
		}

		notBack := []string{}

		for i := len(applied) - 1; i >= 0; i-- {
			d := applied[i]

			// the members and the owner it had, the owner's files included
			if _, undoErr := applyShareMembersAs(d.after, d.before.MemberList(), d.before.TimeMachine, d.before.Username); undoErr != nil {
				notBack = append(notBack, d.before.Path)
			}
		}

		if len(notBack) > 0 {
			return fmt.Errorf("%s: %w; these shares could not be put back as they were: %s", c.share.Path, err, strings.Join(notBack, ", "))
		}

		return fmt.Errorf("%s: %w", c.share.Path, err)
	}

	return nil
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
	sharesMutex.Lock()
	defer sharesMutex.Unlock()

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

	changes := []shareChange{}

	// Everything is checked before anything is written; a change that fails
	// once writing has started puts back the ones made before it.
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
			changes = append(changes, shareChange{share: share, members: members})
		}
	}

	if len(wanted) > 0 {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: "no such share"})
	}

	if err := applyShareChanges(changes, ""); err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: common_err.GetMsg(common_err.SUCCESS), Data: username})
}
