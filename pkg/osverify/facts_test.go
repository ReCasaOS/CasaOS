package osverify

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

func take(t testing.TB, f *fakeProbe) Facts {
	t.Helper()
	return TakeWith(context.Background(), f, healthyResolver)
}

func sorted(s []string) []string {
	out := slices.Clone(s)
	slices.Sort(out)
	return out
}

func TestTheFactsOfAHealthyBox(t *testing.T) {
	f := healthyBox(t)
	got := take(t, f)
	want := Facts{
		Taken: true, At: f.now,
		Kernel: "6.1.0-25-amd64", System: "running", DpkgQuiet: true,
		SambaUsers: 4,
		Mergerfs:   []string{"/DATA/pool", "/media/Media Library"},
		Route:      "eth0", DNS: true,
		Units: map[string]string{
			"casaos.service": "active", "casaos-gateway.service": "active", "casaos-message-bus.service": "active",
			"casaos-user-service.service": "active", "casaos-local-storage.service": "active", "casaos-app-management.service": "active",
		},
		Paused: []string{"docker.list.casaos-paused"},
		Docker: Docker{
			State: DockerUp, Version: "24.0.7", EngineID: "0b7d1a54-6c0e-4f6c-9a46-5d3d1f0f1c11",
			Containers: []Container{
				{Name: "db", Policy: "on-failure"},
				{Name: "jellyfin", Policy: "unless-stopped", Binds: []string{"/DATA/pool/Media", "/DATA/AppData/jellyfin/config"}},
				{Name: "pihole", Policy: "always", Binds: []string{"/DATA/AppData/pihole/etc"}},
				{Name: "scratch", Policy: "no", Binds: []string{"/media/Media Library/scratch"}},
			},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("facts = %+v\nwant    %+v", got, want)
	}
	// nothing was left unknown, so every command that the facts need passed the allow-list (a command that is not on it is
	// not asked, and its fact is unknown)
	for _, argv := range f.asked {
		if !osreadiness.Allowed(argv) {
			t.Errorf("%q reached the machine and is not on the allow-list", argv)
		}
	}
}

func TestOnlyWhatTheAllowListAllowsIsAsked(t *testing.T) {
	f := newFake()
	tk := &taker{ctx: context.Background(), p: f}
	for _, argv := range [][]string{{"apt-get", "update"}, {"pdbedit", "-L", "-v"}, {"systemctl", "restart", "docker.service"}, {"docker", "start", "x"}, {"sh", "-c", "true"}, {}} {
		if _, err := tk.ask(argv...); !errors.Is(err, osreadiness.ErrNotAllowed) {
			t.Errorf("%q: err = %v, want ErrNotAllowed", argv, err)
		}
	}
	if len(f.asked) != 0 {
		t.Errorf("the machine was asked %q", f.asked)
	}
	f.on("x", 0, "uname", "-r")
	if out, err := tk.ask("uname", "-r"); err != nil || out.Text != "x" || len(f.asked) != 1 {
		t.Errorf("an allowed command: %+v, %v, asked %q", out, err, f.asked)
	}
}

func TestFactsCarryNothingThatCannotBeKnown(t *testing.T) {
	var zero Facts
	for _, fact := range allFacts {
		if zero.Knows(fact) {
			t.Errorf("the zero facts know %s", fact)
		}
	}
	got := take(t, healthyBox(t))
	for _, fact := range allFacts {
		if !got.Knows(fact) {
			t.Errorf("a healthy box does not know %s", fact)
		}
	}
	got.Unknown = []string{FactDNS}
	if got.Knows(FactDNS) || !got.Knows(FactRoute) {
		t.Errorf("Knows with dns unknown: dns %v, route %v", got.Knows(FactDNS), got.Knows(FactRoute))
	}
}

func TestFactsSurviveTheirFile(t *testing.T) {
	got := take(t, healthyBox(t))
	data, err := json.Marshal(got)
	if err != nil {
		t.Fatal(err)
	}
	var back Facts
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back, got) {
		t.Errorf("after its file: %+v\nbefore:         %+v", back, got)
	}
}

func TestACancelledContextTakesNothingAndSaysSo(t *testing.T) {
	f := healthyBox(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := TakeWith(ctx, f, func(context.Context, string) bool { t.Error("the resolver was asked"); return true })
	if !got.Taken {
		t.Error("facts that were taken say they were not")
	}
	if len(f.asked) != 0 || len(f.read) != 0 || len(f.listed) != 0 {
		t.Errorf("the machine was asked %q, and read %q and listed %q", f.asked, f.read, f.listed)
	}
	if !slices.Equal(sorted(got.Unknown), sorted(allFacts)) {
		t.Errorf("unknown = %v, want every fact", got.Unknown)
	}
}

func TestSambaUsersAreTheLinesThatNameAUser(t *testing.T) {
	for name, tc := range map[string]struct {
		out  string
		exit int
		want int
	}{
		"the fixture, with a warning of Samba's in front": {fixture(t, "pdbedit-L"), 0, 4},
		"none":              {"", 0, 0},
		"only a warning":    {"WARNING: The \"syslog\" option is deprecated\n", 0, 0},
		"a record of -v":    {"Unix username:        gary\nNT username:          gary\nAccount Flags:        [U          ]\n", 0, 0},
		"windows line ends": {"gary:1000:Gary\r\nmedia:1001:Media\r\n", 0, 2},
		"a name with a space and a colon in the full name": {"mary jane:1003:Mary: Jane\n", 0, 1},
		"a uid that is not a number":                       {"gary:x:Gary\n", 0, 0},
		"no uid":                                           {"gary\n", 0, 0},
		"an empty name":                                    {":1000:x\n", 0, 0},
	} {
		f := healthyBox(t)
		f.on(tc.out, tc.exit, "pdbedit", "-L")
		got := take(t, f)
		if !got.Knows(FactSamba) || got.SambaUsers != tc.want {
			t.Errorf("%s: users %d (known %v), want %d", name, got.SambaUsers, got.Knows(FactSamba), tc.want)
		}
	}
}

func TestSambaUsersAreUnknownWhenPdbeditCannotSay(t *testing.T) {
	for name, set := range map[string]func(*fakeProbe){
		"the tool is not installed": func(f *fakeProbe) { f.forget("pdbedit", "-L") },
		"it fails":                  func(f *fakeProbe) { f.on("Can't open smb.conf\n", 1, "pdbedit", "-L") },
		"it cannot be run":          func(f *fakeProbe) { f.onError(errors.New("fork failed"), "pdbedit", "-L") },
		"its output was cut":        func(f *fakeProbe) { f.onTruncated("gary:1000:Gary\n", "pdbedit", "-L") },
	} {
		f := healthyBox(t)
		set(f)
		got := take(t, f)
		if got.Knows(FactSamba) || got.SambaUsers != 0 {
			t.Errorf("%s: known %v, users %d", name, got.Knows(FactSamba), got.SambaUsers)
		}
		if !got.Knows(FactMounts) {
			t.Errorf("%s: the other facts are lost too: %v", name, got.Unknown)
		}
	}
}

func TestMergerfsMountsAreTheMountsOfTypeFuseMergerfs(t *testing.T) {
	got := take(t, healthyBox(t))
	want := []string{"/DATA/pool", "/media/Media Library"}
	if !slices.Equal(got.Mergerfs, want) {
		t.Errorf("mounts = %q, want %q (the decoys of the fixture are not pools, and \\040 is a space)", got.Mergerfs, want)
	}
	for name, tc := range map[string]struct {
		data string
		want []string
	}{
		"none":                  {"proc /proc proc rw 0 0\n", nil},
		"a pool twice":          {"a:b /p fuse.mergerfs rw 0 0\na:b /p fuse.mergerfs rw 0 0\n", []string{"/p"}},
		"in order":              {"a /z fuse.mergerfs rw 0 0\na /a fuse.mergerfs rw 0 0\n", []string{"/a", "/z"}},
		"the escapes of a path": {"a /m\\040n\\134x fuse.mergerfs rw 0 0\n", []string{"/m n\\x"}},
		"a newline in a path":   {"a /m\\012n fuse.mergerfs rw 0 0\nb /ok fuse.mergerfs rw 0 0\n", []string{"/ok"}},
		"a tab in a path":       {"a /m\\011n fuse.mergerfs rw 0 0\nb /ok fuse.mergerfs rw 0 0\n", []string{"/ok"}},
		"an escape that is not": {"a /m\\0x0n fuse.mergerfs rw 0 0\nb /m\\999 fuse.mergerfs rw 0 0\nc /e\\04 fuse.mergerfs rw 0 0\n", []string{"/e\\04", "/m\\0x0n", "/m\\999"}},
		"a line too short":      {"a /p\nfuse.mergerfs\nb /ok fuse.mergerfs rw 0 0\n", []string{"/ok"}},
		"a longer type":         {"a /p fuse.mergerfs-x rw 0 0\na /q xfuse.mergerfs rw 0 0\na /r fuse rw 0 0\n", nil},
		"windows line ends":     {"a /p fuse.mergerfs rw 0 0\r\n", []string{"/p"}},
	} {
		f := healthyBox(t)
		f.set("/proc/mounts", tc.data)
		got := take(t, f)
		if !got.Knows(FactMounts) || !slices.Equal(got.Mergerfs, tc.want) {
			t.Errorf("%s: mounts %q (known %v), want %q", name, got.Mergerfs, got.Knows(FactMounts), tc.want)
		}
	}
}

func TestMountsAreUnknownWhenTheyCannotBeRead(t *testing.T) {
	for name, err := range map[string]error{"not there": notExist("open", "/proc/mounts"), "refused": &fs.PathError{Op: "open", Path: "/proc/mounts", Err: fs.ErrPermission}, "too large": osreadiness.ErrTooLarge} {
		f := healthyBox(t)
		f.fail("/proc/mounts", err)
		got := take(t, f)
		if got.Knows(FactMounts) || got.Mergerfs != nil {
			t.Errorf("%s: known %v, mounts %q", name, got.Knows(FactMounts), got.Mergerfs)
		}
	}
}

func TestTheDefaultRouteIsTheOneThatGoesNowhereInParticular(t *testing.T) {
	const header = "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\t\tMTU\tWindow\tIRTT\n"
	for name, tc := range map[string]struct {
		route string
		want  string
	}{
		"the fixture":                       {fixture(t, "proc-net-route"), "eth0"},
		"no route at all":                   {header, ""},
		"only the local network":            {header + "eth0\t0001A8C0\t00000000\t0001\t0\t0\t100\t00FFFFFF\t0\t0\t0\n", ""},
		"a default route that is down":      {header + "eth0\t00000000\t0101A8C0\t0002\t0\t0\t100\t00000000\t0\t0\t0\n", ""},
		"a destination that is not default": {header + "eth0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00FFFFFF\t0\t0\t0\n", ""},
		"a mask that is not default":        {header + "eth0\t0001A8C0\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n", ""},
		"the first of two":                  {header + "ens18\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\nwlan0\t00000000\t0101A8C0\t0003\t0\t0\t600\t00000000\t0\t0\t0\n", "ens18"},
		"spaces for tabs":                   {header + "eth0 00000000 0101A8C0 0003 0 0 100 00000000 0 0 0\n", "eth0"},
		"a name that is not an interface's": {header + "e;th0\t00000000\t0101A8C0\t0003\t0\t0\t100\t00000000\t0\t0\t0\n", "other"},
		"a flag that is not a number":       {header + "eth0\t00000000\t0101A8C0\tzz\t0\t0\t100\t00000000\t0\t0\t0\n", ""},
		"a short line":                      {header + "eth0\t00000000\n", ""},
	} {
		f := healthyBox(t)
		f.set("/proc/net/route", tc.route)
		got := take(t, f)
		if !got.Knows(FactRoute) || got.Route != tc.want {
			t.Errorf("%s: route %q (known %v), want %q", name, got.Route, got.Knows(FactRoute), tc.want)
		}
	}
	f := healthyBox(t)
	f.fail("/proc/net/route", notExist("open", "/proc/net/route"))
	if got := take(t, f); got.Knows(FactRoute) {
		t.Error("a table of routes that is not there is a route that is not there")
	}
}

func TestTheResolverIsAskedWithinItsTime(t *testing.T) {
	var asked []string
	answers := func(ok bool) Resolver {
		return func(_ context.Context, name string) bool { asked = append(asked, name); return ok }
	}
	if got := TakeWith(context.Background(), healthyBox(t), answers(true)); !got.DNS || !got.Knows(FactDNS) {
		t.Errorf("a resolver that answers: dns %v, known %v", got.DNS, got.Knows(FactDNS))
	}
	if got := TakeWith(context.Background(), healthyBox(t), answers(false)); got.DNS || !got.Knows(FactDNS) {
		t.Errorf("a resolver that does not: dns %v, known %v", got.DNS, got.Knows(FactDNS))
	}
	if !slices.Equal(asked, []string{"deb.debian.org", "deb.debian.org"}) {
		t.Errorf("the resolver was asked %q", asked)
	}

	// one that does not answer in time is a resolver that does not answer, whatever the time the facts have
	old := dnsTimeout
	dnsTimeout = 30 * time.Millisecond
	defer func() { dnsTimeout = old }()
	slow := func(ctx context.Context, _ string) bool { <-ctx.Done(); return false }
	start := time.Now()
	got := TakeWith(context.Background(), healthyBox(t), slow)
	if got.DNS || !got.Knows(FactDNS) {
		t.Errorf("a resolver that never answers: dns %v, known %v", got.DNS, got.Knows(FactDNS))
	}
	if time.Since(start) > 5*time.Second {
		t.Errorf("the facts waited %v for a resolver", time.Since(start))
	}

	// but one that did not answer because the facts themselves ran out of time is not known to be silent
	ctx, cancel := context.WithCancel(context.Background())
	gone := func(ctx context.Context, _ string) bool { cancel(); <-ctx.Done(); return false }
	got = TakeWith(ctx, healthyBox(t), gone)
	if got.Knows(FactDNS) {
		t.Errorf("the facts were cancelled and say that dns is known (%v)", got.DNS)
	}
}

func TestTheStateOfTheSystemIsASystemdWord(t *testing.T) {
	for name, tc := range map[string]struct {
		out, want string
		exit      int
		err       error
		known     bool
	}{
		"running":                            {"running\n", "running", 0, nil, true},
		"degraded, which ends with a status": {"degraded\n", "degraded", 1, nil, true},
		"starting":                           {"starting\n", "starting", 1, nil, true},
		"maintenance":                        {"maintenance\n", "maintenance", 1, nil, true},
		"stopping":                           {"stopping\n", "stopping", 1, nil, true},
		"initializing":                       {"initializing\n", "initializing", 1, nil, true},
		"offline":                            {"offline\n", "offline", 1, nil, true},
		"unknown is no answer":               {"unknown\n", "", 1, nil, false},
		"an error of the bus":                {"Failed to connect to bus: No such file or directory\n", "", 1, nil, false},
		"nothing":                            {"", "", 1, nil, false},
		"a made-up word":                     {"fine\n", "", 0, nil, false},
		"not installed":                      {"", "", 0, osreadiness.ErrToolMissing, false},
	} {
		f := healthyBox(t)
		if tc.err != nil {
			f.onError(tc.err, "systemctl", "is-system-running")
		} else {
			f.on(tc.out, tc.exit, "systemctl", "is-system-running")
		}
		got := take(t, f)
		if got.Knows(FactSystem) != tc.known || got.System != tc.want {
			t.Errorf("%s: system %q (known %v), want %q (known %v)", name, got.System, got.Knows(FactSystem), tc.want, tc.known)
		}
	}
}

func TestTheKernelIsWhatUnameSays(t *testing.T) {
	for name, tc := range map[string]struct {
		out, want string
		exit      int
	}{
		"a kernel":       {"6.1.0-25-amd64\n", "6.1.0-25-amd64", 0},
		"a pve kernel":   {"6.2.16-3-pve\n", "6.2.16-3-pve", 0},
		"something else": {"6.1 0\n", "", 0},
		"a failure":      {"6.1.0-25-amd64\n", "", 1},
		"nothing":        {"", "", 0},
	} {
		f := healthyBox(t)
		f.on(tc.out, tc.exit, "uname", "-r")
		got := take(t, f)
		if got.Kernel != tc.want || got.Knows(FactKernel) != (tc.want != "") {
			t.Errorf("%s: kernel %q (known %v), want %q", name, got.Kernel, got.Knows(FactKernel), tc.want)
		}
	}
}

func TestDpkgIsQuietWhenItSaysNothingAndEndsWell(t *testing.T) {
	for name, tc := range map[string]struct {
		out       string
		exit      int
		truncated bool
		quiet     bool
	}{
		"quiet":                    {"", 0, false, true},
		"only a newline":           {"\n", 0, false, true},
		"a half installed package": {"The following packages are only half configured, probably due to problems\nconfiguring them the first time.\n libc6:amd64\n", 0, false, false},
		"an error and no words":    {"", 1, false, false},
		"words and an error":       {"dpkg: error: cannot access archive\n", 2, false, false},
		"cut":                      {"", 0, true, false},
	} {
		f := healthyBox(t)
		if tc.truncated {
			f.onTruncated(tc.out, "dpkg", "--audit")
		} else {
			f.on(tc.out, tc.exit, "dpkg", "--audit")
		}
		got := take(t, f)
		if got.DpkgQuiet != tc.quiet || !got.Knows(FactDpkg) {
			t.Errorf("%s: quiet %v (known %v), want %v", name, got.DpkgQuiet, got.Knows(FactDpkg), tc.quiet)
		}
	}
	f := healthyBox(t)
	f.forget("dpkg", "--audit")
	if got := take(t, f); got.Knows(FactDpkg) || got.DpkgQuiet {
		t.Errorf("no dpkg: known %v, quiet %v", got.Knows(FactDpkg), got.DpkgQuiet)
	}
}

func TestTheUnitsOfReCasaOSAreTheOnesTheBoxHasFilesFor(t *testing.T) {
	f := healthyBox(t)
	f.dir("/etc/systemd/system", "casaos-extra.timer", "casaos.service.d", "casaos-os-upgrade.service", "casaos-update.service", "casaos-package-update.service", "casaos-docker-update.service", "casaosx.conf", "mycasaos.service", "casaos-x.socket", "casaos-y.path", "casaos-z.mount", "casaos-gateway.service")
	for _, unit := range []string{"casaos-extra.timer", "casaos-x.socket", "casaos-y.path"} {
		f.on("inactive\n", 0, argvUnit(unit)...)
	}
	f.on("failed\n", 0, argvUnit("casaos-gateway.service")...)
	got := take(t, f)
	want := map[string]string{
		"casaos.service": "active", "casaos-gateway.service": "failed", "casaos-message-bus.service": "active", "casaos-user-service.service": "active",
		"casaos-local-storage.service": "active", "casaos-app-management.service": "active",
		"casaos-extra.timer": "inactive", "casaos-x.socket": "inactive", "casaos-y.path": "inactive",
	}
	if !got.Knows(FactUnits) || !reflect.DeepEqual(got.Units, want) {
		t.Errorf("units = %v, want %v (the transient units of the core and the files that are not units are not asked about)", got.Units, want)
	}
	for _, argv := range f.asked {
		if argv[0] == "systemctl" && len(argv) > 2 && strings.Contains(argv[2], "update") || strings.Contains(strings.Join(argv, " "), "casaos-os-upgrade") {
			t.Errorf("a transient unit was asked about: %q", argv)
		}
	}
}

func TestAUnitThatCannotBeAskedAboutIsNotSaidToBeDown(t *testing.T) {
	f := healthyBox(t)
	f.forget(argvUnit("casaos-gateway.service")...)
	f.on("exploded\n", 0, argvUnit("casaos-message-bus.service")...)
	f.onError(errors.New("bus"), argvUnit("casaos.service")...)
	got := take(t, f)
	for _, unit := range []string{"casaos-gateway.service", "casaos-message-bus.service", "casaos.service"} {
		if got.Units[unit] != unitUnknown {
			t.Errorf("%s = %q, want %q", unit, got.Units[unit], unitUnknown)
		}
	}
	if got.Units["casaos-user-service.service"] != "active" {
		t.Errorf("a unit that was answered is lost: %v", got.Units)
	}
}

func TestUnitsAreUnknownWhenNoDirectoryOfUnitsCanBeRead(t *testing.T) {
	f := healthyBox(t)
	delete(f.dirs, "/usr/lib/systemd/system")
	delete(f.dirs, "/etc/systemd/system")
	if got := take(t, f); !got.Knows(FactUnits) || len(got.Units) != 0 {
		t.Errorf("no directory of units (a box without them has none to ask about): known %v, units %v", got.Knows(FactUnits), got.Units)
	}
	f.fail("/usr/lib/systemd/system", &fs.PathError{Op: "open", Path: "/usr/lib/systemd/system", Err: fs.ErrPermission})
	if got := take(t, f); got.Knows(FactUnits) {
		t.Errorf("a directory of units that cannot be read, and the units are known: %v", got.Units)
	}
}

func TestADirectoryOfUnitsThatIsLinkedIsReadOnce(t *testing.T) {
	f := healthyBox(t)
	f.dir("/lib/systemd/system", "casaos.service", "casaos-gateway.service")
	got := take(t, f)
	count := 0
	for _, argv := range f.asked {
		if slices.Equal(argv, argvUnit("casaos.service")) {
			count++
		}
	}
	if count != 1 || len(got.Units) != 6 {
		t.Errorf("casaos.service was asked about %d times, units %v", count, got.Units)
	}
}

func TestDockerInEveryState(t *testing.T) {
	for name, tc := range map[string]struct {
		set    func(*fakeProbe)
		state  string
		known  bool
		client bool
	}{
		"up": {func(*fakeProbe) {}, DockerUp, true, true},
		"its service is stopped and its client is there": {func(f *fakeProbe) {
			f.on("inactive\n", 0, argvDockerState...)
			f.on("Docker version 24.0.7, build afdd53b\n", 0, "docker", "--version")
		}, DockerStopped, true, true},
		"its service is stopped and only its socket is there": {func(f *fakeProbe) {
			f.on("inactive\n", 0, argvDockerState...)
			f.exists["/run/docker.sock"] = true
		}, DockerStopped, true, false},
		"its service failed": {func(f *fakeProbe) {
			f.on("failed\n", 0, argvDockerState...)
			f.on("Docker version 24.0.7, build afdd53b\n", 0, "docker", "--version")
		}, DockerStopped, true, true},
		"no Docker at all": {func(f *fakeProbe) { f.on("inactive\n", 0, argvDockerState...) }, DockerAbsent, true, false},
		"its service runs and the daemon does not answer": {func(f *fakeProbe) {
			f.on("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n", 1, argvDockerInfo...)
		}, DockerDown, true, false},
		"the daemon answers with what is not its answer": {func(f *fakeProbe) { f.on("hello\n", 0, argvDockerInfo...) }, DockerDown, true, false},
		"the daemon prints its answer and ends with an error": {func(f *fakeProbe) {
			f.on(`{"ServerVersion":"24.0.7","SwarmState":"inactive"}`+"\n", 1, argvDockerInfo...)
		}, DockerDown, true, false},
		"the daemon's answer is cut": {func(f *fakeProbe) {
			f.onTruncated(`{"ServerVersion":"24.0.7","SwarmState":"inactive"}`+"\n", argvDockerInfo...)
		}, DockerDown, true, false},
		"the daemon answers with no version":               {func(f *fakeProbe) { f.on(`{"ServerVersion":"","SwarmState":"inactive"}`+"\n", 0, argvDockerInfo...) }, DockerDown, true, false},
		"its service is starting":                          {func(f *fakeProbe) { f.on("activating\n", 0, argvDockerState...) }, DockerUp, true, false},
		"systemd does not say":                             {func(f *fakeProbe) { f.forget(argvDockerState...) }, "", false, false},
		"systemd says something else":                      {func(f *fakeProbe) { f.on("sleeping\n", 0, argvDockerState...) }, "", false, false},
		"the service runs and the client is not installed": {func(f *fakeProbe) { f.forget(argvDockerInfo...) }, "", false, false},
		"the client cannot be run": {func(f *fakeProbe) {
			f.on("inactive\n", 0, argvDockerState...)
			f.onError(errors.New("fork failed"), "docker", "--version")
		}, "", false, false},
	} {
		f := healthyBox(t)
		tc.set(f)
		got := take(t, f)
		if got.Docker.State != tc.state || got.Knows(FactDocker) != tc.known {
			t.Errorf("%s: state %q (known %v), want %q (known %v)", name, got.Docker.State, got.Knows(FactDocker), tc.state, tc.known)
		}
		if tc.state != DockerUp && (got.Docker.Version != "" || got.Docker.EngineID != "" || got.Docker.Containers != nil) {
			t.Errorf("%s: a Docker that was not asked has answers: %+v", name, got.Docker)
		}
		for _, argv := range f.asked {
			if argv[0] == "docker" && argv[1] != "--version" && (tc.state == DockerStopped || tc.state == DockerAbsent) {
				t.Errorf("%s: the daemon was asked %q while its service does not run", name, argv)
			}
		}
	}
}

func TestDockerSaysItsVersionAndItsEngine(t *testing.T) {
	for name, tc := range map[string]struct {
		id, want string
	}{
		"an id":                       {"0b7d1a54-6c0e-4f6c-9a46-5d3d1f0f1c11\n", "0b7d1a54-6c0e-4f6c-9a46-5d3d1f0f1c11"},
		"an old style id":             {"7TRN:IPZB:QYBB:VPBQ:UWUX:TMCE:Z5OM:LJ4B:ZZ6Z:XH57:6MVH:2LDQ\n", "7TRN:IPZB:QYBB:VPBQ:UWUX:TMCE:Z5OM:LJ4B:ZZ6Z:XH57:6MVH:2LDQ"},
		"a file with two lines":       {"abcdef12-0000\nsecond\n", "abcdef12-0000"},
		"something that is not an id": {"not an id\n", ""},
		"nothing":                     {"", ""},
		"too long":                    {strings.Repeat("a", 65) + "\n", ""},
	} {
		f := healthyBox(t)
		f.set("/var/lib/docker/engine-id", tc.id)
		got := take(t, f)
		if got.Docker.EngineID != tc.want || got.Docker.Version != "24.0.7" {
			t.Errorf("%s: engine %q, version %q", name, got.Docker.EngineID, got.Docker.Version)
		}
		if !got.Knows(FactDocker) {
			t.Errorf("%s: an engine that has no readable id is a Docker that is not known (%v)", name, got.Unknown)
		}
	}
	f := healthyBox(t)
	delete(f.files, "/var/lib/docker/engine-id")
	if got := take(t, f); got.Docker.EngineID != "" || !got.Knows(FactDocker) {
		t.Errorf("a Docker with its data elsewhere: engine %q, known %v", got.Docker.EngineID, got.Knows(FactDocker))
	}
}

// declareContainers makes the box run n containers (c000000000001 and so on), and answer for them in groups of two hundred, as
// the allow-list asks.
func declareContainers(f *fakeProbe, n int) {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%012x", i+1)
	}
	f.on(strings.Join(ids, "\n")+"\n", 0, argvDockerPs...)
	f.forget(argvInspect(containerIDs...)...)
	for start := 0; start < n; start += 200 {
		chunk := ids[start:min(start+200, n)]
		var out strings.Builder
		for _, id := range chunk {
			out.WriteString(`{"name":"/c` + id + `","image":"x","restart":"always","network":"bridge","mounts":[],"ports":{}}` + "\n")
		}
		f.on(out.String(), 0, argvInspect(chunk...)...)
	}
}

func inspectsAsked(f *fakeProbe) (calls, widest int) {
	for _, argv := range f.asked {
		if len(argv) > 1 && argv[1] == "inspect" {
			calls++
			widest = max(widest, len(argv)-4)
		}
	}
	return calls, widest
}

func TestTheContainersAreAskedAboutInGroupsAndOnlyAsLongAsTheyAnswer(t *testing.T) {
	f := healthyBox(t)
	declareContainers(f, 450)
	got := take(t, f)
	if !got.Knows(FactContainers) || len(got.Docker.Containers) != 450 {
		t.Fatalf("containers: %d (known %v)", len(got.Docker.Containers), got.Knows(FactContainers))
	}
	if !slices.IsSortedFunc(got.Docker.Containers, func(a, b Container) int { return strings.Compare(a.Name, b.Name) }) {
		t.Error("the containers are not sorted by name")
	}
	if calls, widest := inspectsAsked(f); calls != 3 || widest != 200 {
		t.Errorf("%d inspects of up to %d containers for 450 containers, want 3 of up to 200", calls, widest)
	}

	// a box has a thousand containers at the most: the thousandth is listed, and with the next one the list is not taken, and
	// the machine is not asked about any of them
	f = healthyBox(t)
	declareContainers(f, 1000)
	if got := take(t, f); !got.Knows(FactContainers) || len(got.Docker.Containers) != 1000 {
		t.Errorf("1000 containers: known %v, %d listed", got.Knows(FactContainers), len(got.Docker.Containers))
	}
	f = healthyBox(t)
	declareContainers(f, 1001)
	got = take(t, f)
	if got.Knows(FactContainers) || got.Docker.Containers != nil || got.Docker.State != DockerUp {
		t.Errorf("1001 containers: known %v, %d listed, state %q", got.Knows(FactContainers), len(got.Docker.Containers), got.Docker.State)
	}
	if calls, _ := inspectsAsked(f); calls != 0 {
		t.Errorf("%d inspects for a list that is too long", calls)
	}

	for name, set := range map[string]func(*fakeProbe){
		"ps fails":      func(f *fakeProbe) { f.on("error\n", 1, argvDockerPs...) },
		"ps is cut":     func(f *fakeProbe) { f.onTruncated(strings.Join(containerIDs, "\n"), argvDockerPs...) },
		"inspect fails": func(f *fakeProbe) { f.on("Error: No such object\n", 1, argvInspect(containerIDs...)...) },
		"ps is not ids": func(f *fakeProbe) { f.on("CONTAINER ID   IMAGE\n", 0, argvDockerPs...) },
	} {
		f := healthyBox(t)
		set(f)
		got := take(t, f)
		if got.Knows(FactContainers) || got.Docker.Containers != nil {
			t.Errorf("%s: known %v, containers %v", name, got.Knows(FactContainers), got.Docker.Containers)
		}
		if got.Docker.State != DockerUp || got.Docker.Version != "24.0.7" || !got.Knows(FactDocker) {
			t.Errorf("%s: a daemon that answered lost its answers: %+v", name, got.Docker)
		}
	}

	// no container running is a list, and an empty one
	f = healthyBox(t)
	f.on("", 0, argvDockerPs...)
	if got := take(t, f); !got.Knows(FactContainers) || len(got.Docker.Containers) != 0 {
		t.Errorf("no container: known %v, %v", got.Knows(FactContainers), got.Docker.Containers)
	}
}

func TestWhatInspectSaysIsReadStrictly(t *testing.T) {
	text := strings.Join([]string{
		`{"name":"/ok","image":"x","restart":"always","network":"bridge","mounts":[{"Type":"bind","Source":"/a/b"},{"Type":"bind","Source":"/a/\u0007b"},{"Type":"tmpfs","Source":""},{"Type":"bind","Source":"/c"}],"ports":{}}`,
		`not json`,
		`{"name":"/bad name","image":"x","restart":"always","network":"bridge","mounts":[],"ports":{}}`,
		`{"name":"/-bad","image":"x","restart":"always","network":"bridge","mounts":[],"ports":{}}`,
		`{"name":"/policy","image":"x","restart":"Always!","network":"bridge","mounts":[],"ports":{}}`,
		`{"name":"/nopolicy","image":"x","restart":"","network":"bridge","mounts":[],"ports":{}}`,
		`[1,2]`,
		``,
		`{"name":"/` + strings.Repeat("n", 129) + `","restart":"no","mounts":[]}`,
		`{"name":"/long","restart":"no","mounts":[{"Type":"bind","Source":"/` + strings.Repeat("p", 300) + `"}]}`,
	}, "\n")
	got := parseInspected(text)
	var names []string
	for _, c := range got {
		names = append(names, c.Name+"/"+c.Policy)
	}
	if want := []string{"long/no", "nopolicy/", "ok/always"}; !slices.Equal(names, want) {
		t.Errorf("containers = %q, want %q", names, want)
	}
	for _, c := range got {
		switch c.Name {
		case "ok":
			if !slices.Equal(c.Binds, []string{"/a/b", "/c"}) {
				t.Errorf("binds of ok = %q: a source with a control character, and a mount that is not a bind, are left out", c.Binds)
			}
		case "long":
			if len(c.Binds) != 0 {
				t.Errorf("a bind of 300 characters is kept: %q", c.Binds)
			}
		}
	}
	many := `{"name":"/m","restart":"no","mounts":[`
	for i := 0; i < 40; i++ {
		many += fmt.Sprintf(`{"Type":"bind","Source":"/b%d"},`, i)
	}
	many += `{"Type":"bind","Source":"/last"}]}`
	if got := parseInspected(many); len(got) != 1 || len(got[0].Binds) != maxBinds {
		t.Errorf("a container with 41 binds keeps %d", len(got[0].Binds))
	}
}

func TestWhatIsLeftPausedIsLookedForInFilesAndLines(t *testing.T) {
	f := healthyBox(t)
	f.dir("/etc/apt/sources.list.d", "docker.list.casaos-paused", "plex.list.casaos-paused", "notes.txt", "mixed.list", "plain.list", "docker.list.casaos-paused.bak")
	f.set("/etc/apt/sources.list", "deb http://deb.debian.org/debian bookworm main\n# casaos-paused: deb http://ppa.example/ubuntu bullseye main\n# a comment\n# casaos-paused: deb http://other.example bullseye main\n")
	f.set("/etc/apt/sources.list.d/mixed.list", "deb http://x bookworm main\n# casaos-paused: deb http://y bullseye main\n")
	f.set("/etc/apt/sources.list.d/plain.list", "deb http://z bookworm main\n")
	got := take(t, f)
	want := []string{"docker.list.casaos-paused", "mixed.list:2", "plex.list.casaos-paused", "sources.list:2", "sources.list:4"}
	if !got.Knows(FactSources) || !slices.Equal(got.Paused, want) {
		t.Errorf("paused = %q (known %v), want %q", got.Paused, got.Knows(FactSources), want)
	}

	f = healthyBox(t)
	f.dir("/etc/apt/sources.list.d")
	f.set("/etc/apt/sources.list", "deb http://deb.debian.org/debian bookworm main\n")
	if got := take(t, f); !got.Knows(FactSources) || len(got.Paused) != 0 {
		t.Errorf("nothing paused: known %v, %q", got.Knows(FactSources), got.Paused)
	}

	f = healthyBox(t)
	delete(f.dirs, "/etc/apt/sources.list.d")
	delete(f.files, "/etc/apt/sources.list")
	if got := take(t, f); !got.Knows(FactSources) || len(got.Paused) != 0 {
		t.Errorf("no sources of apt at all: known %v, %q", got.Knows(FactSources), got.Paused)
	}

	for name, set := range map[string]func(*fakeProbe){
		"the directory cannot be read": func(f *fakeProbe) {
			f.fail("/etc/apt/sources.list.d", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		},
		"sources.list cannot be read": func(f *fakeProbe) {
			f.fail("/etc/apt/sources.list", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		},
		"a file of the directory cannot be read": func(f *fakeProbe) {
			f.dirs["/etc/apt/sources.list.d"] = append(f.dirs["/etc/apt/sources.list.d"], "x.list")
			f.fail("/etc/apt/sources.list.d/x.list", &fs.PathError{Op: "open", Path: "x", Err: fs.ErrPermission})
		},
	} {
		f := healthyBox(t)
		set(f)
		if got := take(t, f); got.Knows(FactSources) {
			t.Errorf("%s: the sources are known (%q)", name, got.Paused)
		}
	}
}

func TestTheFactsOfABoxThatAnswersNothingAreAllUnknown(t *testing.T) {
	got := TakeWith(context.Background(), newFake(), healthyResolver)
	// a resolver is a thing of its own, and the units of a box with no directory of them are none
	for _, fact := range allFacts {
		if (fact == FactDNS || fact == FactUnits || fact == FactSources) == !got.Knows(fact) {
			t.Errorf("an empty box: %s known %v", fact, got.Knows(fact))
		}
	}
	if got.Docker.State != "" || got.System != "" || got.Kernel != "" {
		t.Errorf("an empty box has answers: %+v", got)
	}
}
