package modulecmd

import (
	"strings"
	"testing"
)

func TestTheCommandLineIsTheUsageAndNothingElse(t *testing.T) {
	id := "m1-" + strings.Repeat("a1", 16)
	ok := map[string]command{
		"list":                               {verb: "list"},
		"plan virt":                          {verb: "plan", name: "virt"},
		"status":                             {verb: "status"},
		"status virt":                        {verb: "status", name: "virt"},
		"install virt":                       {verb: "install", name: "virt"},
		"install virt --yes":                 {verb: "install", name: "virt", yes: true},
		"install --yes virt":                 {verb: "install", name: "virt", yes: true},
		"install virt --plan-id " + id:       {verb: "install", name: "virt", planID: id},
		"install virt --yes --plan-id " + id: {verb: "install", name: "virt", yes: true, planID: id},
		"install --plan-id " + id + " virt":  {verb: "install", name: "virt", planID: id},
		"remove virt":                        {verb: "remove", name: "virt"},
		"remove virt --yes":                  {verb: "remove", name: "virt", yes: true},
		"install a-b-9":                      {verb: "install", name: "a-b-9"},
	}
	for line, want := range ok {
		got, err := parseArgs(strings.Fields(line))
		if err != nil || got != want {
			t.Errorf("%q: %+v, %v", line, got, err)
		}
	}
	bad := []string{
		"", "frob", "list virt", "list --yes", "plan", "plan --yes", "plan virt --yes", "plan virt --plan-id " + id, "status virt other", "status --yes",
		"install", "install --yes", "install virt other", "install virt --yes --yes", "install virt --plan-id", "install virt --plan-id nope", "install virt --plan-id " + id + " --plan-id " + id,
		"install virt --plan-id=" + id, "install virt -y", "install virt --Yes", "install virt --force", "install virt --", "install -- virt",
		"remove", "remove virt --plan-id " + id, "remove virt other", "remove virt --yes --yes",
		"install Virt", "install 1virt", "install -virt", "install virt_x", "install " + strings.Repeat("a", 33), "install ../virt", "install virt/x", "install virt.json",
		"install virt --plan-id m1-" + strings.Repeat("A", 32), "install virt --plan-id m1-" + strings.Repeat("a", 31), "install virt --plan-id m2-" + strings.Repeat("a", 32),
	}
	for _, line := range bad {
		if got, err := parseArgs(strings.Fields(line)); err == nil {
			t.Errorf("%q is taken: %+v", line, got)
		}
	}
	// the names that Fields cannot say
	for _, name := range []string{"", " virt", "virt ", "vi rt", "virt\n", "virt\x00", "é"} {
		if got, err := parseArgs([]string{"install", name}); err == nil {
			t.Errorf("the name %q is taken: %+v", name, got)
		}
	}
	if _, err := parseArgs(nil); err == nil {
		t.Error("no words")
	}
}
