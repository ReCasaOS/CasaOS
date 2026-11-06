package v1

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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
	saved := []interface{}{isShareAccount, deleteShareAccount, validateSharePath, restrictShare, releaseShare}

	service.MyService = fakeSharesRepository{shares: fake}
	isShareAccount = func(name string) bool { return name == alice || name == bob }
	deleteShareAccount = func(name string) error {
		*log = append(*log, "delete "+name)
		return nil
	}
	validateSharePath = func(path string) (string, error) { return path, nil }
	restrictShare = func(path, name string) error {
		*log = append(*log, "own "+path+" "+name)
		return nil
	}
	releaseShare = func(path string) error {
		*log = append(*log, "release "+path)
		return nil
	}

	t.Cleanup(func() {
		service.MyService = previous
		isShareAccount = saved[0].(func(string) bool)
		deleteShareAccount = saved[1].(func(string) error)
		validateSharePath = saved[2].(func(string) (string, error))
		restrictShare = saved[3].(func(string, string) error)
		releaseShare = saved[4].(func(string) error)
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
	members := []model2.ShareMember{{Username: bob, ReadOnly: true}}
	if owner := chooseShareOwner(alice, members, alice); owner != bob {
		t.Errorf("got %q, want %q", owner, bob)
	}
}

// Only the old owner's files change hands: a share on /DATA or AppData holds
// the files of containers, which keep theirs.
func TestWalkOwnedByVisitsOnlyTheOwnersFiles(t *testing.T) {
	root := t.TempDir()
	os.MkdirAll(filepath.Join(root, "sub"), 0o755)
	os.WriteFile(filepath.Join(root, "sub", "file"), []byte("x"), 0o644)
	outside := t.TempDir()
	os.Symlink(outside, filepath.Join(root, "link"))

	me := os.Getuid()

	visited := []string{}
	if err := walkOwnedBy(root, me, func(path string) error {
		visited = append(visited, strings.TrimPrefix(path, root))
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	want := map[string]bool{"": true, "/link": true, "/sub": true, "/sub/file": true}
	if len(visited) != len(want) {
		t.Fatalf("visited %v", visited)
	}

	for _, v := range visited {
		if !want[v] {
			t.Errorf("visited %q", v)
		}
	}

	none := 0
	walkOwnedBy(root, me+12345, func(string) error {
		none++
		return nil
	})

	if none != 0 {
		t.Errorf("%d files of another uid were visited", none)
	}
}
