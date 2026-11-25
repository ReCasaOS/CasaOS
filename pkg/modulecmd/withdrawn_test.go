package modulecmd

import (
	"context"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

const withdrawnWhy = "it loses the data of its disks"

func withdrawnList(tag string) string {
	return `[{"name":"` + testName + `","tag":"` + tag + `","reason":"` + withdrawnWhy + `"}]`
}

func planCheck(p osmodule.PlanResult, code string) (osmodule.Check, bool) {
	for _, c := range p.Checks {
		if c.Code == code {
			return c, true
		}
	}
	return osmodule.Check{}, false
}

func TestAnInstallOfAVersionTheCatalogueWithdrewIsRefused(t *testing.T) {
	f := newFixture(t)
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawnList(onlineTag), f.mirrorEntry(onlineTag, foreignRepo)))
	p := PlanOf(context.Background(), f.env, testName)
	if c, ok := planCheck(p, osmodule.CheckWithdrawn); p.Refused != osmodule.RefusedWithdrawn || !ok || c.OK || c.Detail != withdrawnWhy {
		t.Fatalf("plan: %+v", p)
	}
	before := f.treeWithoutBookkeeping()
	if code := f.install("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.RefusedWithdrawn+"\n") {
		t.Fatalf("install: exit %d\n%s", code, f.out)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
	// not withdrawn: the check passes and refuses nothing
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, "[]", f.mirrorEntry(onlineTag, foreignRepo)))
	p = PlanOf(context.Background(), f.env, testName)
	if c, ok := planCheck(p, osmodule.CheckWithdrawn); p.Refused != "" || !ok || !c.OK {
		t.Fatalf("plan: %+v", p)
	}
}

func TestAnUpdateToAVersionTheCatalogueWithdrewIsRefused(t *testing.T) {
	f := installed(t)
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawnList(onlineTag), f.mirrorEntry(onlineTag, foreignRepo)))
	p := PlanUpdateOf(context.Background(), f.env, testName)
	if c, ok := planCheck(p, osmodule.CheckWithdrawn); p.Refused != osmodule.RefusedWithdrawn || !ok || c.Detail != withdrawnWhy {
		t.Fatalf("plan: %+v", p)
	}
	f.out.Reset()
	if code := f.update("--yes"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.RefusedWithdrawn+"\n") {
		t.Fatalf("update: exit %d\n%s", code, f.out)
	}
	if v := installedVersion(f.env, testName); v == nil || *v != testTag {
		t.Errorf("the installed version is %v", v)
	}
}

// the night reads from the list whether the catalogue's tag of an installed module is withdrawn; the list's JSON does not say it
func TestListTellsTheNightTheLatestTagIsWithdrawn(t *testing.T) {
	f := installed(t)
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawnList(onlineTag), f.mirrorEntry(onlineTag, foreignRepo)))
	res, err := List(context.Background(), f.env)
	if err != nil || len(res.Modules) != 1 {
		t.Fatalf("%+v %v", res, err)
	}
	if m := res.Modules[0]; !m.LatestWithdrawn || m.Update == nil || m.Update.Latest != onlineTag || m.Withdrawn != nil {
		t.Errorf("%+v", m)
	}
	if _, m := catalogueOf(t, f); strings.Contains(strings.ToLower(f.out.String()), "latest_withdrawn") || m["withdrawn"] != nil {
		t.Errorf("the JSON says it: %v", m)
	}
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawnList(testTag), f.mirrorEntry(onlineTag, foreignRepo)))
	if res, _ := List(context.Background(), f.env); res.Modules[0].LatestWithdrawn {
		t.Errorf("the installed tag is the withdrawn one: %+v", res.Modules[0])
	}
}
