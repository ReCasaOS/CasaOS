package v1

import (
	"errors"
	"os/user"
	"reflect"
	"testing"

	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

func stubLookups(t *testing.T, byName func(string) (*user.User, error), byID func(string) (*user.User, error)) {
	t.Helper()
	savedUser, savedUID := lookupUser, lookupUID
	lookupUser, lookupUID = byName, byID
	t.Cleanup(func() { lookupUser, lookupUID = savedUser, savedUID })
}

func TestPreviousOwnerUID(t *testing.T) {
	gone := func(name string) (*user.User, error) { return nil, user.UnknownUserError(name) }
	noAccount := func(id string) (*user.User, error) { return nil, user.UnknownUserIdError(899) }
	broken := func(string) (*user.User, error) { return nil, errors.New("nss: connection refused") }
	alice := func(string) (*user.User, error) { return &user.User{Username: "alice", Uid: "899"}, nil }

	cases := []struct {
		name   string
		byName func(string) (*user.User, error)
		byID   func(string) (*user.User, error)
		uid    int
		owned  bool
	}{
		{"the recorded owner owns the folder", alice, noAccount, 899, true},
		{"the folder belongs to someone else", func(string) (*user.User, error) { return &user.User{Uid: "898"}, nil }, noAccount, 898, false},
		{"the owner is gone and nobody has the folder's uid", gone, noAccount, 899, true},
		{"the owner is gone and an account has the folder's uid", gone, alice, 899, false},
		{"the owner's lookup fails", broken, noAccount, 899, false},
		{"the uid's lookup fails", gone, broken, 899, false},
	}

	for _, c := range cases {
		stubLookups(t, c.byName, c.byID)
		uid, owned := previousOwnerUID("alice", 899)
		if uid != c.uid || owned != c.owned {
			t.Errorf("%s: got (%d, %v), want (%d, %v)", c.name, uid, owned, c.uid, c.owned)
		}
	}
}

func TestSharesInsideSkipsOnlyProtectedShares(t *testing.T) {
	shares := []model2.SharesDBModel{
		{Path: "/DATA/Media", Username: "alice"},
		{Path: "/DATA/Media/Public"},
		{Path: "/DATA/Media/Movies", Username: "carol"},
		{Path: "/DATA/Mediateca", Username: "dan"},
	}

	got := sharesInside("/DATA/Media", shares)
	if want := []string{"/DATA/Media/Movies"}; !reflect.DeepEqual(got, want) {
		t.Errorf("got %v, want %v: a guest share inside has no walk of its own", got, want)
	}
}
