package modulecmd

import (
	"context"
	"os"
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

// the online entry is the only one: a shipped one that is not withdrawn would be kept instead
func TestAnInstallOfAVersionTheCatalogueWithdrewIsRefused(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(f.path(pathCatalogue)); err != nil {
		t.Fatal(err)
	}
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
	if err := os.Remove(f.path(pathCatalogue)); err != nil {
		t.Fatal(err)
	}
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

// an installed module whose listed version is withdrawn is offered no update, whichever file the entry comes from
func TestListOffersNoUpdateToAWithdrawnVersion(t *testing.T) {
	f := installed(t) // testTag, the shipped catalogue's
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawnList(onlineTag), f.mirrorEntry(onlineTag, foreignRepo)))
	if _, m := catalogueOf(t, f); m["source"] != "shipped" || m["tag"] != testTag || m["update"].(map[string]any)["available"] != false {
		t.Errorf("a withdrawn online version and the shipped one: %v", m)
	}
	// the flagged version is the only entry
	if err := os.Remove(f.path(pathCatalogue)); err != nil {
		t.Fatal(err)
	}
	_, m := catalogueOf(t, f)
	if m["source"] != "online" || m["tag"] != onlineTag {
		t.Fatalf("the only entry: %v", m)
	}
	if u, ok := m["update"].(map[string]any); ok && u["available"] != false {
		t.Errorf("the only entry, withdrawn: %v", m)
	}
}

// an install of a module whose only entry is withdrawn is refused
func TestAnInstallOfAModuleWhoseOnlyEntryIsWithdrawnIsRefused(t *testing.T) {
	f := newFixture(t)
	if err := os.Remove(f.path(pathCatalogue)); err != nil {
		t.Fatal(err)
	}
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawnList(onlineTag), f.mirrorEntry(onlineTag, foreignRepo)))
	if p := PlanOf(context.Background(), f.env, testName); p.Refused != osmodule.RefusedWithdrawn || p.Tag != onlineTag {
		t.Fatalf("plan: %+v", p)
	}
}
