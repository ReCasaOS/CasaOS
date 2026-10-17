package osmodule

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// virtManifest is the manifest that the virtual machines module proposed, with the schema and the section for an upgrade of the operating system.
const virtManifest = `{
  "schema": 1,
  "name": "virt",
  "title": "Virtual machines",
  "component": "CasaOS-VirtManagement",
  "unit": "casaos-virt-management.service",
  "gateway_route": "/v2/virt_management",
  "packages": {
    "common": ["qemu-utils", "libvirt-daemon-system", "libvirt-clients"],
    "amd64": ["qemu-system-x86", "ovmf"],
    "arm64": ["qemu-system-arm", "qemu-efi-aarch64"]
  },
  "min_recasaos": "0.5.23",
  "os": ["debian:12", "debian:13", "ubuntu:24.04", "ubuntu:26.04"],
  "os_upgrade": {
    "stop_units": ["casaos-virt-management.service"],
    "stop_timeout_seconds": 300,
    "maintenance": {
      "prepare": "/v2/virt_management/maintenance/prepare",
      "status": "/v2/virt_management/maintenance/status",
      "resume": "/v2/virt_management/maintenance/resume"
    }
  }
}`

// with changes one place of the manifest, by decoding it, setting a path and encoding it again.
func with(t *testing.T, path string, value any) string {
	t.Helper()
	var doc map[string]any
	if err := json.Unmarshal([]byte(virtManifest), &doc); err != nil {
		t.Fatal(err)
	}
	at := doc
	keys := strings.Split(path, ".")
	for _, k := range keys[:len(keys)-1] {
		at = at[k].(map[string]any)
	}
	if value == nil {
		delete(at, keys[len(keys)-1])
	} else {
		at[keys[len(keys)-1]] = value
	}
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestTheManifestOfTheVirtualMachinesModuleIsTaken(t *testing.T) {
	m, err := Parse("virt.json", []byte(virtManifest))
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "virt" || m.Unit != "casaos-virt-management.service" || m.OSUpgrade == nil || m.OSUpgrade.StopTimeoutSeconds != 300 ||
		m.OSUpgrade.Maintenance.Resume != "/v2/virt_management/maintenance/resume" || len(m.Packages.Common) != 3 || m.Packages.ARM64[1] != "qemu-efi-aarch64" {
		t.Errorf("%+v", m)
	}
	// a module that says nothing of an upgrade is a module, and is told to be one that does not
	m, err = Parse("virt.json", []byte(with(t, "os_upgrade", nil)))
	if err != nil || m.OSUpgrade != nil {
		t.Errorf("without a section for the upgrade: %+v, %v", m, err)
	}
}

func TestAManifestThatIsNotExactlyWhatIsAskedForIsNotTaken(t *testing.T) {
	long := strings.Repeat("a", 65)
	for _, c := range []struct {
		name, file, text string
		field, reason    string
	}{
		{"a file that is not named after a module", "Virt.json", virtManifest, "file", ReasonFile},
		{"a file with no suffix", "virt", virtManifest, "file", ReasonFile},
		{"a file with a path", "../virt.json", virtManifest, "file", ReasonFile},
		{"a name that is not the file's", "other.json", virtManifest, "name", ReasonValue},
		{"not json", "virt.json", "name: virt", "file", ReasonUnreadable},
		{"nothing", "virt.json", "", "file", ReasonUnreadable},
		{"an array", "virt.json", "[]", "file", ReasonUnreadable},
		{"words after the object", "virt.json", virtManifest + " {}", "file", ReasonUnreadable},
		{"a field that is not known", "virt.json", with(t, "run", "rm -rf /"), "file", ReasonUnreadable},
		{"a key twice", "virt.json", strings.Replace(virtManifest, `"name": "virt",`, `"name": "virt", "name": "virt",`, 1), "file", ReasonUnreadable},
		{"a key twice inside", "virt.json", strings.Replace(virtManifest, `"stop_timeout_seconds": 300,`, `"stop_timeout_seconds": 300, "stop_timeout_seconds": 5,`, 1), "file", ReasonUnreadable},
		{"another schema", "virt.json", with(t, "schema", 2), "schema", ReasonSchema},
		{"no schema", "virt.json", with(t, "schema", nil), "schema", ReasonSchema},
		{"a title with a newline", "virt.json", with(t, "title", "a\nb"), "title", ReasonValue},
		{"an empty title", "virt.json", with(t, "title", ""), "title", ReasonValue},
		{"a title that is too long", "virt.json", with(t, "title", long), "title", ReasonValue},
		{"a unit that is a command", "virt.json", with(t, "unit", "x.service; reboot"), "unit", ReasonValue},
		{"a unit that is not a service", "virt.json", with(t, "unit", "x.timer"), "unit", ReasonValue},
		{"a component with a slash", "virt.json", with(t, "component", "a/b"), "component", ReasonValue},
		{"a route without a version", "virt.json", with(t, "gateway_route", "/virt"), "gateway_route", ReasonValue},
		{"a route with a query", "virt.json", with(t, "gateway_route", "/v2/virt?x=1"), "gateway_route", ReasonValue},
		{"no unit", "virt.json", with(t, "unit", ""), "unit", ReasonMissing},
		{"a version that is not three numbers", "virt.json", with(t, "min_recasaos", "0.5"), "min_recasaos", ReasonValue},
		{"a package with an option", "virt.json", with(t, "packages.common", []string{"--force"}), "packages.common", ReasonValue},
		{"a package twice", "virt.json", with(t, "packages.amd64", []string{"ovmf", "ovmf"}), "packages.amd64", ReasonValue},
		{"too many packages", "virt.json", with(t, "packages.arm64", manyOf("pkg", 33)), "packages.arm64", ReasonTooMany},
		{"no operating system", "virt.json", with(t, "os", []string{}), "os", ReasonMissing},
		{"an operating system that is not a name and a version", "virt.json", with(t, "os", []string{"debian"}), "os", ReasonValue},
		{"an upgrade with no unit to stop", "virt.json", with(t, "os_upgrade.stop_units", []string{}), "os_upgrade.stop_units", ReasonMissing},
		{"a unit to stop that is a path", "virt.json", with(t, "os_upgrade.stop_units", []string{"/etc/x.service"}), "os_upgrade.stop_units", ReasonValue},
		{"no time to wait", "virt.json", with(t, "os_upgrade.stop_timeout_seconds", 0), "os_upgrade.stop_timeout_seconds", ReasonValue},
		{"a wait of more than an hour", "virt.json", with(t, "os_upgrade.stop_timeout_seconds", 3601), "os_upgrade.stop_timeout_seconds", ReasonValue},
		{"a call that is missing", "virt.json", with(t, "os_upgrade.maintenance.status", ""), "os_upgrade.maintenance.status", ReasonMissing},
		{"a call outside the route of the module", "virt.json", with(t, "os_upgrade.maintenance.prepare", "/v2/other/maintenance/prepare"), "os_upgrade.maintenance.prepare", ReasonValue},
		{"a call that goes up", "virt.json", with(t, "os_upgrade.maintenance.resume", "/v2/virt_management/../x"), "os_upgrade.maintenance.resume", ReasonValue},
		{"a call with a query", "virt.json", with(t, "os_upgrade.maintenance.resume", "/v2/virt_management/x?y=1"), "os_upgrade.maintenance.resume", ReasonValue},
		{"a call with an empty segment", "virt.json", with(t, "os_upgrade.maintenance.status", "/v2/virt_management//status"), "os_upgrade.maintenance.status", ReasonValue},
		{"a call that is the route itself", "virt.json", with(t, "os_upgrade.maintenance.status", "/v2/virt_management"), "os_upgrade.maintenance.status", ReasonValue},
		{"a call that goes on the route of another module", "virt.json", with(t, "os_upgrade.maintenance.status", "/v2/virt_managementx/status"), "os_upgrade.maintenance.status", ReasonValue},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, err := Parse(c.file, []byte(c.text))
			var bad *Invalid
			if !errors.As(err, &bad) || bad.Field != c.field || bad.Reason != c.reason {
				t.Errorf("error = %v, want %s %s", err, c.field, c.reason)
			}
		})
	}
}

func manyOf(prefix string, n int) []string {
	var out []string
	for i := 0; i < n; i++ {
		out = append(out, prefix+"-"+strings.Repeat("a", i%5+1)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	return out
}

// The bounds are where they are said to be: a file of exactly the most is read, one byte more is not.
func TestTheManifestIsReadUpToItsBoundAndNotOneByteMore(t *testing.T) {
	pad := func(n int) []byte {
		text := []byte(virtManifest)
		return append(text, []byte(strings.Repeat(" ", n-len(text)))...)
	}
	if _, err := Parse("virt.json", pad(MaxBytes)); err != nil {
		t.Errorf("a file of %d bytes: %v", MaxBytes, err)
	}
	var bad *Invalid
	if _, err := Parse("virt.json", pad(MaxBytes+1)); !errors.As(err, &bad) || bad.Reason != ReasonTooLarge {
		t.Errorf("a file of %d bytes: %v", MaxBytes+1, err)
	}
	if got := FileName("virt"); got != "virt.json" {
		t.Errorf("FileName = %q", got)
	}
	// the longest name and the longest list that are taken
	if _, err := Parse(strings.Repeat("a", 32)+".json", []byte(strings.Replace(virtManifest, `"name": "virt"`, `"name": "`+strings.Repeat("a", 32)+`"`, 1))); err != nil {
		t.Errorf("a name of 32 letters: %v", err)
	}
	if _, err := Parse(strings.Repeat("a", 33)+".json", []byte(strings.Replace(virtManifest, `"name": "virt"`, `"name": "`+strings.Repeat("a", 33)+`"`, 1))); err == nil {
		t.Error("a name of 33 letters")
	}
	if _, err := Parse("virt.json", []byte(with(t, "packages.arm64", manyOf("pkg", 32)))); err != nil {
		t.Errorf("32 packages: %v", err)
	}
	if _, err := Parse("virt.json", []byte(with(t, "os_upgrade.stop_timeout_seconds", 3600))); err != nil {
		t.Errorf("a wait of an hour: %v", err)
	}
}
