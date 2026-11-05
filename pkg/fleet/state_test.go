package fleet

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
)

func TestMasterStateRoundTripAnd0600(t *testing.T) {
	f := New(t.TempDir())
	want := MasterState{Enabled: true, Address: "https://m", Boxes: []Box{{ID: "b1", Name: "Maison", KeyHash: "h"}}}
	if err := f.saveMaster(want); err != nil {
		t.Fatal(err)
	}
	got, ok := f.loadMaster()
	info, _ := os.Stat(filepath.Join(f.Root, StateDir, "master.json"))
	if !ok || !reflect.DeepEqual(got, want) || (runtime.GOOS != "windows" && info.Mode().Perm() != 0o600) {
		t.Fatalf("%+v %v %v", got, ok, info.Mode())
	}
}

func TestMissingOrBrokenFilesAreRoleOff(t *testing.T) {
	f := New(t.TempDir())
	if _, ok := f.loadMaster(); ok {
		t.Fatal("missing master.json read as on")
	}
	if _, ok := f.loadMember(); ok {
		t.Fatal("missing member.json read as on")
	}
	os.MkdirAll(filepath.Join(f.Root, StateDir), 0o700)
	os.WriteFile(filepath.Join(f.Root, StateDir, "member.json"), []byte("{"), 0o600)
	logged := 0
	f.Log = func(string, error) { logged++ }
	if _, ok := f.loadMember(); ok || logged != 1 {
		t.Fatalf("broken member.json: ok %v logged %d", ok, logged)
	}
}

// A master.json or member.json edited by hand to a plain-HTTP remote master is a file the code rule refused: role off.
func TestLoadRefusesANonHTTPSMasterOutsideLoopback(t *testing.T) {
	f := New(t.TempDir())
	f.saveMember(MemberState{MasterURL: "http://10.0.0.2", BoxID: "b", Key: "k"})
	if _, ok := f.loadMember(); ok {
		t.Fatal("member accepted")
	}
	f.saveMaster(MasterState{Enabled: true, Address: "http://10.0.0.2"})
	if _, ok := f.loadMaster(); ok {
		t.Fatal("master accepted")
	}
}

func TestClearMemberRemovesTheFile(t *testing.T) {
	f := New(t.TempDir())
	if err := f.saveMember(MemberState{MasterURL: "https://m", BoxID: "b", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if err := f.clearMember(); err != nil {
		t.Fatal(err)
	}
	if _, ok := f.loadMember(); ok {
		t.Fatal("still on")
	}
	if _, err := os.Stat(filepath.Join(f.Root, StateDir, "member.json")); !os.IsNotExist(err) {
		t.Fatalf("file still there: %v", err)
	}
	if err := f.clearMember(); err != nil {
		t.Fatalf("clearing twice: %v", err)
	}
}

func TestMemberLinkReadsMemberJSON(t *testing.T) {
	f := New(t.TempDir())
	if _, _, ok := f.MemberLink(); ok {
		t.Fatal("no member.json read as a member")
	}
	if err := f.saveMember(MemberState{MasterURL: "https://m", BoxID: "b1", Key: "k"}); err != nil {
		t.Fatal(err)
	}
	if m, b, ok := f.MemberLink(); m != "https://m" || b != "b1" || !ok {
		t.Fatalf("%q %q %v", m, b, ok)
	}
}
