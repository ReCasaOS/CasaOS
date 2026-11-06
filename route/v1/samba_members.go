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

		// a member the share has already stays as it is, even once its
		// account is gone: a client sends the whole list back unchanged
		if !already[member.Username] {
			if _, err := user.Lookup(member.Username); err != nil {
				return errors.New(common_err.GetMsg(common_err.USER_NOT_EXIST))
			}

			// a member may become the owner: never root, www-data or a person's login
			if !isShareAccount(member.Username) {
				return errShareMemberNotShareAccount
			}
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
// next one, has been deleted. prune are the roots of other shares inside path,
// which have owners and walks of their own.
type reownPlan struct {
	path     string
	oldUID   int
	uid, gid int
	prune    []string
}

// skippedReown is set when an owner change leaves the old owner's files as
// they are: on a folder where containers keep theirs (see reownRefused), or
// on one that no longer belongs to the old owner.
type skippedReown struct {
	path   string
	reason string
}

const (
	skipContainers = "containers"
	skipNotOwned   = "not-owned"
)

// moveShareOwnership hands a shared directory from one owner to the next, and
// returns the walk left to do once Samba has taken the change, or nil.
//
// Guest to account and back only touch the directory itself, as they always
// have. Account to account also re-owns the old owner's files inside, since
// they keep the old owner and a 0660 mask that would lock the new one out. Only
// those, and never on /DATA, AppData or a whole disk (reownRefused): there a
// share account's uid can be a container's too (postgres's 999). The old
// owner's uid is its account's while it exists; the folder's owner only stands
// in for an account deleted from the shell, and only when no account has that
// uid. A folder that was given to somebody else since keeps its files: that
// uid may be a person's login or a container's.
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

	folder, err := readFolder(path)
	if err != nil {
		return nil, nil, err
	}

	oldUID, owned := previousOwnerUID(from, folder.uid)

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

	if !owned {
		return nil, &skippedReown{path: path, reason: skipNotOwned}, nil
	}

	// root's files are never handed over, and the same uid has nothing to move
	if oldUID == 0 || oldUID == uid {
		return nil, nil, nil
	}

	if reownRefused(path) {
		return nil, &skippedReown{path: path, reason: skipContainers}, nil
	}

	return &reownPlan{path: path, oldUID: oldUID, uid: uid, gid: gid}, nil, nil
}

// previousOwnerUID is the uid whose files an owner change hands over, and
// whether the folder is still that owner's.
func previousOwnerUID(from string, folderUID int) (int, bool) {
	account, err := lookupUser(from)
	if err == nil {
		uid, err := strconv.Atoi(account.Uid)

		return uid, err == nil && uid == folderUID
	}

	// only an account that is known to be gone hands its folder's uid over: a
	// lookup that failed (NSS down, an unreadable passwd) says nothing
	if !errors.As(err, new(user.UnknownUserError)) {
		return folderUID, false
	}

	// the account is gone: the folder's uid is its, unless an account has it
	_, err = lookupUID(strconv.Itoa(folderUID))

	return folderUID, errors.As(err, new(user.UnknownUserIdError))
}

var (
	lookupUser = user.Lookup
	lookupUID  = user.LookupId
)

// folderState is a share folder's owner and mode, as an undo puts it back.
type folderState struct {
	uid, gid int
	mode     os.FileMode
}

// readFolder reads a share folder's owner and mode; restoreFolder puts them
// back by number, which works for an owner whose account is gone.
var readFolder = func(path string) (folderState, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return folderState{}, err
	}

	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return folderState{}, errors.New("cannot read the owner of " + path)
	}

	return folderState{uid: int(stat.Uid), gid: int(stat.Gid), mode: info.Mode() & (os.ModePerm | os.ModeSetuid | os.ModeSetgid | os.ModeSticky)}, nil
}

var restoreFolder = func(path string, state folderState) error {
	if err := os.Lchown(path, state.uid, state.gid); err != nil {
		return err
	}

	return os.Chmod(path, state.mode)
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

// sharesInside lists the roots of the other shares under path.
func sharesInside(path string, shares []model2.SharesDBModel) []string {
	inside := []string{}

	for _, share := range shares {
		// a guest share inside has no walk of its own: the outer
		// owner's files in it are handed over with the rest
		if share.Username != "" && strings.HasPrefix(share.Path, path+"/") {
			inside = append(inside, share.Path)
		}
	}

	return inside
}

// The walks run one at a time, in the order they were asked for, on one
// worker: alice→bob then bob→carol must not run the other way round.
var (
	walksMu      sync.Mutex
	walks        []reownPlan
	walkerActive bool
)

// reownShareContents queues a plan for the worker. A variable so the tests
// never chown anything.
var reownShareContents = queueReown

// runWalk runs one plan; a variable so the tests can watch the order.
var runWalk = runReown

func queueReown(plan reownPlan) {
	walksMu.Lock()
	defer walksMu.Unlock()

	walks = append(walks, plan)
	if !walkerActive {
		walkerActive = true
		go drainWalks()
	}
}

func drainWalks() {
	for {
		walksMu.Lock()
		if len(walks) == 0 {
			walkerActive = false
			walksMu.Unlock()

			return
		}

		plan := walks[0]
		walks = walks[1:]
		walksMu.Unlock()

		runWalk(plan)
	}
}

func runReown(plan reownPlan) {
	// find walks with fts relative to each directory and never follows a
	// symlink (-P); -execdir runs chown inside the file's own folder on ./name,
	// and -h changes a link itself, so a folder swapped for a link mid-walk
	// cannot send it elsewhere. -xdev stays off disks mounted inside, and other
	// shares inside are pruned. chown goes on past a file it cannot change
	// (EPERM, a read-only mount) and --from checks the owner once more.
	output, err := exec.Command("find", reownArgs(plan, mountPointsUnder(plan.path))...).CombinedOutput()
	if err != nil {
		logger.Error("some of a share's files could not be handed to its new owner",
			zap.String("path", plan.path), zap.String("output", strings.TrimSpace(string(output))), zap.Error(err))
	}
}

// reownArgs are find's arguments for a plan; skip are folders not to enter
// (other shares, mount points). The + makes chown read the ids as numbers,
// never as names.
func reownArgs(plan reownPlan, skip []string) []string {
	args := []string{"-P", plan.path, "-xdev"}

	prune := append(append([]string{}, plan.prune...), skip...)
	if len(prune) > 0 {
		args = append(args, "(")

		for i, p := range prune {
			if i > 0 {
				args = append(args, "-o")
			}

			args = append(args, "-path", globEscape(p))
		}

		args = append(args, ")", "-prune", "-o")
	}

	return append(args,
		"-uid", strconv.Itoa(plan.oldUID),
		"-execdir", "chown", "-h",
		fmt.Sprintf("--from=+%d", plan.oldUID),
		fmt.Sprintf("+%d:+%d", plan.uid, plan.gid),
		"{}", "+")
}

// globEscape makes a path match itself in find's -path.
func globEscape(path string) string {
	var b strings.Builder

	for _, r := range path {
		if strings.ContainsRune(`*?[]\`, r) {
			b.WriteByte('\\')
		}

		b.WriteRune(r)
	}

	return b.String()
}

// mountPointsUnder lists what is mounted under path, from the mount table: a
// mount point's own folder belongs to the disk mounted there, which -xdev
// alone would still change.
var mountPointsUnder = func(path string) []string {
	data, err := os.ReadFile("/proc/self/mountinfo")
	if err != nil {
		return nil
	}

	points := []string{}

	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		point := unescapeMount(fields[4])
		if strings.HasPrefix(point, path+"/") {
			points = append(points, point)
		}
	}

	return points
}

// unescapeMount reads the octal escapes (\040 for a space) of the mount table.
func unescapeMount(s string) string {
	var b strings.Builder

	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3

				continue
			}
		}

		b.WriteByte(s[i])
	}

	return b.String()
}

// applyShareMembers changes who may mount one share, moving the directory to a
// new owner first when needed and putting it back if Samba refuses the result.
// leaving is an account about to be deleted, or "". The walk it returns is left
// to the caller, to start once every change it makes together has worked.
func applyShareMembers(share model2.SharesDBModel, members []model2.ShareMember, timeMachine bool, leaving string) (model2.SharesDBModel, *reownPlan, *skippedReown, error) {
	after, plan, skipped, _, err := applyShareMembersUndoable(share, members, timeMachine, leaving)

	return after, plan, skipped, err
}

// applyShareMembersUndoable is applyShareMembers that also returns how to put
// the share and its folder back as they were. The folder goes back by number,
// owner, group and mode, which works when its owner's account is gone.
func applyShareMembersUndoable(share model2.SharesDBModel, members []model2.ShareMember, timeMachine bool, leaving string) (model2.SharesDBModel, *reownPlan, *skippedReown, func() error, error) {
	owner, err := chooseShareOwner(share.Username, members, leaving)
	if err != nil {
		return share, nil, nil, nil, err
	}

	path, err := validateSharePath(share.Path)
	if err != nil {
		return share, nil, nil, nil, err
	}

	moved := owner != share.Username

	var folder folderState
	if moved {
		if folder, err = readFolder(path); err != nil {
			return share, nil, nil, nil, err
		}
	}

	putFolderBack := func() error {
		if !moved {
			return nil
		}

		return restoreFolder(path, folder)
	}

	plan, skipped, err := moveShareOwnership(path, share.Username, owner)
	if err != nil {
		// it may have changed the folder before failing
		_ = putFolderBack()

		return share, nil, nil, nil, err
	}

	shares := service.MyService.Shares()
	id := strconv.FormatUint(uint64(share.ID), 10)

	if err := shares.UpdateShare(id, owner, members, timeMachine); err != nil {
		_ = putFolderBack()

		return share, nil, nil, nil, err
	}

	if plan != nil {
		plan.prune = sharesInside(path, shares.GetSharesList())
	}

	before := share
	undo := func() error {
		return errors.Join(shares.UpdateShare(id, before.Username, before.MemberList(), before.TimeMachine), putFolderBack())
	}

	share.Username = owner
	share.Members = model2.FormatMembers(members)
	share.Anonymous = owner == ""
	share.TimeMachine = timeMachine

	return share, plan, skipped, undo, nil
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
// left as they are.
func applyShareChanges(changes []shareChange, leaving string) ([]skippedReown, error) {
	type done struct {
		path string
		undo func() error
	}

	applied := []done{}
	plans := []reownPlan{}
	skipped := []skippedReown{}

	for _, c := range changes {
		_, plan, skip, undo, err := applyShareMembersUndoable(c.share, c.members, c.share.TimeMachine, leaving)
		if err == nil {
			applied = append(applied, done{path: c.share.Path, undo: undo})

			if plan != nil {
				plans = append(plans, *plan)
			}

			if skip != nil {
				skipped = append(skipped, *skip)
			}

			continue
		}

		notBack := []string{}

		for i := len(applied) - 1; i >= 0; i-- {
			// the members, the owner and the folder it had; its files never moved
			if undoErr := applied[i].undo(); undoErr != nil {
				notBack = append(notBack, applied[i].path)
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
// owner's files, and why.
func skippedMessage(skipped []skippedReown) string {
	containers, notOwned := []string{}, []string{}

	for _, s := range skipped {
		if s.reason == skipNotOwned {
			notOwned = append(notOwned, s.path)
		} else {
			containers = append(containers, s.path)
		}
	}

	parts := []string{}

	if len(containers) > 0 {
		parts = append(parts, "the files the old owner had in "+strings.Join(containers, ", ")+
			" keep their owner: containers may keep data there under the same uid, so they are not changed.")
	}

	if len(notOwned) > 0 {
		parts = append(parts, "the files in "+strings.Join(notOwned, ", ")+
			" keep their owner: the folder belonged to another account than the share's owner, so it is not known whose files are the share's.")
	}

	if len(parts) == 0 {
		return common_err.GetMsg(common_err.SUCCESS)
	}

	return strings.Join(parts, " ") + " Hand them over by hand if needed."
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
