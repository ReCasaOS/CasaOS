package osmodule

import (
	"encoding/json"
	"errors"
	"os"
	"regexp"
	"strings"
	"testing"
)

// invalidAt says what a manifest is refused for: the field and the reason, or empty strings when it is taken.
func invalidAt(t *testing.T, text string) (field, reason string) {
	t.Helper()
	_, err := Parse("virt.json", []byte(text))
	var bad *Invalid
	if err == nil {
		return "", ""
	}
	if !errors.As(err, &bad) {
		t.Fatalf("an error that is not an Invalid: %v", err)
	}
	return bad.Field, bad.Reason
}

// A title is something a person can read: a title made of characters that print as nothing (a Braille blank, the fillers of Hangul) is a title with nothing in it, although each
// of those characters is "printable" to the letter.
func TestATitleThatShowsNothingIsNotATitle(t *testing.T) {
	for name, literal := range map[string]string{
		"a Braille blank":             `"⠀"`,
		"three Braille blanks":        `"⠀⠀⠀"`,
		"the Hangul filler":           `"ㅤ"`,
		"the Hangul choseong filler":  `"ᅟ"`,
		"the Hangul jungseong filler": `"ᅠ"`,
		"the halfwidth Hangul filler": `"ﾠ"`,
		"all of them and a space":     `" ⠀ ㅤᅟᅠﾠ "`,
		"a mark with nothing to mark": `"́"`,
		"a mark and a blank":          `"́⠀"`,
		"spaces":                      `"   "`,
		"nothing":                     `""`,
	} {
		if field, reason := invalidAt(t, withRaw(t, "title", literal)); field != "title" || reason != ReasonValue {
			t.Errorf("%s: refused for %q %q", name, field, reason)
		}
	}
	// one thing that shows is enough, whatever it is
	for name, literal := range map[string]string{
		"a letter":                            `"a"`,
		"a letter of another script":          `"日"`,
		"a digit":                             `"7"`,
		"a number that is no digit":           `"②"`,
		"a punctuation mark":                  `"!"`,
		"a symbol":                            `"+"`,
		"a currency sign":                     `"€"`,
		"a letter among blanks":               `"⠀aㅤ"`,
		"a Braille pattern that is not blank": `"⠁"`,
		"a letter with its mark":              `"é"`,
	} {
		if field, reason := invalidAt(t, withRaw(t, "title", literal)); field != "" {
			t.Errorf("%s: refused for %q %q", name, field, reason)
		}
	}
}

// The route of a module is a name under a version, and the name is not one the core, or a service of the distribution, answers on: the gateway sends a request to the longest route it
// knows, and a module that is installed on one of those would be sent the requests of the dashboard.
func TestAModuleCannotTakeTheRouteOfTheCore(t *testing.T) {
	reserved := []string{
		"sys", "users", "usb", "disks", "storage", "cloud", "recover", "other", "zt", "samba", "file", "folder", "image", "notify", "gateway", "casaos", "app_management",
		"message_bus", "local_storage", "port", "driver", "batch", "test", "health", "apps", "container",
	}
	for _, name := range reserved {
		for _, version := range []string{"v1", "v2", "v3", "v10"} {
			route := "/" + version + "/" + name
			if field, reason := invalidAt(t, withRoute(t, route)); field != "gateway_route" || reason != ReasonValue {
				t.Errorf("%s: refused for %q %q", route, field, reason)
			}
		}
	}
	// ...and a name that only starts like one, or contains one, is a name
	for _, name := range []string{"virt_management", "sysx", "xsys", "sys_", "file2", "user", "app", "message", "local", "gateway2", "s", "zt1"} {
		if field, reason := invalidAt(t, withRoute(t, "/v2/"+name)); field != "" {
			t.Errorf("/v2/%s: refused for %q %q", name, field, reason)
		}
	}
	// the module that is the reason for the rule is the proof that it is not too wide
	if field, reason := invalidAt(t, virtManifest); field != "" {
		t.Errorf("the route of the virtual machines module: %q %q", field, reason)
	}
}

// Every group of routes that the core registers is reserved: the list is read off the router, so that a route added to the core without a line in the list is found here.
func TestEveryGroupOfRoutesThatTheCoreRegistersIsReserved(t *testing.T) {
	source, err := os.ReadFile("../../route/v1.go")
	if err != nil {
		t.Skipf("the router is not here: %v", err)
	}
	groups := regexp.MustCompile(`v1Group\.Group\("/([a-z_]+)"\)`).FindAllStringSubmatch(string(source), -1)
	if len(groups) < 10 {
		t.Fatalf("%d groups found in the router: the pattern has gone stale", len(groups))
	}
	for _, g := range groups {
		if field, _ := invalidAt(t, withRoute(t, "/v1/"+g[1])); field != "gateway_route" {
			t.Errorf("/v1/%s is a group of the core and a module can take it", g[1])
		}
	}
}

// The units that the upgrade of the system stops are the module's own and no others: the list is a way for the module to say what to stop, not for it to stop the system's services.
func TestAnUpgradeStopsTheUnitOfTheModuleAndNoOther(t *testing.T) {
	own := "casaos-virt-management.service"
	others := func(n int) []string {
		var out []string
		for i := 0; i < n; i++ {
			out = append(out, "other"+string(rune('a'+i))+".service")
		}
		return out
	}
	for _, c := range []struct {
		name          string
		units         []string
		field, reason string
	}{
		{"its own unit", []string{own}, "", ""},
		{"a unit of the system", []string{"ssh.service"}, "os_upgrade.stop_units", ReasonValue},
		{"its own unit and one of the system's", []string{own, "docker.service"}, "os_upgrade.stop_units", ReasonValue},
		{"a unit of the system and then its own", []string{"docker.service", own}, "os_upgrade.stop_units", ReasonValue},
		{"a unit that is its own with another case", []string{"Casaos-virt-management.service"}, "os_upgrade.stop_units", ReasonValue},
		{"a unit that has its own name inside", []string{"x" + own}, "os_upgrade.stop_units", ReasonValue},
		{"eight units, the first of them its own", append([]string{own}, others(7)...), "os_upgrade.stop_units", ReasonValue},
		{"nine units, the first of them its own", append([]string{own}, others(8)...), "os_upgrade.stop_units", ReasonTooMany},
	} {
		field, reason := invalidAt(t, with(t, "os_upgrade.stop_units", c.units))
		if field != c.field || reason != c.reason {
			t.Errorf("%s: refused for %q %q, want %q %q", c.name, field, reason, c.field, c.reason)
		}
	}
}

// The bounds that were found to move without a test noticing, each tried with the largest value that is taken and the smallest that is not.
func TestTheBoundsThatNothingPinnedBefore(t *testing.T) {
	segments := func(n int) string { return strings.Repeat("/a", n) }
	call := func(n int) string { return quoted("/v2/virt_management" + segments(n)) }
	var oses []string
	for i := 10; i < 27; i++ {
		oses = append(oses, quoted("debian:"+string(rune('0'+i/10))+string(rune('0'+i%10))))
	}
	for _, c := range []struct {
		name, path, literal string
		field, reason       string
	}{
		{"a call of eight segments under the route", "os_upgrade.maintenance.status", call(8), "", ""},
		{"a call of nine segments under the route", "os_upgrade.maintenance.status", call(9), "os_upgrade.maintenance.status", ReasonValue},
		{"a call of one segment under the route", "os_upgrade.maintenance.status", call(1), "", ""},
		{"16 operating systems", "os", "[" + strings.Join(oses[:16], ",") + "]", "", ""},
		{"17 operating systems", "os", "[" + strings.Join(oses[:17], ",") + "]", "os", ReasonTooMany},
		{"a component of 64 characters", "component", quoted("C" + rep("c", 63)), "", ""},
		{"a component of 65 characters", "component", quoted("C" + rep("c", 64)), "component", ReasonValue},
		{"a component that starts with a digit", "component", `"1abc"`, "component", ReasonValue},
		{"a version of four digits, the first number", "min_recasaos", `"9999.0.0"`, "", ""},
		{"a version of five digits, the first number", "min_recasaos", `"10000.0.0"`, "min_recasaos", ReasonValue},
		{"a version of four digits, the second number", "min_recasaos", `"0.9999.0"`, "", ""},
		{"a version of five digits, the second number", "min_recasaos", `"0.10000.0"`, "min_recasaos", ReasonValue},
		{"a version of four digits, the third number", "min_recasaos", `"0.0.9999"`, "", ""},
		{"a version of five digits, the third number", "min_recasaos", `"0.0.10000"`, "min_recasaos", ReasonValue},
		{"an operating system with a major of two digits", "os", `["debian:99"]`, "", ""},
		{"an operating system with a major of three digits", "os", `["debian:100"]`, "os", ReasonValue},
		{"an operating system with a major of one digit", "os", `["ubuntu:9"]`, "", ""},
		{"an operating system with a release of two digits", "os", `["ubuntu:24.04"]`, "", ""},
		{"an operating system with a release of three digits", "os", `["ubuntu:24.004"]`, "os", ReasonValue},
		{"an operating system with no major", "os", `["ubuntu:"]`, "os", ReasonValue},
	} {
		t.Run(c.name, func(t *testing.T) {
			text := withRaw(t, c.path, c.literal)
			if field, reason := invalidAt(t, text); field != c.field || reason != c.reason {
				t.Errorf("refused for %q %q, want %q %q", field, reason, c.field, c.reason)
			}
		})
	}
}

// withRoute is the manifest of the virtual machines module on another route. The calls of the section for an upgrade are under the route, so the section is left out.
func withRoute(t *testing.T, route string) string {
	t.Helper()
	var doc map[string]json.RawMessage
	if err := json.Unmarshal([]byte(withRaw(t, "gateway_route", quoted(route))), &doc); err != nil {
		t.Fatal(err)
	}
	delete(doc, "os_upgrade")
	out, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}
