package v1

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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
	errShareWithoutOwner          = errors.New("none of the share's members is a share account made in the dashboard that could own it: add one first")
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
// deleted, which no longer counts as existing. A new owner is a share account,
// since every member's session writes as the owner (force user), preferably one
// who can write; members kept from an older share on another account never
// become owners.
func chooseShareOwner(current string, members []model2.ShareMember, leaving string) (string, error) {
	if len(members) == 0 {
		return "", nil
	}

	if current != "" && current != leaving {
		if _, err := user.Lookup(current); err == nil {
			return current, nil
		}
	}

	candidates := []model2.ShareMember{}

	for _, member := range members {
		if isShareAccount(member.Username) {
			candidates = append(candidates, member)
		}
	}

	if len(candidates) == 0 {
		return "", errShareWithoutOwner
	}

	for _, member := range candidates {
		if !member.ReadOnly {
			return member.Username, nil
		}
	}

	return candidates[0].Username, nil
}

// reownPlan is the walk an owner change leaves to do: the files of oldUID under
// path go to uid:gid. The ids are resolved when the change is planned, so a
// walk that waits behind another still has them once the new owner, or the
// next one, has been deleted.
type reownPlan struct {
	path     string
	oldUID   int
	uid, gid int
}

// skippedReown is set when an owner change leaves the old owner's files as
// they are, on a folder where containers keep theirs (see reownRefused).
type skippedReown struct{ path string }

// moveShareOwnership hands a shared directory from one owner to the next, and
// returns the walk left to do once Samba has taken the change, or nil.
//
// Guest to account and back only touch the directory itself, as they always
// have. Account to account also re-owns the old owner's files inside, since
// they keep the old owner and a 0660 mask that would lock the new one out. Only
// those, and never on /DATA, AppData or a whole disk (reownRefused): there a
// share account's uid can be a container's too (postgres's 999). The old
// owner is whoever owns the share's folder, so a share whose owning account is
// gone can still be changed.
//
// path must already have been through service.ValidateSharePath.
func moveShareOwnership(path, from, to string) (*reownPlan, *skippedReown, error) {
	switch {
	case from == to:
		return nil, nil, nil
	case to == "":
		return nil, nil, releaseShare(path)
	case from == "":
		return nil, nil, restrictShare(path, to)
	}

	oldUID, err := shareFolderOwner(path)
	if err != nil {
		return nil, nil, err
	}

	account, err := user.Lookup(to)
	if err != nil {
		return nil, nil, err
	}

	uid, err := strconv.Atoi(account.Uid)
	if err != nil {
		return nil, nil, err
	}

	gid, err := strconv.Atoi(account.Gid)
	if err != nil {
		return nil, nil, err
	}

	if err := restrictShare(path, to); err != nil {
		return nil, nil, err
	}

	// root's files are never handed over, and the same uid has nothing to move
	if oldUID == 0 || oldUID == uid {
		return nil, nil, nil
	}

	if reownRefused(path) {
		return nil, &skippedReown{path: path}, nil
	}

	return &reownPlan{path: path, oldUID: oldUID, uid: uid, gid: gid}, nil, nil
}

// shareFolderOwner is the uid that owns a share's folder: its owner, even when
// the account has been deleted since.
var shareFolderOwner = func(path string) (int, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, errors.New("cannot read the owner of " + path)
	}

	return int(stat.Uid), nil
}

// reownRefused says whether a share's folder holds data of containers whose
// uids a share account may share: /DATA, AppData and anything in it, a share
// root itself and the root of a mounted disk.
func reownRefused(path string) bool {
	if path == "/DATA/AppData" || strings.HasPrefix(path, "/DATA/AppData/") {
		return true
	}

	for _, root := range []string{"/DATA", "/var/lib/casaos/files", "/mnt", "/media", "/srv"} {
		if path == root {
			return true
		}
	}

	return isMountPoint(path)
}

// isMountPoint says whether path is the root of a filesystem of its own. When
// it cannot tell, it says yes, which leaves the files alone.
var isMountPoint = func(path string) bool {
	var self, parent syscall.Stat_t

	if syscall.Lstat(path, &self) != nil || syscall.Lstat(filepath.Dir(path), &parent) != nil {
		return true
	}

	return self.Dev != parent.Dev || self.Ino == parent.Ino
}

// shareWalks runs one re-owning walk at a time: two owner changes close
// together would otherwise walk the same tree at once.
var shareWalks sync.Mutex

// reownShareContents runs a plan in the background. A variable so the tests
// never chown anything.
var reownShareContents = func(plan reownPlan) {
	go runReown(plan)
}

func runReown(plan reownPlan) {
	shareWalks.Lock()
	defer shareWalks.Unlock()

	// chown walks with fts relative to each directory and never follows a
	// symlink (-R -h, -P by default), so a folder swapped for a link mid-walk
	// cannot send it elsewhere. It goes on past a file it cannot change (EPERM,
	// a read-only mount) and reports them at the end.
	output, err := exec.Command("chown", reownArgs(plan)...).CombinedOutput()
	if err != nil {
		logger.Error("some of a share's files could not be handed to its new owner",
			zap.String("path", plan.path), zap.String("output", strings.TrimSpace(string(output))), zap.Error(err))
	}
}

// reownArgs are chown's arguments for a plan. The + makes chown read the ids as
// numbers, never as names.
func reownArgs(plan reownPlan) []string {
	return []string{
		"-R", "-h",
		fmt.Sprintf("--from=+%d", plan.oldUID),
		fmt.Sprintf("+%d:+%d", plan.uid, plan.gid),
		"--", plan.path,
	}
}

// applyShareMembers changes who may mount one share, moving the directory to a
// new owner first when needed and putting it back if Samba refuses the result.
// leaving is an account about to be deleted, or "".
func applyShareMembers(share model2.SharesDBModel, members []model2.ShareMember, timeMachine bool, leaving string) (model2.SharesDBModel, *reownPlan, *skippedReown, error) {
	owner, err := chooseShareOwner(share.Username, members, leaving)
	if err != nil {
		return share, nil, nil, err
	}

	return applyShareMembersAs(share, members, timeMachine, owner)
}

// applyShareMembersAs is applyShareMembers with the owner chosen already. The
// walk it returns is left to the caller, to start once every change it makes
// together has worked.
func applyShareMembersAs(share model2.SharesDBModel, members []model2.ShareMember, timeMachine bool, owner string) (model2.SharesDBModel, *reownPlan, *skippedReown, error) {
	path, err := validateSharePath(share.Path)
	if err != nil {
		return share, nil, nil, err
	}

	plan, skipped, err := moveShareOwnership(path, share.Username, owner)
	if err != nil {
		return share, nil, nil, err
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

		return share, nil, nil, err
	}

	share.Username = owner
	share.Members = model2.FormatMembers(members)
	share.Anonymous = owner == ""
	share.TimeMachine = timeMachine

	return share, plan, skipped, nil
}

// shareChange is one share and the members it is to have.
type shareChange struct {
	share   model2.SharesDBModel
	members []model2.ShareMember
}

// applyShareChanges applies changes in order. When one fails, the ones already
// applied are put back as they were, newest first, so a refused request leaves
// the shares as they were; what could not be put back is said in the error.
// No file is re-owned until every change has worked, so putting one back only
// gives the folder back. It returns the folders whose old owner's files were
// left as they are (see reownRefused).
func applyShareChanges(changes []shareChange, leaving string) ([]string, error) {
	type done struct{ before, after model2.SharesDBModel }

	applied := []done{}
	plans := []reownPlan{}
	skipped := []string{}

	for _, c := range changes {
		after, plan, skip, err := applyShareMembers(c.share, c.members, c.share.TimeMachine, leaving)
		if err == nil {
			applied = append(applied, done{before: c.share, after: after})

			if plan != nil {
				plans = append(plans, *plan)
			}

			if skip != nil {
				skipped = append(skipped, skip.path)
			}

			continue
		}

		notBack := []string{}

		for i := len(applied) - 1; i >= 0; i-- {
			d := applied[i]

			// the members and the owner it had; its files never moved
			if _, _, _, undoErr := applyShareMembersAs(d.after, d.before.MemberList(), d.before.TimeMachine, d.before.Username); undoErr != nil {
				notBack = append(notBack, d.before.Path)
			}
		}

		if len(notBack) > 0 {
			return nil, fmt.Errorf("%s: %w; these shares could not be put back as they were: %s", c.share.Path, err, strings.Join(notBack, ", "))
		}

		return nil, fmt.Errorf("%s: %w", c.share.Path, err)
	}

	for _, plan := range plans {
		reownShareContents(plan)
	}

	return skipped, nil
}

// skippedMessage is the success message, saying which folders kept the old
// owner's files.
func skippedMessage(skipped []string) string {
	if len(skipped) == 0 {
		return common_err.GetMsg(common_err.SUCCESS)
	}

	return "the files the old owner had in " + strings.Join(skipped, ", ") +
		" keep their owner: containers may keep data there under the same uid, so they are not changed. Hand them over by hand if needed."
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

	// the account may become a share's owner, whose uid every session writes as
	if !isShareAccount(username) {
		return ctx.JSON(common_err.CLIENT_ERROR, model.Result{Success: common_err.CLIENT_ERROR, Message: errShareMemberNotShareAccount.Error()})
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

	skipped, err := applyShareChanges(changes, "")
	if err != nil {
		return ctx.JSON(common_err.SERVICE_ERROR, model.Result{Success: common_err.SERVICE_ERROR, Message: err.Error()})
	}

	return ctx.JSON(common_err.SUCCESS, model.Result{Success: common_err.SUCCESS, Message: skippedMessage(skipped), Data: username})
}
