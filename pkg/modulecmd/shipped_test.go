package modulecmd

import (
	"context"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// --shipped is the installer's: the modules of the shipped catalogue alone, as if the online one were off, so the installer never installs a tag the night has not waited for.

func TestListShippedReadsTheShippedCatalogueAlone(t *testing.T) {
	f := installed(t)
	f.writeOnline(f.onlineEntry(onlineTag))
	v := f.json("list", "--shipped")
	cat, m := v["catalogue"].(map[string]any), v["modules"].([]any)[0].(map[string]any)
	// as the setting off says it
	if on := cat["online"].(map[string]any); cat["ok"] != true || on["ok"] != false || on["error"] != "off" || m["source"] != "shipped" || m["tag"] != testTag {
		t.Errorf("catalogue %v, module %v", cat, m)
	}
	if u := m["update"].(map[string]any); u["available"] != false || u["latest"] != testTag {
		t.Errorf("update: %v", u)
	}
	// and without the flag nothing changes
	if _, m := catalogueOf(t, f); m["source"] != "online" || m["tag"] != onlineTag {
		t.Errorf("without --shipped: %v", m)
	}
}

// laterOnline puts online a later tag of the shipped entry, whose archive's digest is not the one the downloader serves (an update that took it would fail), and checks that,
// without --shipped, the update's plan takes it.
func laterOnline(t *testing.T, f *fixture) {
	t.Helper()
	e := f.onlineEntry("v0.1.5")
	e.Assets.AMD64.URL = strings.ReplaceAll(f.entry.Assets.AMD64.URL, f.entry.Tag, "v0.1.5")
	f.writeOnline(e)
	if p := PlanUpdateOf(context.Background(), f.env, testName); p.Tag != "v0.1.5" {
		t.Fatalf("without --shipped the plan is not of the online tag: %+v", p)
	}
}

func TestUpdateShippedInstallsTheShippedTag(t *testing.T) {
	f := updatable(t)
	laterOnline(t, f)
	if code := f.update("--yes", "--shipped"); code != ExitOK {
		t.Fatalf("exit %d:\n%s%s", code, f.out, f.errOut)
	}
	if got := strings.TrimSpace(f.read("/var/lib/casaos/modules/virt.version")); got != newTag {
		t.Errorf("installed %q, want the shipped %s", got, newTag)
	}
	for p, content := range wantAfter {
		if got := f.read(p); got != content {
			t.Errorf("%s holds %q", p, got)
		}
	}
}

func TestUpdateShippedNeverMovesAModuleBack(t *testing.T) {
	for _, c := range []struct {
		name    string
		shipped string
		code    string
	}{
		{"the shipped tag is the installed one", testTag, osmodule.RefusedUpToDate},
		{"the shipped tag is older than the installed one", "v0.0.9", osmodule.RefusedDowngrade},
	} {
		t.Run(c.name, func(t *testing.T) {
			f := updatable(t)
			f.publish(c.shipped, testManifest(), v2Entries(t, testManifest()))
			laterOnline(t, f)
			before := f.treeWithoutBookkeeping()
			if code := f.update("--yes", "--shipped"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+c.code+"\n") {
				t.Fatalf("update: %d\n%s", code, f.out)
			}
			sameTree(t, before, f.treeWithoutBookkeeping())
		})
	}
}

// flaggedOnline is updatable with the shipped tag withdrawn by the online catalogue.
func flaggedOnline(t *testing.T) *fixture {
	t.Helper()
	f := updatable(t)
	f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawnList(newTag), f.mirrorEntry(onlineTag, foreignRepo)))
	return f
}

// --shipped takes the modules of the shipped file alone, and still heeds the versions the online catalogue withdrew.
func TestUpdateShippedRefusesAShippedTagWithdrawnOnline(t *testing.T) {
	f := flaggedOnline(t)
	before := f.treeWithoutBookkeeping()
	if code := f.update("--yes", "--shipped"); code != ExitRefused || !strings.Contains(f.out.String(), "refused: "+osmodule.RefusedWithdrawn+"\n") {
		t.Fatalf("update: exit %d\n%s%s", code, f.out, f.errOut)
	}
	sameTree(t, before, f.treeWithoutBookkeeping())
}

func TestListShippedOffersNoUpdateToAShippedTagWithdrawnOnline(t *testing.T) {
	f := flaggedOnline(t)
	v := f.json("list", "--shipped")
	cat, m := v["catalogue"].(map[string]any), v["modules"].([]any)[0].(map[string]any)
	if on := cat["online"].(map[string]any); on["ok"] != false || on["error"] != "off" || m["source"] != "shipped" || m["tag"] != newTag {
		t.Errorf("catalogue %v, module %v", cat, m)
	}
	if u := m["update"].(map[string]any); u["available"] != false || u["latest"] != newTag {
		t.Errorf("update: %v", u)
	}
}

// an online file that does not pass changes nothing to --shipped, and with the setting off it is not read at all
func TestUpdateShippedWithoutAGoodOnlineFileOrWithTheSettingOff(t *testing.T) {
	for name, set := range map[string]func(f *fixture){
		"an online file that does not pass": func(f *fixture) {
			f.writeFile(pathOnlineCatalogue, strings.Replace(online3Raw(t, 7, withdrawnList(newTag), f.mirrorEntry(onlineTag, foreignRepo)), `"schema":3`, `"schema":"3"`, 1))
		},
		"setting off": func(f *fixture) {
			f.writeFile(pathOnlineCatalogue, online3Raw(t, 7, withdrawnList(newTag), f.mirrorEntry(onlineTag, foreignRepo)))
			f.env.OnlineCatalogueOff = true
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := updatable(t)
			set(f)
			if code := f.update("--yes", "--shipped"); code != ExitOK {
				t.Fatalf("exit %d:\n%s%s", code, f.out, f.errOut)
			}
			if got := strings.TrimSpace(f.read("/var/lib/casaos/modules/virt.version")); got != newTag {
				t.Errorf("installed %q, want the shipped %s", got, newTag)
			}
		})
	}
}
