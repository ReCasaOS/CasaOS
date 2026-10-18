//go:build linux

package modulecmd

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The assistant on a real Debian box, through Main, with the real file system, real apt and the real files: this runs only inside a copy of a Debian chroot that is to be spared (the test does
// not write anything on the box, but a plan that went wrong would):
//
//	CASAOS_MODULECMD_REAL_BOX="debian:12 fakedep" <this test binary> -test.run TestARealDebianBoxIsPlannedThroughMain
//
// The first word is the system the box is (the one the module accepts), the others the packages the module asks for. It is run as root, and as a user that is not.
func TestARealDebianBoxIsPlannedThroughMain(t *testing.T) {
	spec := strings.Fields(os.Getenv("CASAOS_MODULECMD_REAL_BOX"))
	if len(spec) < 2 {
		t.Skip("needs a Debian chroot to spare (CASAOS_MODULECMD_REAL_BOX=\"debian:12 <package>...\")")
	}
	m := testManifest()
	m.OS = []string{spec[0]}
	m.Packages = osmodule.Packages{Common: spec[1:]}
	archive := strings.Repeat("ab", 32)
	entry := osmodule.Entry{
		Name: testName, Component: testComponent, Tier: osmodule.TierCommunity, Tag: testTag, Commit: strings.Repeat("a", 40),
		Assets: osmodule.Assets{AMD64: &osmodule.Asset{
			URL:    "https://github.com/ReCasaOS/" + testComponent + "/releases/download/" + testTag + "/linux-amd64-casaos-virt-management-" + testTag + ".tar.gz",
			SHA256: archive,
		}},
		Manifest: m,
	}
	raw, err := json.Marshal(osmodule.Catalogue{Schema: 1, Modules: []osmodule.Entry{entry}})
	if err != nil {
		t.Fatal(err)
	}
	catalogue := filepath.Join(t.TempDir(), "catalogue.json")
	if err := os.WriteFile(catalogue, raw, 0o644); err != nil {
		t.Fatal(err)
	}

	// what the box keeps, which a plan must leave as it is
	watched := []string{"/var/lib/dpkg/status", "/var/lib/apt/extended_states", "/var/log/apt/history.log", "/var/log/dpkg.log", "/var/log/apt/eipp.log.xz"}
	digest := func() map[string]string {
		out := map[string]string{}
		for _, p := range watched {
			b, err := os.ReadFile(p)
			if err != nil {
				out[p] = "absent"
				continue
			}
			s := sha256.Sum256(b)
			out[p] = string(s[:])
		}
		return out
	}
	before := digest()

	var stdout, stderr bytes.Buffer
	env := Env{
		Roots: Roots{}, Stdout: &stdout, Stderr: &stderr,
		Getenv: func(k string) string {
			switch k {
			case "CASAOS_MODULE_TEST":
				return "1"
			case "CASAOS_MODULE_CATALOGUE":
				return catalogue
			}
			return ""
		},
		EUID: os.Geteuid, Arch: "amd64", Version: "0.5.29",
		Runner: newRunner(Roots{}), Sandbox: newSandbox(Roots{}), Host: newHost(), Gateway: newGateway(Roots{}), Clock: realClock{},
	}
	if code := Main(context.Background(), []string{"plan", testName}, env); code != ExitOK {
		t.Fatalf("exit %d\n%s%s", code, &stdout, &stderr)
	}
	var plan struct {
		Refused *string `json:"refused"`
		PlanID  string  `json:"plan_id"`
		Checks  []struct {
			Code   string `json:"code"`
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		} `json:"checks"`
		Packages struct {
			ToInstall []string `json:"to_install"`
			Known     bool     `json:"known"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &plan); err != nil {
		t.Fatalf("%v\n%s", err, &stdout)
	}
	t.Logf("%s", &stdout)
	for _, c := range plan.Checks {
		switch {
		case c.Code == osmodule.CheckBusy && os.Geteuid() != 0:
			if c.OK || c.Detail == "" || !strings.HasPrefix(c.Detail, osmodule.DetailNotChecked) {
				t.Errorf("the check of the package manager, as a user that is not root: %+v", c)
			}
		case !c.OK:
			t.Errorf("the check %s fails: %q", c.Code, c.Detail)
		}
	}
	if plan.Refused != nil {
		t.Errorf("the plan is refused: %s", *plan.Refused)
	}
	if plan.PlanID == "" || !plan.Packages.Known {
		t.Errorf("no plan id or no sizes: %+v", plan)
	}
	if after := digest(); len(after) != len(before) {
		t.Fatal("the files that are watched")
	} else {
		for p, d := range before {
			if after[p] != d {
				t.Errorf("%s was changed by the plan", p)
			}
		}
	}
	if os.Geteuid() != 0 && bytes.Contains(stderr.Bytes(), []byte("permission")) {
		t.Errorf("complaints on the error output: %s", &stderr)
	}
}
