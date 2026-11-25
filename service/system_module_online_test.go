package service

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"sync/atomic"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/config"
	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

const onlineModuleTag = "v0.2.0"

// countingGet is a Downloader that counts the requests it is asked for and answers each with body.
type countingGet struct {
	body  []byte
	calls atomic.Int32
}

func (g *countingGet) Get(_ context.Context, _ string, dst io.Writer) (int64, error) {
	g.calls.Add(1)
	n, err := dst.Write(g.body)
	return int64(n), err
}

func withOnlineCatalogueSetting(t *testing.T, on bool) {
	t.Helper()
	previous := config.ModulesInfo.OnlineCatalogue
	config.ModulesInfo.OnlineCatalogue = on
	t.Cleanup(func() { config.ModulesInfo.OnlineCatalogue = previous })
}

// onlineCatalogue is an online catalogue of virt at a newer tag than the shipped one.
func (b *moduleBox) onlineCatalogue(serial int64) []byte {
	b.t.Helper()
	raw, err := json.Marshal(osmodule.Catalogue{Schema: 2, Serial: serial, Generated: "2026-11-25T10:00:00Z", Modules: []osmodule.Entry{b.entry("virt", onlineModuleTag)}})
	if err != nil {
		b.t.Fatal(err)
	}
	return raw
}

func TestRefreshIsOffWhenTheSettingIsOff(t *testing.T) {
	b := newModuleBox(t)
	online := b.env.Roots.OnlineCatalogue()
	get := &countingGet{body: b.onlineCatalogue(1)}

	withOnlineCatalogueSetting(t, false)
	b.system.refreshModuleCatalogue(context.Background(), get, "https://github.com/ReCasaOS/modules/releases/latest/download/catalogue-2.json")
	if n := get.calls.Load(); n != 0 {
		t.Errorf("%d requests with the setting off", n)
	}
	if _, err := os.Lstat(online); !os.IsNotExist(err) {
		t.Errorf("a file was written: %v", err)
	}

	// on, the same call fetches and keeps the file
	withOnlineCatalogueSetting(t, true)
	b.system.refreshModuleCatalogue(context.Background(), get, "https://github.com/ReCasaOS/modules/releases/latest/download/catalogue-2.json")
	if n := get.calls.Load(); n != 1 {
		t.Errorf("%d requests with the setting on", n)
	}
	if _, err := os.Lstat(online); err != nil {
		t.Errorf("no file: %v", err)
	}

	// an image-managed host has no online catalogue
	previous := config.ServerInfo.ImageManaged
	config.ServerInfo.ImageManaged = true
	t.Cleanup(func() { config.ServerInfo.ImageManaged = previous })
	b.system.refreshModuleCatalogue(context.Background(), get, "https://github.com/ReCasaOS/modules/releases/latest/download/catalogue-2.json")
	if n := get.calls.Load(); n != 1 {
		t.Errorf("%d requests on an image-managed host", n)
	}
}

func TestListIgnoresTheOnlineFileWhenTheSettingIsOff(t *testing.T) {
	b := newModuleBox(t)
	b.write("/var/lib/casaos/modules/catalogue.json", string(b.onlineCatalogue(3)))

	withOnlineCatalogueSetting(t, true)
	list, err := b.system.ListSystemModules()
	if err != nil || len(list.Modules) != 1 || list.Modules[0].Tag != onlineModuleTag || list.Catalogue.Online == nil || !list.Catalogue.Online.OK {
		t.Fatalf("the setting on: %+v, %v", list, err)
	}

	withOnlineCatalogueSetting(t, false)
	list, err = b.system.ListSystemModules()
	if err != nil || len(list.Modules) != 1 || list.Modules[0].Tag != boxModuleTag || list.Catalogue.Online == nil || list.Catalogue.Online.OK || list.Catalogue.Online.Error != "off" {
		t.Errorf("the setting off: %+v, %v", list, err)
	}
	if plan, _ := b.system.PlanSystemModule("virt"); plan.Tag != boxModuleTag {
		t.Errorf("the plan reads the online file: %+v", plan)
	}
}
