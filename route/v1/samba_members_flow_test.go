package v1

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"

	"github.com/labstack/echo/v4"

	"github.com/ReCasaOS/CasaOS/service"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

// Accounts every Linux box has; the tests call them share accounts.
const (
	alice = "daemon"
	bob   = "bin"
)

// fakeShares keeps the shares in memory and records what was asked of it.
type fakeShares struct {
	service.SharesService
	shares  []model2.SharesDBModel
	updates []string
	failOn  map[uint]bool
}

func (f *fakeShares) GetSharesList() []model2.SharesDBModel {
	return append([]model2.SharesDBModel{}, f.shares...)
}

func (f *fakeShares) UpdateShare(id, owner string, members []model2.ShareMember, timeMachine bool) error {
	n, _ := strconv.Atoi(id)
	if f.failOn[uint(n)] {
		return errors.New("testparm refused it")
	}

	for i := range f.shares {
		if f.shares[i].ID == uint(n) {
			f.shares[i].Username = owner
			f.shares[i].Members = model2.FormatMembers(members)
			f.shares[i].Anonymous = owner == ""
		}
	}

	f.updates = append(f.updates, id+" "+owner+" "+model2.FormatMembers(members))

	return nil
}

type fakeSharesRepository struct {
	service.Repository
	shares *fakeShares
}

func (f fakeSharesRepository) Shares() service.SharesService { return f.shares }

// sharesRig puts the shares in a fake service and records, in order, what the
// changes do to the system instead of doing it.
func sharesRig(t *testing.T, shares ...model2.SharesDBModel) (*fakeShares, *[]string) {
	t.Helper()

	fake := &fakeShares{shares: shares, failOn: map[uint]bool{}}
	log := &[]string{}

	previous := service.MyService
	saved := []interface{}{isShareAccount, deleteShareAccount, validateSharePath, restrictShare, releaseShare, shareFolderOwner, isMountPoint, reownShareContents}

	// who owns each folder, as restrictShare leaves it
	owners := map[string]string{}
	for _, share := range shares {
		owners[share.Path] = share.Username
	}

	service.MyService = fakeSharesRepository{shares: fake}
	isShareAccount = func(name string) bool { return name == alice || name == bob }
	deleteShareAccount = func(name string) error {
		*log = append(*log, "delete "+name)
		return nil
	}
	validateSharePath = func(path string) (string, error) { return path, nil }
	restrictShare = func(path, name string) error {
		owners[path] = name
		*log = append(*log, "own "+path+" "+name)
		return nil
	}
	releaseShare = func(path string) error {
		owners[path] = ""
		*log = append(*log, "release "+path)
		return nil
	}
	shareFolderOwner = func(path string) (int, error) {
		if owners[path] == "" {
			return 0, nil
		}

		account, err := user.Lookup(owners[path])
		if err != nil {
			return 0, err
		}

		return strconv.Atoi(account.Uid)
	}
	isMountPoint = func(string) bool { return false }
	reownShareContents = func(plan reownPlan) {
		*log = append(*log, fmt.Sprintf("reown %s %d->%d", plan.path, plan.oldUID, plan.uid))
	}

	t.Cleanup(func() {
		service.MyService = previous
		isShareAccount = saved[0].(func(string) bool)
		deleteShareAccount = saved[1].(func(string) error)
		validateSharePath = saved[2].(func(string) (string, error))
		restrictShare = saved[3].(func(string, string) error)
		releaseShare = saved[4].(func(string) error)
		shareFolderOwner = saved[5].(func(string) (int, error))
		isMountPoint = saved[6].(func(string) bool)
		reownShareContents = saved[7].(func(reownPlan))
	})

	return fake, log
}

func callShares(t *testing.T, handler echo.HandlerFunc, method, username, body string) (int, string) {
	t.Helper()

	e := echo.New()
	request := httptest.NewRequest(method, "/v1/samba/users/"+username+"/shares", strings.NewReader(body))
	request.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
	recorder := httptest.NewRecorder()
	ctx := e.NewContext(request, recorder)
	ctx.SetParamNames("username")
	ctx.SetParamValues(username)

	if err := handler(ctx); err != nil {
		t.Fatal(err)
	}

	return recorder.Code, recorder.Body.String()
}

func protected(id uint, path, owner, members string) model2.SharesDBModel {
	return model2.SharesDBModel{ID: id, Path: path, Username: owner, Members: members}
}

func TestPutSambaUserSharesRefusesAndLeavesEverything(t *testing.T) {
	cases := []struct {
		name, body, says string
	}{
		{"a guest share", `[{"id":1},{"id":2},{"id":3}]`, "open to everyone"},
		{"a share left with nobody", `[{"id":1}]`, "needs at least one account"},
		{"a share that does not exist", `[{"id":1},{"id":2},{"id":42}]`, "no such share"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fake, log := sharesRig(t,
				protected(1, "/DATA/Media", alice, alice+":rw "+bob+":ro"),
				protected(2, "/DATA/Private", alice, alice+":rw"),
				model2.SharesDBModel{ID: 3, Path: "/DATA/Public", Anonymous: true},
			)

			code, body := callShares(t, PutSambaUserShares, http.MethodPut, alice, c.body)

			if code == http.StatusOK || !strings.Contains(body, c.says) {
				t.Fatalf("%d %s, want a refusal saying %q", code, body, c.says)
			}

			if len(fake.updates) > 0 || len(*log) > 0 {
				t.Fatalf("a refused request changed %v %v", fake.updates, *log)
			}
		})
	}
}

// a failure in the middle puts back the shares changed before it
func TestPutSambaUserSharesPutsBackWhatItChangedWhenOneFails(t *testing.T) {
	fake, _ := sharesRig(t,
		protected(1, "/DATA/A", alice, alice+":rw"),
		protected(2, "/DATA/B", alice, alice+":rw"),
	)
	fake.failOn[2] = true

	code, body := callShares(t, PutSambaUserShares, http.MethodPut, bob, `[{"id":1},{"id":2,"read_only":true}]`)

	if code == http.StatusOK || !strings.Contains(body, "/DATA/B") {
		t.Fatalf("%d %s", code, body)
	}

	if got := fake.shares[0].Members; got != alice+":rw" {
		t.Errorf("share 1 is %q, want it back as %q", got, alice+":rw")
	}

	if len(fake.updates) != 2 || !strings.HasSuffix(fake.updates[1], alice+":rw") {
		t.Errorf("updates %v, want the change and its undo", fake.updates)
	}
}

func TestDeleteSambaUserHandsItsSharesOverBeforeTheAccountGoes(t *testing.T) {
	fake, log := sharesRig(t,
		protected(1, "/DATA/Media", alice, alice+":rw "+bob+":ro"),
		protected(2, "/DATA/Other", bob, bob+":rw "+alice+":ro"),
	)

	if code, body := callShares(t, DeleteSambaUser, http.MethodDelete, alice, ""); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}

	if fake.shares[0].Username != bob || fake.shares[0].Members != bob+":ro" {
		t.Errorf("the share alice owned is %+v, want it bob's", fake.shares[0])
	}

	if fake.shares[1].Username != bob || fake.shares[1].Members != bob+":rw" {
		t.Errorf("the other share is %+v, want alice out of it", fake.shares[1])
	}

	if last := (*log)[len(*log)-1]; last != "delete "+alice {
		t.Errorf("the account went before its shares were changed: %v", *log)
	}

	if (*log)[0] != "own /DATA/Media "+bob {
		t.Errorf("the share was not handed to bob: %v", *log)
	}
}

func TestDeleteSambaUserKeepsTheAccountWhenASharesRefuses(t *testing.T) {
	fake, log := sharesRig(t,
		protected(1, "/DATA/A", alice, alice+":rw "+bob+":rw"),
		protected(2, "/DATA/B", alice, alice+":rw "+bob+":rw"),
	)
	fake.failOn[2] = true

	if code, _ := callShares(t, DeleteSambaUser, http.MethodDelete, alice, ""); code == http.StatusOK {
		t.Fatal("a refused share should fail the delete")
	}

	for _, entry := range *log {
		if strings.HasPrefix(entry, "delete ") {
			t.Fatalf("the account was deleted although a share still names it: %v", *log)
		}
	}

	if fake.shares[0].Username != alice || fake.shares[0].Members != alice+":rw "+bob+":rw" {
		t.Errorf("share 1 is %+v, want it back as it was", fake.shares[0])
	}
}

func TestDeleteSambaUserRefusesTheLastMember(t *testing.T) {
	_, log := sharesRig(t, protected(1, "/DATA/Mine", alice, alice+":rw"))

	if code, body := callShares(t, DeleteSambaUser, http.MethodDelete, alice, ""); code == http.StatusOK || !strings.Contains(body, "only one") {
		t.Fatalf("%d %s", code, body)
	}

	if len(*log) > 0 {
		t.Fatalf("%v", *log)
	}
}

func TestOnlyShareAccountsCanBeMembers(t *testing.T) {
	sharesRig(t)

	if err := validateShareMembers([]model2.ShareMember{{Username: "root"}}); !errors.Is(err, errShareMemberNotShareAccount) {
		t.Errorf("root as a member: %v", err)
	}

	if err := validateShareMembers([]model2.ShareMember{{Username: alice}, {Username: bob, ReadOnly: true}}); err != nil {
		t.Errorf("two share accounts: %v", err)
	}

	// a member the share has already stays, whatever account it is: an older
	// share made on another account is not broken by a change of anything else
	if err := validateShareMembers([]model2.ShareMember{{Username: "root"}, {Username: alice}}, model2.ShareMember{Username: "root"}); err != nil {
		t.Errorf("a member kept: %v", err)
	}
}

func TestChooseShareOwnerPassesOverTheAccountBeingDeleted(t *testing.T) {
	sharesRig(t)

	members := []model2.ShareMember{{Username: bob, ReadOnly: true}}
	if owner, err := chooseShareOwner(alice, members, alice); err != nil || owner != bob {
		t.Errorf("got %q %v, want %q", owner, err, bob)
	}
}

// a member kept from an older share on another account never becomes the
// owner: every session would write as it
func TestChooseShareOwnerPicksOnlyShareAccounts(t *testing.T) {
	sharesRig(t)

	members := []model2.ShareMember{{Username: "root"}, {Username: bob, ReadOnly: true}}
	if owner, err := chooseShareOwner("gone-account-xyz", members, ""); err != nil || owner != bob {
		t.Errorf("got %q %v, want %q", owner, err, bob)
	}

	if _, err := chooseShareOwner("gone-account-xyz", []model2.ShareMember{{Username: "root"}}, ""); !errors.Is(err, errShareWithoutOwner) {
		t.Errorf("only root left: %v", err)
	}
}

func TestPutSambaUserSharesRefusesAnAccountThatIsNotAShareAccount(t *testing.T) {
	fake, log := sharesRig(t, protected(1, "/DATA/Media", alice, alice+":rw"))

	code, body := callShares(t, PutSambaUserShares, http.MethodPut, "root", `[{"id":1}]`)
	if code == http.StatusOK || !strings.Contains(body, "only a share account") {
		t.Fatalf("%d %s", code, body)
	}

	if len(fake.updates) > 0 || len(*log) > 0 {
		t.Fatalf("%v %v", fake.updates, *log)
	}
}

// nothing changes before the account is known to be deletable
func TestDeleteSambaUserRefusesAnotherAccountBeforeTouchingAnything(t *testing.T) {
	fake, log := sharesRig(t, protected(1, "/DATA/Old", "root", "root:rw "+bob+":rw"))

	code, body := callShares(t, DeleteSambaUser, http.MethodDelete, "root", "")
	if code == http.StatusOK || !strings.Contains(body, "not created by CasaOS") {
		t.Fatalf("%d %s", code, body)
	}

	if len(fake.updates) > 0 || len(*log) > 0 {
		t.Fatalf("%v %v", fake.updates, *log)
	}
}

func TestDeleteSambaUserSaysTheSharesChangedWhenTheAccountStays(t *testing.T) {
	_, _ = sharesRig(t, protected(1, "/DATA/Media", alice, alice+":rw "+bob+":rw"))
	deleteShareAccount = func(string) error { return errors.New("userdel: user alice is currently used by process 42") }

	code, body := callShares(t, DeleteSambaUser, http.MethodDelete, alice, "")
	if code == http.StatusOK || !strings.Contains(body, "delete it again") {
		t.Fatalf("%d %s", code, body)
	}
}

// the walk takes the ids it was planned with, and runs only once Samba took
// the change
func TestTheWalkIsPlannedWithIDsAndStartsAfterUpdateShare(t *testing.T) {
	fake, log := sharesRig(t, protected(1, "/DATA/Media", alice, alice+":rw "+bob+":rw"))

	if code, body := callShares(t, DeleteSambaUser, http.MethodDelete, alice, ""); code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}

	a, _ := user.Lookup(alice)
	b, _ := user.Lookup(bob)
	want := "reown /DATA/Media " + a.Uid + "->" + b.Uid

	if fmt.Sprint(*log) != fmt.Sprint([]string{"own /DATA/Media " + bob, want, "delete " + alice}) {
		t.Errorf("log %v", *log)
	}

	if len(fake.updates) != 1 {
		t.Errorf("updates %v", fake.updates)
	}
}

func TestNoWalkWhenUpdateShareFails(t *testing.T) {
	fake, log := sharesRig(t, protected(1, "/DATA/Media", alice, alice+":rw "+bob+":rw"))
	fake.failOn[1] = true

	callShares(t, DeleteSambaUser, http.MethodDelete, alice, "")

	for _, entry := range *log {
		if strings.HasPrefix(entry, "reown") {
			t.Fatalf("a walk started for a share Samba refused: %v", *log)
		}
	}
}

// a batch that fails puts the folders back and starts no walk at all
func TestNoWalkWhenALaterShareFails(t *testing.T) {
	fake, log := sharesRig(t,
		protected(1, "/DATA/A", alice, alice+":rw "+bob+":rw"),
		protected(2, "/DATA/B", alice, alice+":rw "+bob+":rw"),
	)
	fake.failOn[2] = true

	callShares(t, DeleteSambaUser, http.MethodDelete, alice, "")

	for _, entry := range *log {
		if strings.HasPrefix(entry, "reown") {
			t.Fatalf("%v", *log)
		}
	}

	if (*log)[len(*log)-1] != "own /DATA/A "+alice {
		t.Errorf("share A's folder is not back with alice: %v", *log)
	}
}

// a share whose owning account was deleted from the shell can still change:
// the old owner is whoever owns the folder
func TestAShareWhoseOwnerIsGoneCanChange(t *testing.T) {
	fake, log := sharesRig(t, protected(1, "/DATA/Media", "gone-account-xyz", "gone-account-xyz:rw "+bob+":rw"))
	shareFolderOwner = func(string) (int, error) { return 4242, nil }

	code, body := callShares(t, PutSambaUserShares, http.MethodPut, bob, `[{"id":1,"read_only":true}]`)
	if code != http.StatusOK {
		t.Fatalf("%d %s", code, body)
	}

	if fake.shares[0].Username != bob {
		t.Errorf("owner %q", fake.shares[0].Username)
	}

	b, _ := user.Lookup(bob)
	if (*log)[len(*log)-1] != "reown /DATA/Media 4242->"+b.Uid {
		t.Errorf("log %v", *log)
	}
}

// /DATA, AppData and disk roots keep the old owner's files: a container may
// write there as the same uid, and the answer says so
func TestNoWalkWhereContainersKeepTheirData(t *testing.T) {
	for _, path := range []string{"/DATA", "/DATA/AppData", "/DATA/AppData/postgres", "/mnt"} {
		t.Run(path, func(t *testing.T) {
			_, log := sharesRig(t, protected(1, path, alice, alice+":rw "+bob+":rw"))

			code, body := callShares(t, DeleteSambaUser, http.MethodDelete, alice, "")
			if code != http.StatusOK || !strings.Contains(body, "keep their owner") {
				t.Fatalf("%d %s", code, body)
			}

			for _, entry := range *log {
				if strings.HasPrefix(entry, "reown") {
					t.Fatalf("%v", *log)
				}
			}
		})
	}

	_, log := sharesRig(t, protected(1, "/DATA/Disk", alice, alice+":rw "+bob+":rw"))
	isMountPoint = func(path string) bool { return path == "/DATA/Disk" }

	callShares(t, DeleteSambaUser, http.MethodDelete, alice, "")

	for _, entry := range *log {
		if strings.HasPrefix(entry, "reown") {
			t.Fatalf("a disk root was walked: %v", *log)
		}
	}
}

func TestReownArgs(t *testing.T) {
	got := strings.Join(reownArgs(reownPlan{path: "/DATA/Media", oldUID: 899, uid: 898, gid: 897}), " ")
	if got != "-R -h --from=+899 +898:+897 -- /DATA/Media" {
		t.Errorf("got %q", got)
	}
}

// the real chown, as root only (the golang container): the old owner's files
// change hands, everybody else's and a link's target stay
func TestRunReownWithChown(t *testing.T) {
	if os.Getuid() != 0 {
		t.Skip("needs root")
	}

	root := t.TempDir()
	outside := filepath.Join(t.TempDir(), "target")
	os.WriteFile(outside, nil, 0o644)
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "mine"), nil, 0o644)
	os.WriteFile(filepath.Join(root, "theirs"), nil, 0o644)
	os.Symlink(outside, filepath.Join(root, "link"))

	for _, p := range []string{outside, filepath.Join(root, "sub"), filepath.Join(root, "sub", "mine"), filepath.Join(root, "link")} {
		os.Lchown(p, 4001, 4001)
	}
	os.Lchown(filepath.Join(root, "theirs"), 999, 999)

	runReown(reownPlan{path: root, oldUID: 4001, uid: 4002, gid: 4003})

	owner := func(p string) int {
		info, _ := os.Lstat(p)
		return int(info.Sys().(*syscall.Stat_t).Uid)
	}

	for p, want := range map[string]int{
		filepath.Join(root, "sub"):         4002,
		filepath.Join(root, "sub", "mine"): 4002,
		filepath.Join(root, "link"):        4002,
		filepath.Join(root, "theirs"):      999,
		outside:                            4001,
	} {
		if got := owner(p); got != want {
			t.Errorf("%s is %d's, want %d's", p, got, want)
		}
	}
}

func TestIsMountPoint(t *testing.T) {
	if !isMountPoint("/") {
		t.Error("/ is a mount point")
	}

	if !isMountPoint("/does/not/exist") {
		t.Error("a path it cannot read counts as a mount point")
	}
}
