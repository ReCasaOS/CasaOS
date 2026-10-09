package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The owner's box: Debian 11, docker-ce 28.0.4 from Docker's repository, 29.8.0 on offer.
const (
	debian28 = "5:28.0.4-1~debian.11~bullseye"
	debian29 = "5:29.8.0-1~debian.11~bullseye"

	ownerPolicy = "docker-ce:\n  Installed: " + debian28 + "\n  Candidate: " + debian29 + "\n  Version table:\n     " + debian29 + " 500\n        500 https://download.docker.com/linux/debian bullseye/stable amd64 Packages\n *** " + debian28 + " 100\n        100 /var/lib/dpkg/status\n"
	// the same box, with the engine coming from a mirror that is not Docker's
	mirrorPolicy = "docker-ce:\n  Installed: " + debian28 + "\n  Candidate: " + debian29 + "\n  Version table:\n     " + debian29 + " 500\n        500 http://mirror.example.com/docker bullseye/stable amd64 Packages\n *** " + debian28 + " 100\n        100 /var/lib/dpkg/status\n"
)

func instEngine(name, current, candidate string) string {
	return fmt.Sprintf("Inst %s [%s] (%s Docker CE:bullseye [amd64])\n", name, current, candidate)
}

var (
	ownerDockerCE = instEngine("docker-ce", debian28, debian29)
	ownerCLI      = instEngine("docker-ce-cli", debian28, debian29)
	ownerContaind = instEngine("containerd.io", "1.7.27-1", "2.1.4-1")
)

// ownerBox is the box the button is built for, with every answer the check needs given: any
// command it does not answer fails the test.
func ownerBox(t *testing.T) *aptBox {
	return &aptBox{
		strict:            t,
		upgradeSimulation: simLibc + ownerDockerCE + ownerCLI + ownerContaind,
		dockerCE:          debian28,
		packages:          map[string]string{"docker-ce-cli": debian28, "containerd.io": "1.7.27-1"},
		policy:            ownerPolicy,
		enginePlans:       map[string]string{"docker-ce": ownerDockerCE, "docker-ce-cli": ownerCLI, "containerd.io": ownerContaind},
	}
}

func checkBox(t *testing.T, box *aptBox, tweak func(*systemPackageUpdater)) SystemPackageUpdates {
	t.Helper()
	updater := newTestSystemPackageUpdater(t)
	updater.command = box.command
	if tweak != nil {
		tweak(updater)
	}
	got, err := updater.check()
	if err != nil {
		t.Fatalf("check() error = %v", err)
	}
	return got
}

// engineSimulations are the calls that simulated installing the engine's packages.
func engineSimulations(box *aptBox) []string {
	var calls []string
	for _, call := range box.calls {
		if strings.Contains(call, " -s ") && strings.Contains(call, " install ") {
			calls = append(calls, call)
		}
	}
	return calls
}

func TestDockerUpdateOwnersCase(t *testing.T) {
	box := ownerBox(t)
	got := checkBox(t, box, nil)

	if got.Docker == nil || got.Docker.Update == nil {
		t.Fatalf("Docker = %#v: no update", got.Docker)
	}
	update := got.Docker.Update
	if !update.Available || update.Refusal != "" || update.RefusalDetail == nil || len(update.RefusalDetail) != 0 {
		t.Errorf("update = %#v, want available and no refusal", update)
	}
	if update.From != "28.0.4" || update.To != "29.8.0" || !update.MajorJump {
		t.Errorf("from %q to %q, major jump %v: want 28.0.4, 29.8.0, true", update.From, update.To, update.MajorJump)
	}
	wantPackages := []SystemPackageUpdate{
		{Name: "containerd.io", CurrentVersion: "1.7.27-1", CandidateVersion: "2.1.4-1"},
		{Name: "docker-ce", CurrentVersion: debian28, CandidateVersion: debian29},
		{Name: "docker-ce-cli", CurrentVersion: debian28, CandidateVersion: debian29},
	}
	if !reflect.DeepEqual(update.Packages, wantPackages) {
		t.Errorf("packages = %#v, want %#v", update.Packages, wantPackages)
	}

	// the plan's id is the hash of its sorted lines "name current>candidate"
	var lines []string
	for _, pkg := range wantPackages {
		lines = append(lines, pkg.Name+" "+pkg.CurrentVersion+">"+pkg.CandidateVersion)
	}
	sort.Strings(lines)
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	if update.PlanID != hex.EncodeToString(sum[:]) || len(update.PlanID) != 64 {
		t.Errorf("plan_id = %q, want %x", update.PlanID, sum)
	}
	// ... and the same whatever order apt printed them in, and on the next look
	reversed := ownerBox(t)
	reversed.enginePlans = nil
	reversed.installSimulation = ownerCLI + ownerContaind + ownerDockerCE
	if again := checkBox(t, reversed, nil); again.Docker.Update.PlanID != update.PlanID || !reflect.DeepEqual(again.Docker.Update.Packages, wantPackages) {
		t.Errorf("after apt printed the lines in another order: plan_id = %q, packages = %#v; want %q and %#v", again.Docker.Update.PlanID, again.Docker.Update.Packages, update.PlanID, wantPackages)
	}
	if again := checkBox(t, ownerBox(t), nil); again.Docker.Update.PlanID != update.PlanID {
		t.Errorf("plan_id = %q on a second look, want %q", again.Docker.Update.PlanID, update.PlanID)
	}

	// the transaction apt was asked for: an upgrade of what is on the box, nothing removed, no lock
	want := "/usr/bin/apt-get -s --no-remove -o Debug::NoLocking=true -o Dpkg::Use-Pty=0 install --only-upgrade --no-install-recommends containerd.io docker-ce docker-ce-cli"
	if calls := engineSimulations(box); len(calls) != 1 || calls[0] != want {
		t.Errorf("simulations = %#v, want exactly %q", calls, want)
	}
	// the daemon was asked once, in the format the parser reads
	var asked int
	for _, call := range box.calls {
		if call == "/usr/bin/docker info --format "+dockerpkg.DaemonInfoFormat {
			asked++
		}
	}
	if asked != 1 {
		t.Errorf("docker info was run %d times: %#v", asked, box.calls)
	}

	// the Docker line itself is what it was
	if got.Docker.Version != "28.0.4" || got.Docker.Origin != "docker-repository" || !got.Docker.RestartsDocker || len(got.Docker.Updates) != 3 ||
		got.Docker.Candidate != "" || got.Docker.Held || !strings.HasPrefix(got.Docker.ManualCommand, "sudo apt-get update && sudo apt-get install --only-upgrade ") {
		t.Errorf("Docker = %#v", got.Docker)
	}
	if got.Count != 1 || len(got.Updates) != 1 || got.Updates[0].Name != "libc6" {
		t.Errorf("the update list = %#v: Docker's packages are not in it", got.Updates)
	}
}

func TestDockerUpdateAPatchOfTheSameMajor(t *testing.T) {
	box := ownerBox(t)
	const current, candidate = "5:29.8.1-1~debian.11~bullseye", "5:29.8.2-1~debian.11~bullseye"
	engine, client := instEngine("docker-ce", current, candidate), instEngine("docker-ce-cli", current, candidate)
	box.upgradeSimulation = engine + client
	box.dockerCE = current
	box.packages = map[string]string{"docker-ce-cli": current}
	box.policy = strings.NewReplacer(debian28, current, debian29, candidate).Replace(ownerPolicy)
	box.enginePlans = map[string]string{"docker-ce": engine, "docker-ce-cli": client}

	update := checkBox(t, box, nil).Docker.Update
	if update == nil || !update.Available || update.From != "29.8.1" || update.To != "29.8.2" || update.MajorJump || len(update.Packages) != 2 {
		t.Fatalf("update = %#v, want 29.8.1 to 29.8.2, available, no major jump", update)
	}
}

func TestDockerUpdateIsFoundWhenAptKeepsDockerCEBack(t *testing.T) {
	// apt's upgrade leaves docker-ce out (it would need a new dependency), but installing it
	// by name resolves it: the plan is the explicit install, not the upgrade
	box := ownerBox(t)
	box.upgradeSimulation = simLibc
	got := checkBox(t, box, nil)

	if got.Docker.Candidate != "29.8.0" || len(got.Docker.Updates) != 0 {
		t.Fatalf("Docker = %#v, want a candidate and no update listed", got.Docker)
	}
	update := got.Docker.Update
	if update == nil || !update.Available || update.To != "29.8.0" || !update.MajorJump || len(update.Packages) != 3 {
		t.Fatalf("update = %#v, want the explicit install's plan", update)
	}
}

func TestDockerUpdateOfAPluginAloneKeepsTheEngineVersion(t *testing.T) {
	box := ownerBox(t)
	const plugin = "2.40.0-1~debian.11~bullseye"
	const newer = "2.40.1-1~debian.11~bullseye"
	box.upgradeSimulation = instEngine("docker-compose-plugin", plugin, newer)
	box.packages["docker-compose-plugin"] = plugin
	box.policy = strings.NewReplacer(debian29, debian28).Replace(ownerPolicy) // the engine is current
	box.enginePlans = map[string]string{"docker-compose-plugin": instEngine("docker-compose-plugin", plugin, newer)}

	got := checkBox(t, box, nil)
	update := got.Docker.Update
	if update == nil || !update.Available || update.From != "28.0.4" || update.To != "28.0.4" || update.MajorJump || len(update.Packages) != 1 || update.Packages[0].Name != "docker-compose-plugin" {
		t.Fatalf("update = %#v, want the engine's version for both ends and one package", update)
	}
	if got.Docker.RestartsDocker {
		t.Error("a plugin restarts Docker")
	}
}

func TestDockerUpdateIsOmittedWhenThereIsNothingToUpdate(t *testing.T) {
	// the engine is at the newest version and nothing of its family is pending: the line is
	// there, an update is not, and apt is not asked to simulate one
	box := ownerBox(t)
	box.upgradeSimulation = simLibc
	box.policy = currentPolicy
	box.dockerCE = dockerCE
	got := checkBox(t, box, nil)
	if got.Docker == nil || got.Docker.Update != nil {
		t.Fatalf("Docker = %#v, want a line without an update", got.Docker)
	}
	if calls := engineSimulations(box); len(calls) != 0 {
		t.Errorf("the engine's plan was simulated for a box with nothing to update: %#v", calls)
	}

	// no Docker at all
	if got := checkBox(t, &aptBox{strict: t, upgradeSimulation: simLibc}, nil); got.Docker != nil {
		t.Errorf("Docker = %#v on a box without Docker", got.Docker)
	}

	// a candidate that apt's explicit install does not offer either: nothing to update, so
	// no update, whatever the policy said
	box = ownerBox(t)
	box.upgradeSimulation = simLibc
	box.enginePlans = map[string]string{}
	if got := checkBox(t, box, nil); got.Docker == nil || got.Docker.Candidate == "" || got.Docker.Update != nil {
		t.Errorf("Docker = %#v, want a candidate and no update", got.Docker)
	}
}

type refusalCase struct {
	name string
	// change makes the owner's box what the case is
	change func(*aptBox)
	tweak  func(*systemPackageUpdater)
	code   string
	detail []string
	// planned: the transaction was simulated, and parsed, so that what it is is told
	planned bool
}

func lowDisk(free map[string]uint64) func(*systemPackageUpdater) {
	return func(u *systemPackageUpdater) {
		u.freeBytes = func(path string) (uint64, error) {
			if n, ok := free[path]; ok {
				return n, nil
			}
			return 0, errors.New("statfs " + path + ": no such file or directory")
		}
	}
}

const (
	aptArchives = "/var/cache/apt/archives"
	usr         = "/usr"
	gib         = uint64(1 << 30)
)

func refusalCases() []refusalCase {
	remove := func(line string) func(*aptBox) {
		return func(b *aptBox) { b.enginePlans["docker-ce-cli"] += line }
	}
	return []refusalCase{
		{name: "the engine is a mirror's", change: func(b *aptBox) { b.policy = mirrorPolicy }, code: "origin"},
		{name: "the engine is the distribution's", change: func(b *aptBox) {
			b.dockerCE, b.packages = "", nil
			b.dockerIO = "26.1.5+dfsg1-9"
			b.upgradeSimulation = simLibc + "Inst docker.io [26.1.5+dfsg1-9] (26.1.6+dfsg1-1 Debian:11/stable [amd64])\n"
		}, code: "origin"},
		{name: "docker-ce is on hold", change: func(b *aptBox) { b.dockerCEStatus = "hi" }, code: "held"},
		{name: "the client is on hold", change: func(b *aptBox) { b.statuses = map[string]string{"docker-ce-cli": "hi"} }, code: "held"},
		{name: "a plugin is on hold", change: func(b *aptBox) {
			b.packages["docker-buildx-plugin"] = "0.21.1-1~debian.11~bullseye"
			b.statuses = map[string]string{"docker-buildx-plugin": "hi"}
		}, code: "held"},
		{name: "there is no docker command", tweak: func(u *systemPackageUpdater) {
			u.lookPath = func(name string) (string, error) {
				if name == "docker" {
					return "", errors.New("not found")
				}
				return "/usr/bin/" + name, nil
			}
		}, code: "daemon"},
		{name: "the daemon is stopped", change: func(b *aptBox) { b.noDaemon = true }, code: "daemon"},
		{name: "the daemon does not answer in time", change: func(b *aptBox) { b.daemonHangs = true }, code: "daemon"},
		{name: "the daemon does not say its version", change: func(b *aptBox) { b.daemonInfo = "WARNING: no memory limit support\n" }, code: "daemon"},
		{name: "the node is in a swarm", change: func(b *aptBox) { b.daemonInfo = `{"ServerVersion":"28.0.4","SwarmState":"active"}` }, code: "swarm"},
		{name: "the simulation fails", change: func(b *aptBox) {
			b.installFailure = "E: Unable to correct problems, you have held broken packages."
		}, code: "plan", detail: []string{}},
		{name: "the simulation fails for a removal", change: func(b *aptBox) {
			b.installFailure = "E: Packages need to be removed but remove is disabled."
		}, code: "plan", detail: []string{}},
		{name: "a removal", change: remove("Remv docker-compose-v2 [2.1.0]\n"), code: "plan", detail: []string{"docker-compose-v2"}},
		{name: "a package that is not the engine's", change: remove(simLibc), code: "plan", detail: []string{"libc6"}},
		{name: "a new dependency", change: remove("Inst libnewdep (1.0 Debian:11/stable [amd64])\n"), code: "plan", detail: []string{"libnewdep"}},
		{name: "a new package of the engine", change: remove("Inst docker-model-plugin (1.0.0-1~debian.11~bullseye Docker CE:bullseye [amd64])\n"), code: "plan", detail: []string{"docker-model-plugin"}},
		{name: "a version that is not a version", change: func(b *aptBox) {
			b.enginePlans["docker-ce"] = instEngine("docker-ce", debian28, "5:29.8.0-1;touch${IFS}x")
		}, code: "plan", detail: []string{"docker-ce"}},
		{name: "a package named like a command", change: remove("Inst evil$(reboot) [1] (2 x [amd64])\n"), code: "plan", detail: []string{dockerpkg.InvalidName}},
		{name: "little room in apt's cache", tweak: lowDisk(map[string]uint64{aptArchives: gib - 1, usr: 50 * gib}), code: "disk", planned: true},
		{name: "little room in /usr", tweak: lowDisk(map[string]uint64{aptArchives: 50 * gib, usr: 1 << 20}), code: "disk", planned: true},
		{name: "little room and a place that cannot be read", tweak: lowDisk(map[string]uint64{usr: 1 << 20}), code: "disk", planned: true},
		{name: "no place can be read", tweak: lowDisk(nil), code: "disk", planned: true},
		{name: "no way to read", tweak: func(u *systemPackageUpdater) { u.freeBytes = nil }, code: "disk", planned: true},
	}
}

func TestDockerUpdateRefusals(t *testing.T) {
	for _, c := range refusalCases() {
		t.Run(c.name, func(t *testing.T) {
			box := ownerBox(t)
			if c.change != nil {
				c.change(box)
			}
			got := checkBox(t, box, c.tweak)
			if got.Docker == nil || got.Docker.Update == nil {
				t.Fatalf("Docker = %#v: no update to refuse", got.Docker)
			}
			update := got.Docker.Update
			if update.Available || update.Refusal != c.code {
				t.Errorf("available = %v, refusal = %q, want a refusal %q", update.Available, update.Refusal, c.code)
			}
			wantDetail := c.detail
			if wantDetail == nil {
				wantDetail = []string{}
			}
			if update.RefusalDetail == nil || !reflect.DeepEqual(update.RefusalDetail, wantDetail) {
				t.Errorf("refusal_detail = %#v, want %#v", update.RefusalDetail, wantDetail)
			}
			if update.Packages == nil {
				t.Error("packages is null, want a list")
			}

			// the expensive simulation is made only once the cheap reasons have passed
			simulated := len(engineSimulations(box)) > 0
			if want := c.code == "plan" || c.code == "disk"; simulated != want {
				t.Errorf("simulated = %v, want %v: %#v", simulated, want, box.calls)
			}
			// what the plan says is said when it can be, even if a later reason refuses
			if c.planned {
				if update.PlanID == "" || update.From != "28.0.4" || update.To != "29.8.0" || !update.MajorJump || len(update.Packages) != 3 {
					t.Errorf("update = %#v, want the plan told even though the disk refuses", update)
				}
			} else if update.PlanID != "" || len(update.Packages) != 0 {
				t.Errorf("update = %#v, want no plan", update)
			}
			// nothing a hostile apt printed gets to the page
			encoded, _ := json.Marshal(update)
			for _, bad := range []string{"touch", "reboot", "${IFS}"} {
				if strings.Contains(string(encoded), bad) {
					t.Errorf("the update %s holds %q", encoded, bad)
				}
			}
		})
	}
}

func TestDockerUpdateDiskFloor(t *testing.T) {
	for name, c := range map[string]struct {
		tweak func(*systemPackageUpdater)
		want  bool
	}{
		"exactly the floor on both":          {lowDisk(map[string]uint64{aptArchives: gib, usr: gib}), true},
		"one byte short on one":              {lowDisk(map[string]uint64{aptArchives: gib, usr: gib - 1}), false},
		"apt's cache cannot be read":         {lowDisk(map[string]uint64{usr: 5 * gib}), true},
		"/usr cannot be read":                {lowDisk(map[string]uint64{aptArchives: 5 * gib}), true},
		"room on both":                       {lowDisk(map[string]uint64{aptArchives: 5 * gib, usr: 5 * gib}), true},
		"short on both":                      {lowDisk(map[string]uint64{aptArchives: 100, usr: 100}), false},
		"none left in the one that is there": {lowDisk(map[string]uint64{aptArchives: 0, usr: 5 * gib}), false},
	} {
		update := checkBox(t, ownerBox(t), c.tweak).Docker.Update
		if update == nil || update.Available != c.want || (!c.want && update.Refusal != "disk") || (c.want && update.Refusal != "") {
			t.Errorf("%s: update = %#v, want available = %v", name, update, c.want)
		}
	}
}

func TestDockerUpdateRefusalOrder(t *testing.T) {
	// the first reason that applies is the one given: each case has this reason and the ones after it
	supported := systemPackageSupport{supported: true, aptPath: "/usr/bin/apt-get", systemdPath: "/usr/bin/systemd-run"}
	cases := []struct {
		name    string
		support systemPackageSupport
		change  func(*aptBox)
		tweak   func(*systemPackageUpdater)
		want    string
	}{
		{"unsupported over origin", systemPackageSupport{}, func(b *aptBox) { b.policy = mirrorPolicy }, nil, "unsupported"},
		{"origin over held", supported, func(b *aptBox) { b.policy = mirrorPolicy; b.dockerCEStatus = "hi" }, nil, "origin"},
		{"held over daemon", supported, func(b *aptBox) { b.dockerCEStatus = "hi"; b.noDaemon = true }, nil, "held"},
		{"held over swarm", supported, func(b *aptBox) {
			b.dockerCEStatus = "hi"
			b.daemonInfo = `{"ServerVersion":"28.0.4","SwarmState":"active"}`
		}, nil, "held"},
		{"daemon over plan", supported, func(b *aptBox) { b.noDaemon = true; b.installFailure = "E: no" }, nil, "daemon"},
		{"swarm over plan", supported, func(b *aptBox) {
			b.daemonInfo = `{"ServerVersion":"28.0.4","SwarmState":"active"}`
			b.installFailure = "E: no"
		}, nil, "swarm"},
		{"plan over disk", supported, func(b *aptBox) { b.installFailure = "E: no" }, lowDisk(nil), "plan"},
	}
	for _, c := range cases {
		box := ownerBox(t)
		c.change(box)
		updater := newTestSystemPackageUpdater(t)
		updater.command = box.command
		if c.tweak != nil {
			c.tweak(updater)
		}
		update, plan := updater.dockerUpdatePreflight(context.Background(), c.support)
		if update.Refusal != c.want || update.Available {
			t.Errorf("%s: refusal = %q, available = %v, want %q", c.name, update.Refusal, update.Available, c.want)
		}
		if len(plan.Packages) != 0 {
			t.Errorf("%s: a plan is returned with a refusal for the plan: %#v", c.name, plan)
		}
	}
}

func TestDockerUpdatePreflightOfAnEngineThatIsNotDockerCE(t *testing.T) {
	supported := systemPackageSupport{supported: true, aptPath: "/usr/bin/apt-get"}
	updater := newTestSystemPackageUpdater(t)
	updater.command = (&aptBox{strict: t, snap: true}).command
	if update, _ := updater.dockerUpdatePreflight(context.Background(), supported); update.Refusal != DockerRefusalOrigin {
		t.Errorf("a snap: refusal = %q", update.Refusal)
	}
	updater.command = (&aptBox{strict: t}).command
	if update, _ := updater.dockerUpdatePreflight(context.Background(), supported); update.Refusal != DockerRefusalOrigin {
		t.Errorf("no engine: refusal = %q", update.Refusal)
	}
}

func TestDockerUpdatePreflightHandsTheStartThePlanItWillInstall(t *testing.T) {
	updater := newTestSystemPackageUpdater(t)
	updater.command = ownerBox(t).command
	update, plan := updater.dockerUpdatePreflight(context.Background(), systemPackageSupport{supported: true, aptPath: "/usr/bin/apt-get"})
	if !update.Available || plan.ID() != update.PlanID {
		t.Fatalf("update = %#v, plan = %#v", update, plan)
	}
	// sorted as strings: "docker-ce-cli=" comes before "docker-ce=", "-" being before "="
	wantPins := []string{"containerd.io=2.1.4-1", "docker-ce-cli=" + debian29, "docker-ce=" + debian29}
	if !reflect.DeepEqual(plan.Pins(), wantPins) || !reflect.DeepEqual(plan.Names(), []string{"containerd.io", "docker-ce", "docker-ce-cli"}) {
		t.Errorf("pins = %#v, names = %#v", plan.Pins(), plan.Names())
	}
}

func TestDockerUpdateChangesNothing(t *testing.T) {
	// the check only looks: the one apt-get that is not a simulation is the index refresh the
	// check always made, and nothing is started
	box := ownerBox(t)
	updater := newTestSystemPackageUpdater(t)
	updater.command = box.command
	var started bool
	updater.start = func(string, string, ...string) ([]byte, error) { started = true; return nil, nil }
	if _, err := updater.check(); err != nil {
		t.Fatal(err)
	}
	if started {
		t.Error("the check started a unit")
	}
	for _, call := range box.calls {
		if strings.Contains(call, "apt-get") && !strings.Contains(call, " -s ") && !strings.HasSuffix(call, "apt-get update") {
			t.Errorf("the check ran %q", call)
		}
	}
}

func TestSystemPackagesJSONCarriesTheDockerUpdate(t *testing.T) {
	encode := func(v any) map[string]any {
		data, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(data, &decoded); err != nil {
			t.Fatal(err)
		}
		return decoded
	}
	keys := func(m map[string]any) []string {
		var out []string
		for k := range m {
			out = append(out, k)
		}
		sort.Strings(out)
		return out
	}

	docker := encode(checkBox(t, ownerBox(t), nil))["docker"].(map[string]any)
	update := docker["update"].(map[string]any)
	if got := keys(update); !reflect.DeepEqual(got, []string{"available", "from", "major_jump", "packages", "plan_id", "refusal", "refusal_detail", "to"}) {
		t.Errorf("update keys = %v", got)
	}
	if update["available"] != true || update["refusal"] != "" || update["from"] != "28.0.4" || update["to"] != "29.8.0" || update["major_jump"] != true {
		t.Errorf("update = %#v", update)
	}
	if detail, ok := update["refusal_detail"].([]any); !ok || len(detail) != 0 {
		t.Errorf("refusal_detail = %#v, want []", update["refusal_detail"])
	}
	packages := update["packages"].([]any)
	if len(packages) != 3 {
		t.Fatalf("packages = %#v", packages)
	}
	first := packages[0].(map[string]any)
	if got := keys(first); !reflect.DeepEqual(got, []string{"candidate_version", "current_version", "name"}) || first["name"] != "containerd.io" {
		t.Errorf("a package = %v %#v", got, first)
	}
	// every field the line had is still there, and nothing else was added to it but the update
	for _, key := range []string{"installed", "origin", "version", "updates", "restarts_docker", "manual_command"} {
		if _, ok := docker[key]; !ok {
			t.Errorf("the Docker line lost %q: %v", key, keys(docker))
		}
	}
	if len(docker) != 7 {
		t.Errorf("the Docker line holds %v", keys(docker))
	}

	// a refusal before the plan: the list is [] and not null, the strings are empty
	refused := ownerBox(t)
	refused.policy = mirrorPolicy
	refusedUpdate := encode(checkBox(t, refused, nil))["docker"].(map[string]any)["update"].(map[string]any)
	if refusedUpdate["refusal"] != "origin" || refusedUpdate["available"] != false || refusedUpdate["plan_id"] != "" {
		t.Errorf("update = %#v", refusedUpdate)
	}
	if list, ok := refusedUpdate["packages"].([]any); !ok || len(list) != 0 {
		t.Errorf("packages = %#v, want []", refusedUpdate["packages"])
	}
	if list, ok := refusedUpdate["refusal_detail"].([]any); !ok || len(list) != 0 {
		t.Errorf("refusal_detail = %#v, want []", refusedUpdate["refusal_detail"])
	}

	// nothing to update: no "update" key at all
	idle := ownerBox(t)
	idle.upgradeSimulation, idle.policy, idle.dockerCE = simLibc, currentPolicy, dockerCE
	idleDocker := encode(checkBox(t, idle, nil))["docker"].(map[string]any)
	if _, there := idleDocker["update"]; there {
		t.Errorf("the Docker line of a box with nothing to update has an update: %#v", idleDocker)
	}
}

func TestTheFakeAptBoxComparesVersionsNumerically(t *testing.T) {
	for _, c := range []struct {
		candidate, installed string
		later                bool
	}{
		{"5:29.10.0-1~u", "5:29.9.0-1~u", true},
		{"5:29.9.0-1~u", "5:29.10.0-1~u", false},
		{"5:29.8.2-1", "5:29.8.1-1", true},
		{"29.8.2", "5:28.0.4", false}, // the epoch first
		{"1:1.0", "9:99", false},
		{"10:1", "9:99", true},
		{"5:28.0.4-1", "5:28.0.4-1", false},
		{"2.1.4-1", "1.7.27-1", true},
	} {
		_, err := (&aptBox{}).command(context.Background(), "dpkg", "--compare-versions", c.candidate, "gt", c.installed)
		if (err == nil) != c.later {
			t.Errorf("%q gt %q = %v, want %v", c.candidate, c.installed, err == nil, c.later)
		}
	}
}

func TestACandidateOf29_10IsNewerThan29_9(t *testing.T) {
	box := ownerBox(t)
	const installed, candidate = "5:29.9.0-1~debian.11~bullseye", "5:29.10.0-1~debian.11~bullseye"
	box.upgradeSimulation = simLibc
	box.dockerCE = installed
	box.policy = strings.NewReplacer(debian28, installed, debian29, candidate).Replace(ownerPolicy)
	box.enginePlans = map[string]string{}
	if got := checkBox(t, box, nil).Docker; got.Candidate != "29.10.0" {
		t.Errorf("candidate = %q: 29.10.0 is later than 29.9.0", got.Candidate)
	}
}

type recordedErrors struct{ messages []string }

func (r *recordedErrors) Errorf(format string, args ...any) {
	r.messages = append(r.messages, fmt.Sprintf(format, args...))
}

func TestAStrictAptBoxFailsOnACommandNobodyAnswers(t *testing.T) {
	for _, call := range [][]string{
		{"df", "-h"},
		{"systemctl", "start", "docker.service"},
		{"/usr/bin/apt-get", "-y", "install", "docker-ce"},
		{"/usr/bin/docker", "version"},
		{"apt-mark", "showhold"},
	} {
		var recorded recordedErrors
		_, err := (&aptBox{strict: &recorded}).command(context.Background(), call[0], call[1:]...)
		if len(recorded.messages) != 1 || err == nil {
			t.Errorf("%v: errors = %v, err = %v", call, recorded.messages, err)
		}
		// without the option it is what it always was: success, no output
		if out, err := (&aptBox{}).command(context.Background(), call[0], call[1:]...); out != nil || err != nil {
			t.Errorf("%v: a box that is not strict answered %q, %v", call, out, err)
		}
	}
	var recorded recordedErrors
	strict := &aptBox{strict: &recorded, dockerCE: debian28}
	for _, call := range [][]string{
		{"systemctl", "show", "x.service", "--property=ActiveState", "--value"},
		{"dpkg-query", "-W", "docker-ce"},
		{"/usr/bin/apt-get", "update"},
		{"/usr/bin/apt-get", "-s", "upgrade"},
		{"/usr/bin/docker", "info"},
		{"/usr/bin/docker", "ps", "-q"},
		{"/usr/bin/docker", "inspect", "abc"},
	} {
		strict.command(context.Background(), call[0], call[1:]...)
	}
	if len(recorded.messages) != 0 {
		t.Errorf("a command the box knows was reported: %v", recorded.messages)
	}
}
