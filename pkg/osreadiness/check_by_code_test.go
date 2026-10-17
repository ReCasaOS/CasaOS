package osreadiness

import (
	"context"
	"io/fs"
	"testing"
)

// A check that is asked for by its code is the one of the table, run the way the report runs it: the finding is the report's own for that code.
func TestACheckAskedForByItsCodeIsTheReportsOwn(t *testing.T) {
	f := newBox(t)
	fstab("UUID=1234-ABCD /data ext4 defaults 0 2")(f)

	got, ok := CheckByCode(context.Background(), f, "fstab_unresolved")
	if !ok {
		t.Fatal("the check is in the table")
	}
	var want Finding
	for _, finding := range Run(context.Background(), f, Options{}).Checks {
		if finding.Code == "fstab_unresolved" {
			want = finding
		}
	}
	if got.Severity != Refuse || got.Code != "fstab_unresolved" || got.Title != want.Title || got.Detail != want.Detail {
		t.Errorf("finding = %+v, want the report's %+v", got, want)
	}
	if mounts, _ := got.Data["mounts"].([]string); len(mounts) != 1 || mounts[0] != "/data" {
		t.Errorf("data = %v", got.Data)
	}
}

// The barrier asks for three of them, and a machine that says nothing against them says OK, or a word that is not a refusal.
func TestTheChecksOfTheBarrierCanBeAskedForOneByOne(t *testing.T) {
	f := newBox(t)
	for code, severity := range map[string]Severity{"fstab_unresolved": OK, "fstab_network": OK, "network_names": Info} {
		got, ok := CheckByCode(context.Background(), f, code)
		if !ok || got.Severity != severity || got.Data["unknown"] != false {
			t.Errorf("%s: %+v (ok %v), want %s and a known answer", code, got, ok, severity)
		}
	}
}

func TestACodeThatIsNotInTheTableIsNoCheck(t *testing.T) {
	for _, code := range []string{"", "nothing", "FSTAB_UNRESOLVED", "fstab_unresolved "} {
		if finding, ok := CheckByCode(context.Background(), newBox(t), code); ok {
			t.Errorf("%q gave %+v", code, finding)
		}
	}
}

// A check that cannot read what it needs is a refusal that says it does not know, as in the report: a barrier that cannot be read is red.
func TestACheckThatCannotReadIsARefusalThatSaysSo(t *testing.T) {
	f := newBox(t)
	f.fail(fstabPath, &fs.PathError{Op: "open", Path: fstabPath, Err: fs.ErrPermission})
	got, ok := CheckByCode(context.Background(), f, "fstab_unresolved")
	if !ok || got.Severity != Refuse || got.Data["unknown"] != true {
		t.Errorf("finding = %+v", got)
	}
}

// What the check runs goes through the allow-list whatever the Probe is: a check that wanted a command that is not on it would be refused, here as in the report.
func TestTheProbeOfACheckAskedForByCodeRunsOnlyWhatIsAllowed(t *testing.T) {
	var asked []string
	register(Check{Code: "zz_by_code_probe", Group: GroupHop, Title: "a check that wants to run a command", Run: func(ctx context.Context, p Probe) Result {
		_, err := p.Run(ctx, "rm", "-rf", "/")
		asked = append(asked, "rm")
		if err == nil {
			return Result{Severity: Refuse, Title: "it ran"}
		}
		return Result{Severity: OK}
	}})
	t.Cleanup(func() { registry = registry[:len(registry)-1] })

	// a machine that would answer the command: the allow-list is what says no
	f := newBox(t)
	f.on("", 0, "rm", "-rf", "/")
	got, ok := CheckByCode(context.Background(), f, "zz_by_code_probe")
	if !ok || got.Severity != OK || len(asked) != 1 {
		t.Errorf("finding = %+v (ok %v, asked %v): the command was not refused", got, ok, asked)
	}
}
