package osmodule

import "testing"

func TestValidNameIsTheRuleOfTheManifest(t *testing.T) {
	for _, ok := range []string{"a", "virt", "a-b-c", "x1", "abcdefghijklmnopqrstuvwxyz012345"} {
		if !ValidName(ok) {
			t.Errorf("%q is a name of a module", ok)
		}
	}
	for _, bad := range []string{"", "V", "Virt", "1virt", "-virt", "vi rt", "vi_rt", "virt\n", "virt/..", "../virt", "a.b", "abcdefghijklmnopqrstuvwxyz0123456", "vïrt", "virt\x00"} {
		if ValidName(bad) {
			t.Errorf("%q is not a name of a module", bad)
		}
	}
}
