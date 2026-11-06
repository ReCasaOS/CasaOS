package v1

import (
	"testing"

	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

func TestWithMember(t *testing.T) {
	members := []model2.ShareMember{{Username: "alice"}, {Username: "bob", ReadOnly: true}}

	got := model2.FormatMembers(withMember(members, "bob", true, false))
	if got != "alice:rw bob:rw" {
		t.Errorf("changing access should keep the order, got %q", got)
	}

	got = model2.FormatMembers(withMember(members, "carol", true, true))
	if got != "alice:rw bob:ro carol:ro" {
		t.Errorf("a new member goes last, got %q", got)
	}

	got = model2.FormatMembers(withMember(members, "alice", false, false))
	if got != "bob:ro" {
		t.Errorf("removing a member, got %q", got)
	}
}

func TestChooseShareOwner(t *testing.T) {
	saved := isShareAccount
	isShareAccount = func(name string) bool { return name == "alice" || name == "bob" }
	t.Cleanup(func() { isShareAccount = saved })

	owner := func(current string, members []model2.ShareMember) string {
		got, err := chooseShareOwner(current, members, "")
		if err != nil {
			t.Fatal(err)
		}

		return got
	}

	if got := owner("root", []model2.ShareMember{{Username: "alice"}}); got != "root" {
		t.Errorf("an owner whose account exists stays, got %q", got)
	}

	members := []model2.ShareMember{{Username: "bob", ReadOnly: true}, {Username: "alice"}}
	if got := owner("gone-account-xyz", members); got != "alice" {
		t.Errorf("a missing owner should pass to a member who can write, got %q", got)
	}

	if got := owner("", []model2.ShareMember{{Username: "bob", ReadOnly: true}}); got != "bob" {
		t.Errorf("with only read-only members the first one owns, got %q", got)
	}

	if got := owner("root", nil); got != "" {
		t.Errorf("a share with no members has no owner, got %q", got)
	}
}

func TestPathSharedElsewhere(t *testing.T) {
	shares := []model2.SharesDBModel{
		{ID: 2, Path: "/DATA/Downloads"},
		{ID: 3, Path: "/DATA/Downloads"},
		{ID: 5, Path: "/DATA/Media"},
	}

	if !pathSharedElsewhere(shares, shares[0]) {
		t.Error("a duplicate row should keep the folder owned")
	}

	if pathSharedElsewhere(shares, shares[2]) {
		t.Error("the only share of a folder is not shared elsewhere")
	}
}
