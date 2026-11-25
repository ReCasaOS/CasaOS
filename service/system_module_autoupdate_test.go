package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/modulecmd"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// foreignCatalogue puts an online catalogue of schema 3 on the box with the module at onlineModuleTag, written in
// the repository repo and mirrored by ReCasaOS.
func (b *moduleBox) foreignCatalogue(name, repo string) {
	b.t.Helper()
	e := b.entry(name, onlineModuleTag)
	a := *e.Assets.AMD64
	a.URL = "https://github.com/ReCasaOS/modules/releases/download/m-" + name + "-" + onlineModuleTag + "/linux-amd64-casaos-" + name + "-" + onlineModuleTag + ".tar.gz"
	e.Assets, e.Repository = osmodule.Assets{AMD64: &a}, repo
	raw, err := json.Marshal(osmodule.Catalogue{Schema: 3, Serial: 7, Generated: "2026-11-25T10:00:00Z", Modules: []osmodule.Entry{e}})
	if err != nil {
		b.t.Fatal(err)
	}
	b.write("/var/lib/casaos/modules/catalogue.json", strings.TrimSuffix(string(raw), "}")+`,"withdrawn":[]}`)
}

func moduleRefusalCode(err error) string {
	var r *ModuleRefusal
	if errors.As(err, &r) {
		return r.Code
	}
	return ""
}

func TestTheOwnerAllowsTheNightToUpdateAForeignModule(t *testing.T) {
	withOnlineCatalogueSetting(t, true)
	b := newModuleBox(t)
	b.install("virt")
	b.write("/var/lib/casaos/modules/virt.version", boxModuleTag+"\n")
	b.foreignCatalogue("virt", "someone/CasaOS-Virt")

	if err := b.system.SetSystemModuleAutoUpdate("virt", true); err != nil {
		t.Fatal(err)
	}
	// the yes is stored with the repository of the entry the list shows
	if got := modulecmd.AutoAllowed(b.env.Roots); !reflect.DeepEqual(got, map[string]string{"virt": "someone/CasaOS-Virt"}) {
		t.Fatalf("allowed %v", got)
	}
	list, err := b.system.ListSystemModules()
	if err != nil || len(list.Modules) != 1 || list.Modules[0].AutoUpdate == nil || !*list.Modules[0].AutoUpdate {
		t.Fatalf("the list: %+v %v", list.Modules, err)
	}
	night := &NightModules{Provider: b.system}
	if updates, err := night.List(context.Background()); err != nil || len(updates) != 1 || !updates[0].Foreign || !updates[0].Allowed {
		t.Fatalf("the night: %+v %v", updates, err)
	}
	// the catalogue now lists a module of that name from another repository: the yes was not given for it
	b.foreignCatalogue("virt", "bob/CasaOS-Virt")
	list, err = b.system.ListSystemModules()
	if err != nil || len(list.Modules) != 1 || list.Modules[0].AutoUpdate == nil || *list.Modules[0].AutoUpdate {
		t.Fatalf("the list from another repository: %+v %v", list.Modules, err)
	}
	if updates, err := night.List(context.Background()); err != nil || len(updates) != 1 || !updates[0].Foreign || updates[0].Allowed {
		t.Fatalf("the night from another repository: %+v %v", updates, err)
	}
	b.foreignCatalogue("virt", "someone/CasaOS-Virt")
	if err := b.system.SetSystemModuleAutoUpdate("virt", false); err != nil {
		t.Fatal(err)
	}
	if got := modulecmd.AutoAllowed(b.env.Roots); len(got) != 0 {
		t.Fatalf("allowed %v after no", got)
	}
}

func TestTheAutoUpdateOfAModuleIsRefusedWhenItIsNotAnInstalledForeignOne(t *testing.T) {
	withOnlineCatalogueSetting(t, true)
	for _, c := range []struct {
		name  string
		setup func(b *moduleBox)
		code  string
	}{
		{"a module of ReCasaOS", func(b *moduleBox) { b.install("virt") }, ModuleNotForeign},
		{"a module of ReCasaOS in a catalogue of schema 3", func(b *moduleBox) {
			b.install("virt")
			b.foreignCatalogue("virt", "ReCasaOS/CasaOS-Virt")
		}, ModuleNotForeign},
		{"a foreign module that is not installed", func(b *moduleBox) { b.foreignCatalogue("virt", "someone/CasaOS-Virt") }, ModuleNotInstalled},
		{"a module the box does not know", func(b *moduleBox) { b.setCatalogue("mcp") }, ModuleNotInstalled},
		{"no catalogue", func(b *moduleBox) {
			b.install("virt")
			b.write("/usr/share/casaos/modules/catalogue.json", "not json")
		}, osmodule.CheckCatalogue},
	} {
		t.Run(c.name, func(t *testing.T) {
			b := newModuleBox(t)
			c.setup(b)
			for _, on := range []bool{true, false} {
				want := c.code
				if !on && want == ModuleNotInstalled {
					want = "" // a no is taken for a module that is not installed: it drops a yes left behind
				}
				if got := moduleRefusalCode(b.system.SetSystemModuleAutoUpdate("virt", on)); got != want {
					t.Errorf("on = %v: refused with %q, want %q", on, got, want)
				}
			}
			if b.exists("/var/lib/casaos/modules/auto-update.json") {
				t.Error("the file was written")
			}
		})
	}
	b := newModuleBox(t)
	if got := moduleRefusalCode(b.system.SetSystemModuleAutoUpdate("Virt", true)); got != ModuleBadRequest {
		t.Errorf("a bad name: %q", got)
	}
}

// A yes left in the file for a module that is no longer installed, or no longer in the catalogue, is taken out by a no.
func TestANoDropsAYesLeftForAModuleNotInstalledOrNotListed(t *testing.T) {
	withOnlineCatalogueSetting(t, true)
	for name, setup := range map[string]func(b *moduleBox){
		"not installed":          func(b *moduleBox) { b.foreignCatalogue("virt", "someone/CasaOS-Virt") },
		"not in the catalogue":   func(b *moduleBox) { b.install("virt"); b.setCatalogue("mcp") },
		"neither, nor any entry": func(b *moduleBox) { b.setCatalogue("mcp") },
	} {
		t.Run(name, func(t *testing.T) {
			b := newModuleBox(t)
			setup(b)
			b.write("/var/lib/casaos/modules/auto-update.json", `{"modules":{"mcp":"alice/mcp","virt":"someone/CasaOS-Virt"}}`+"\n")
			if err := b.system.SetSystemModuleAutoUpdate("virt", false); err != nil {
				t.Fatal(err)
			}
			if got := modulecmd.AutoAllowed(b.env.Roots); !reflect.DeepEqual(got, map[string]string{"mcp": "alice/mcp"}) {
				t.Fatalf("allowed %v", got)
			}
		})
	}
}
