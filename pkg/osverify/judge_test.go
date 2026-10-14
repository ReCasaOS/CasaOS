package osverify

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/osrun"
)

// world is a machine before and after an upgrade, and the backup folder of the hop that went between. It begins as a
// machine that came back as it was, and a scenario spoils it.
type world struct {
	before, after Facts
	// restart are the facts of the machine when the owner asked for its restart: none unless a scenario says
	restart Facts
	bk      BKFiles
}

func clone(t testing.TB, f Facts) Facts {
	t.Helper()
	data, err := json.Marshal(f)
	if err != nil {
		t.Fatal(err)
	}
	var out Facts
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

// copyOf is a copy of facts that a scenario can change without changing the original.
func copyOf(f Facts) Facts {
	data, err := json.Marshal(f)
	if err != nil {
		panic(err)
	}
	var out Facts
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out
}

func newWorld(t testing.TB) *world {
	t.Helper()
	facts := take(t, healthyBox(t))
	// the box that is judged has put Docker's source back: what stays paused is a scenario's business
	facts.Paused = nil
	return &world{before: clone(t, facts), after: clone(t, facts), bk: bkFixture(t)}
}

func (w *world) judge() []Finding {
	return JudgeWith(w.before, w.after, w.bk, Options{Restart: w.restart})
}

// unknown says that a fact was not taken.
func unknown(f *Facts, facts ...string) { f.Unknown = append(f.Unknown, facts...) }

// The findings of a scenario, with the severity spelled out: a test that took it from the table of the code would say the
// same whatever the table says.
func errorF(code, arg string, apps ...string) Finding {
	return Finding{Code: code, Severity: "error", Arg: arg, Apps: apps}
}
func warnF(code, arg string) Finding { return Finding{Code: code, Severity: "warn", Arg: arg} }
func incompleteF(facts string) Finding {
	return Finding{Code: CodeFactsIncomplete, Severity: "warn", Arg: facts}
}
func infoF(code, arg string) Finding { return Finding{Code: code, Severity: "info", Arg: arg} }
func notReturnedF(name, policy string) Finding {
	return Finding{Code: CodeContainerNotReturned, Severity: "warn", Arg: name, Policy: policy}
}

// bkFixture is the backup folder of a hop whose machine came back as it was: what block 2 wrote down, and what block 8
// wrote after the restart.
func bkFixture(t testing.TB) BKFiles {
	t.Helper()
	bk := BKFiles{}
	for _, name := range BKNames {
		data, err := os.ReadFile(filepath.Join("testdata", "bk", name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		bk[name] = strings.ReplaceAll(string(data), "\r\n", "\n")
	}
	return bk
}

type scenario struct {
	name  string
	spoil func(w *world)
	want  []Finding
}

func setUnit(w *world, unit, state string) { w.after.Units[unit] = state }

func addContainer(f *Facts, c Container) {
	f.Docker.Containers = append(f.Docker.Containers, c)
	slices.SortFunc(f.Docker.Containers, func(a, b Container) int { return strings.Compare(a.Name, b.Name) })
}

func without(f *Facts, name string) {
	f.Docker.Containers = slices.DeleteFunc(slices.Clone(f.Docker.Containers), func(c Container) bool { return c.Name == name })
}

var scenarios = []scenario{
	{name: "a machine that came back as it was", spoil: func(w *world) {}},

	// the kernel
	{name: "another kernel runs", spoil: func(w *world) { w.bk[BKKernel] = "6.1.0-26-amd64\n" },
		want: []Finding{errorF(CodeKernelUnexpected, "6.1.0-26-amd64>6.1.0-25-amd64")}},
	{name: "the kernel is not known", spoil: func(w *world) { w.bk[BKKernel] = "6.1.0-26-amd64\n"; unknown(&w.after, FactKernel) },
		want: []Finding{incompleteF("kernel")}},
	{name: "the kernel is not known and was not written", spoil: func(w *world) { delete(w.bk, BKKernel); unknown(&w.after, FactKernel) }},
	{name: "the kernel the upgrade installed is not written", spoil: func(w *world) { delete(w.bk, BKKernel) }},
	{name: "the kernel that is written is not one", spoil: func(w *world) { w.bk[BKKernel] = "the new one\n" }},
	{name: "the kernel file is empty", spoil: func(w *world) { w.bk[BKKernel] = "" }},

	// the units of ReCasaOS
	{name: "the gateway does not run", spoil: func(w *world) { setUnit(w, "casaos-gateway.service", "inactive") },
		want: []Finding{errorF(CodeCasaosUnitDown, "casaos-gateway.service")}},
	{name: "a unit failed", spoil: func(w *world) { setUnit(w, "casaos.service", "failed") }, want: []Finding{errorF(CodeCasaosUnitDown, "casaos.service")}},
	{name: "a unit that keeps being started again", spoil: func(w *world) { setUnit(w, "casaos.service", "activating") }, want: []Finding{errorF(CodeCasaosUnitDown, "casaos.service")}},
	{name: "a unit that reads its configuration again", spoil: func(w *world) { setUnit(w, "casaos.service", "reloading") }},
	{name: "a unit that has no file any more", spoil: func(w *world) { delete(w.after.Units, "casaos-user-service.service") },
		want: []Finding{errorF(CodeCasaosUnitDown, "casaos-user-service.service")}},
	{name: "a unit that could not be asked about", spoil: func(w *world) { setUnit(w, "casaos.service", unitUnknown) }, want: []Finding{incompleteF("units")}},
	{name: "a unit that did not run before and could not be asked about", spoil: func(w *world) {
		w.bk[BKCasaosUnits] += "casaos-extra.timer inactive\n"
		setUnit(w, "casaos-extra.timer", unitUnknown)
	}},
	{name: "two units that do not run, by name", spoil: func(w *world) {
		setUnit(w, "casaos.service", "inactive")
		setUnit(w, "casaos-app-management.service", "failed")
	}, want: []Finding{errorF(CodeCasaosUnitDown, "casaos-app-management.service"), errorF(CodeCasaosUnitDown, "casaos.service")}},
	{name: "a unit that did not run before", spoil: func(w *world) {
		w.bk[BKCasaosUnits] += "casaos-extra.timer inactive\ncasaos-other.service failed\n"
	}},
	{name: "the units are not known", spoil: func(w *world) { unknown(&w.after, FactUnits); w.after.Units = nil }, want: []Finding{incompleteF("units")}},
	{name: "the units are not known and block 2 wrote none", spoil: func(w *world) { unknown(&w.after, FactUnits); w.after.Units = nil; delete(w.bk, BKCasaosUnits) }},
	{name: "the unit of the upgrade itself, which is gone", spoil: func(w *world) {
		w.bk[BKCasaosUnits] += "casaos-update.service active\ncasaos-package-update.service active\ncasaos-docker-update.service active\n"
	}},
	{name: "a line of the units that is not a unit and a state", spoil: func(w *world) {
		w.bk[BKCasaosUnits] += "casaos-x.service\n\n  \nthree words here\nwhat; active\n"
	}},

	// Docker
	{name: "Docker does not run", spoil: func(w *world) { w.after.Docker = Docker{State: DockerStopped} }, want: []Finding{errorF(CodeDockerDown, "stopped")}},
	{name: "Docker's daemon does not answer", spoil: func(w *world) { w.after.Docker = Docker{State: DockerDown} }, want: []Finding{errorF(CodeDockerDown, "down")}},
	{name: "Docker is gone", spoil: func(w *world) { w.after.Docker = Docker{State: DockerAbsent} }, want: []Finding{errorF(CodeDockerDown, "absent")}},
	{name: "Docker did not run before", spoil: func(w *world) {
		w.before.Docker = Docker{State: DockerStopped}
		w.after.Docker = Docker{State: DockerStopped}
	}},
	{name: "Docker is not known now", spoil: func(w *world) { w.after.Docker = Docker{}; unknown(&w.after, FactDocker, FactContainers) }, want: []Finding{incompleteF("docker")}},
	{name: "Docker is not known now and did not run before", spoil: func(w *world) {
		w.before.Docker = Docker{State: DockerStopped}
		w.bk[BKContainers] = ""
		w.after.Docker = Docker{}
		unknown(&w.after, FactDocker, FactContainers)
	}},
	{name: "Docker was not known before, and the containers that block 2 listed ran", spoil: func(w *world) {
		w.before.Docker = Docker{}
		unknown(&w.before, FactDocker, FactContainers)
		w.after.Docker = Docker{State: DockerDown}
	}, want: []Finding{errorF(CodeDockerDown, "down")}},
	{name: "Docker was not known before, and block 2 listed no container that ran", spoil: func(w *world) {
		w.before.Docker = Docker{}
		unknown(&w.before, FactDocker, FactContainers)
		w.bk[BKContainers] = "/halted policy=always state=exited\n"
		w.after.Docker = Docker{State: DockerDown}
	}},
	{name: "Docker was not known before and the facts of before say nothing of the containers", spoil: func(w *world) {
		w.before = Facts{}
		w.bk[BKContainers] = "/web policy=always state=running\n"
		w.after.Docker = Docker{State: DockerStopped}
	}, want: []Finding{errorF(CodeDockerDown, "stopped")}},
	{name: "Docker is another version", spoil: func(w *world) { w.after.Docker.Version = "25.0.1" },
		want: []Finding{warnF(CodeDockerVersionChanged, "24.0.7>25.0.1")}},
	{name: "Docker is the same version in another engine", spoil: func(w *world) { w.after.Docker.EngineID = "ffffffff-6c0e-4f6c-9a46-5d3d1f0f1c11" },
		want: []Finding{warnF(CodeDockerVersionChanged, "engine_id")}},
	{name: "Docker's version and engine both changed", spoil: func(w *world) {
		w.after.Docker.Version = "25.0.1"
		w.after.Docker.EngineID = "ffffffff-6c0e-4f6c-9a46-5d3d1f0f1c11"
	}, want: []Finding{warnF(CodeDockerVersionChanged, "24.0.7>25.0.1")}},
	{name: "Docker's engine id was not readable", spoil: func(w *world) {
		w.before.Docker.EngineID = ""
		w.after.Docker.EngineID = "ffffffff-6c0e-4f6c-9a46-5d3d1f0f1c11"
	}},
	{name: "Docker's version is not known after", spoil: func(w *world) { w.after.Docker.Version = "" }},
	{name: "Docker's version is not compared when it is not up", spoil: func(w *world) {
		w.after.Docker = Docker{State: DockerDown, Version: "25.0.1"}
	}, want: []Finding{errorF(CodeDockerDown, "down")}},

	// the pools
	{name: "a pool is not mounted", spoil: func(w *world) { w.after.Mergerfs = []string{"/media/Media Library"} },
		want: []Finding{errorF(CodeMountsMissing, "/DATA/pool", "jellyfin")}},
	{name: "a pool with a space in its name is not mounted", spoil: func(w *world) { w.after.Mergerfs = []string{"/DATA/pool"} },
		want: []Finding{errorF(CodeMountsMissing, "/media/Media Library", "scratch")}},
	{name: "no pool is mounted", spoil: func(w *world) { w.after.Mergerfs = nil },
		want: []Finding{errorF(CodeMountsMissing, "/DATA/pool", "jellyfin"), errorF(CodeMountsMissing, "/media/Media Library", "scratch")}},
	{name: "the apps of a pool are the ones under it, and not the ones beside it", spoil: func(w *world) {
		addContainer(&w.before, Container{Name: "beside", Policy: "always", Binds: []string{"/DATA/pool2/x", "/DATA/poolish"}})
		addContainer(&w.before, Container{Name: "exact", Policy: "always", Binds: []string{"/DATA/pool"}})
		addContainer(&w.before, Container{Name: "deep", Policy: "no", Binds: []string{"/elsewhere", "/DATA/pool/a/b/c"}})
		w.after.Mergerfs = []string{"/media/Media Library"}
	}, want: []Finding{errorF(CodeMountsMissing, "/DATA/pool", "deep", "exact", "jellyfin")}},
	{name: "an app that has the same pool twice is named once", spoil: func(w *world) {
		addContainer(&w.before, Container{Name: "twice", Policy: "no", Binds: []string{"/DATA/pool/a", "/DATA/pool/b"}})
		w.after.Mergerfs = []string{"/media/Media Library"}
	}, want: []Finding{errorF(CodeMountsMissing, "/DATA/pool", "jellyfin", "twice")}},
	{name: "a pool is lost and the containers of before are not known", spoil: func(w *world) {
		w.before.Docker.Containers = nil
		unknown(&w.before, FactContainers)
		w.after.Mergerfs = []string{"/media/Media Library"}
	}, want: []Finding{errorF(CodeMountsMissing, "/DATA/pool")}},
	{name: "a pool that is new", spoil: func(w *world) { w.after.Mergerfs = append(w.after.Mergerfs, "/new") }},
	{name: "the pools are not known after", spoil: func(w *world) { w.after.Mergerfs = nil; unknown(&w.after, FactMounts) }, want: []Finding{incompleteF("mounts")}},
	{name: "the pools were not known before", spoil: func(w *world) { w.before.Mergerfs = nil; unknown(&w.before, FactMounts); w.after.Mergerfs = nil }},

	// dpkg and systemd
	{name: "dpkg is not quiet", spoil: func(w *world) { w.after.DpkgQuiet = false }, want: []Finding{errorF(CodeDpkgNotQuiet, "")}},
	{name: "dpkg was not quiet before and is now", spoil: func(w *world) { w.before.DpkgQuiet = false }},
	{name: "dpkg could not be asked", spoil: func(w *world) { w.after.DpkgQuiet = false; unknown(&w.after, FactDpkg) }, want: []Finding{incompleteF("dpkg")}},
	{name: "the system is still starting", spoil: func(w *world) { w.after.System = "starting" }, want: []Finding{errorF(CodeSystemNotReady, "starting")}},
	{name: "the system is in maintenance", spoil: func(w *world) { w.after.System = "maintenance" }, want: []Finding{errorF(CodeSystemNotReady, "maintenance")}},
	{name: "the system is stopping", spoil: func(w *world) { w.after.System = "stopping" }, want: []Finding{errorF(CodeSystemNotReady, "stopping")}},
	{name: "the system has started with a unit that failed", spoil: func(w *world) { w.after.System = "degraded" }},
	{name: "the system is not known", spoil: func(w *world) { w.after.System = ""; unknown(&w.after, FactSystem) }, want: []Finding{incompleteF("system")}},

	// the lists that the blocks wrote
	{name: "a unit fails that did not", spoil: func(w *world) { w.after.Failed = []string{"smartd.service", "wsdd.service"} },
		want: []Finding{warnF(CodeUnitsFailedNew, "wsdd.service")}},
	{name: "a unit fails that did not, in the list that block 8 wrote, when the machine cannot be asked", spoil: func(w *world) {
		w.after.Failed = nil
		unknown(&w.after, FactFailed)
		w.bk[BKFailedUnitsAfter] = "smartd.service\nwsdd.service\n"
	}, want: []Finding{warnF(CodeUnitsFailedNew, "wsdd.service"), incompleteF("failed_units")}},
	{name: "the machine's own list wins over the one that block 8 wrote", spoil: func(w *world) {
		w.after.Failed = []string{"smartd.service"}
		w.bk[BKFailedUnitsAfter] = "smartd.service\nwsdd.service\n"
	}},
	{name: "two units fail that did not, by name, once each", spoil: func(w *world) {
		w.after.Failed = []string{"wsdd.service", "smartd.service", "apt-daily.service", "wsdd.service"}
	}, want: []Finding{warnF(CodeUnitsFailedNew, "apt-daily.service"), warnF(CodeUnitsFailedNew, "wsdd.service")}},
	{name: "a unit that failed before does not fail now", spoil: func(w *world) { w.after.Failed = nil }},
	{name: "the units that failed now are not written, and the machine cannot say", spoil: func(w *world) {
		delete(w.bk, BKFailedUnitsAfter)
		w.after.Failed = nil
		unknown(&w.after, FactFailed)
	}, want: []Finding{incompleteF("failed_units")}},
	{name: "the units that failed before are not written", spoil: func(w *world) { delete(w.bk, BKFailedUnits); w.after.Failed = []string{"wsdd.service"} }},
	{name: "a line of failed units that is not a unit", spoil: func(w *world) {
		w.after.Failed = nil
		unknown(&w.after, FactFailed)
		w.bk[BKFailedUnitsAfter] = "smartd.service\n●\nnot a unit!\n-x.service\n"
	}, want: []Finding{incompleteF("failed_units")}},

	{name: "a port is not listened on", spoil: func(w *world) {
		w.after.Listening = slices.DeleteFunc(slices.Clone(w.after.Listening), func(a string) bool { return a == "0.0.0.0:8096" })
	},
		want: []Finding{warnF(CodePortsMissing, "8096")}},
	{name: "ports, in the order of their numbers", spoil: func(w *world) {
		w.after.Listening = []string{"0.0.0.0:22", "0.0.0.0:139", "[::]:22", "127.0.0.1:631", "127.0.0.53:53"}
	}, want: []Finding{warnF(CodePortsMissing, "80"), warnF(CodePortsMissing, "445"), warnF(CodePortsMissing, "8096")}},
	{name: "a port that is listened on at one address of two", spoil: func(w *world) {
		w.after.Listening = slices.DeleteFunc(slices.Clone(w.after.Listening), func(a string) bool { return a == "[::]:22" })
	}},
	{name: "a port that moved to another address", spoil: func(w *world) {
		w.after.Listening = slices.DeleteFunc(slices.Clone(w.after.Listening), func(a string) bool { return a == "0.0.0.0:80" })
		w.after.Listening = append(w.after.Listening, "127.0.0.1:80")
	}},
	{name: "a port that the kernel handed out", spoil: func(w *world) {
		w.after.Listening = slices.DeleteFunc(slices.Clone(w.after.Listening), func(a string) bool { return a == "127.0.0.1:35827" })
	}},
	{name: "the last port of the ones that a service asks for, and the first of the ones that the kernel hands out", spoil: func(w *world) {
		w.bk[BKListening] += "0.0.0.0:32767\n0.0.0.0:32768\n"
	}, want: []Finding{warnF(CodePortsMissing, "32767")}},
	{name: "ports of an older ss", spoil: func(w *world) {
		w.bk[BKListening] = "*:22\n*:80\n"
		w.after.Listening = []string{"0.0.0.0:22", "[::]:22"}
	}, want: []Finding{warnF(CodePortsMissing, "80")}},
	{name: "lines of the ports that are not ports", spoil: func(w *world) {
		w.bk[BKListening] += "garbage\n:\n0.0.0.0:0\n0.0.0.0:99999\n0.0.0.0:x\n"
	}},
	{name: "a new port", spoil: func(w *world) { w.after.Listening = append(w.after.Listening, "0.0.0.0:9000") }},
	{name: "a port that is gone, in the list that block 8 wrote, when the machine cannot be asked", spoil: func(w *world) {
		w.after.Listening = nil
		unknown(&w.after, FactListening)
		w.bk[BKListeningAfter] = "0.0.0.0:22\n0.0.0.0:445\n0.0.0.0:139\n0.0.0.0:8096\n"
	}, want: []Finding{warnF(CodePortsMissing, "53"), warnF(CodePortsMissing, "80"), warnF(CodePortsMissing, "631"), incompleteF("listening")}},
	{name: "the ports now are not written, and the machine cannot say", spoil: func(w *world) {
		delete(w.bk, BKListeningAfter)
		w.after.Listening = nil
		unknown(&w.after, FactListening)
	}, want: []Finding{incompleteF("listening")}},
	{name: "the ports before are not written", spoil: func(w *world) { delete(w.bk, BKListening) }},

	// Samba, the network
	{name: "Samba has fewer users", spoil: func(w *world) { w.after.SambaUsers = 3 }, want: []Finding{warnF(CodeSambaUsersChanged, "4>3")}},
	{name: "Samba has more users", spoil: func(w *world) { w.after.SambaUsers = 5 }, want: []Finding{warnF(CodeSambaUsersChanged, "4>5")}},
	{name: "Samba had no user and has none", spoil: func(w *world) { w.before.SambaUsers, w.after.SambaUsers = 0, 0 }},
	{name: "Samba's users cannot be counted now", spoil: func(w *world) { w.after.SambaUsers = 0; unknown(&w.after, FactSamba) }, want: []Finding{incompleteF("samba")}},
	{name: "Samba's users could not be counted before", spoil: func(w *world) { w.before.SambaUsers = 0; unknown(&w.before, FactSamba); w.after.SambaUsers = 3 }},
	{name: "Samba is not on the box", spoil: func(w *world) {
		w.before.SambaUsers, w.after.SambaUsers = 0, 0
		unknown(&w.before, FactSamba)
		unknown(&w.after, FactSamba)
	}},
	{name: "the machine has no route out", spoil: func(w *world) { w.after.Route = "" }, want: []Finding{warnF(CodeRouteMissing, "eth0")}},
	{name: "the machine goes out by another card", spoil: func(w *world) { w.after.Route = "ens18" }},
	{name: "the machine had no route", spoil: func(w *world) { w.before.Route, w.after.Route = "", "" }},
	{name: "the route is not known now", spoil: func(w *world) { w.after.Route = ""; unknown(&w.after, FactRoute) }, want: []Finding{incompleteF("route")}},
	{name: "the route was not known before", spoil: func(w *world) { w.before.Route = ""; unknown(&w.before, FactRoute); w.after.Route = "" }},
	{name: "the resolver no longer answers", spoil: func(w *world) { w.after.DNS = false }, want: []Finding{warnF(CodeDNSMissing, "")}},
	{name: "the resolver did not answer before", spoil: func(w *world) { w.before.DNS, w.after.DNS = false, false }},
	{name: "the resolver is not known now", spoil: func(w *world) { w.after.DNS = false; unknown(&w.after, FactDNS) }, want: []Finding{incompleteF("dns")}},
	{name: "the resolver was not known before", spoil: func(w *world) { w.before.DNS = false; unknown(&w.before, FactDNS); w.after.DNS = false }},

	// the warnings of block 7
	{name: "block 7 warned of a small initrd", spoil: func(w *world) { w.bk[BKWarnings] = "initrd_small\n" }, want: []Finding{infoF(CodePrerebootWarning, "initrd_small")}},
	{name: "block 7 warned twice of one thing and of another", spoil: func(w *world) { w.bk[BKWarnings] = "grub_cfg_missing\ninitrd_small\ngrub_cfg_missing\n" },
		want: []Finding{infoF(CodePrerebootWarning, "grub_cfg_missing"), infoF(CodePrerebootWarning, "initrd_small")}},
	{name: "a warning that is not a word", spoil: func(w *world) { w.bk[BKWarnings] = "Not a word\n; rm -rf /\n\n" }},
	{name: "no warnings", spoil: func(w *world) { delete(w.bk, BKWarnings) }},

	// Docker's source
	{name: "Docker's repository could not be reached", spoil: func(w *world) { w.bk[BKDockerSourceNote] = "unreachable\n" },
		want: []Finding{warnF(CodeDockerSourceUnreachable, "unreachable")}},
	{name: "curl is missing for Docker's repository", spoil: func(w *world) { w.bk[BKDockerSourceNote] = "no_curl\n" },
		want: []Finding{warnF(CodeDockerSourceUnreachable, "no_curl")}},
	{name: "Docker publishes nothing for the release yet", spoil: func(w *world) { w.bk[BKDockerSourceNote] = "not_published\n" },
		want: []Finding{infoF(CodeDockerSourceNotPublished, "")}},
	{name: "a note that says something else", spoil: func(w *world) { w.bk[BKDockerSourceNote] = "fine\n" }},
	{name: "an empty note", spoil: func(w *world) { w.bk[BKDockerSourceNote] = "" }},
	{name: "no note", spoil: func(w *world) { delete(w.bk, BKDockerSourceNote) }},
	{name: "Docker's source is still paused", spoil: func(w *world) { w.after.Paused = []string{"docker.list.casaos-paused"} },
		want: []Finding{infoF(CodeDockerSourcePaused, "")}},
	{name: "the other sources that the upgrade paused are still paused", spoil: func(w *world) {
		w.after.Paused = []string{"docker.list.casaos-paused", "plex.list.casaos-paused", "sources.list:12"}
	}, want: []Finding{infoF(CodeDockerSourcePaused, ""), infoF(CodeSourcesLeftPaused, "plex.list.casaos-paused"), infoF(CodeSourcesLeftPaused, "sources.list:12")}},
	{name: "what is paused is not known", spoil: func(w *world) { w.after.Paused = []string{"docker.list.casaos-paused"}; unknown(&w.after, FactSources) }, want: []Finding{incompleteF("sources")}},

	// the containers
	{name: "a container that comes back by itself did not", spoil: func(w *world) { without(&w.after, "pihole") },
		want: []Finding{notReturnedF("pihole", "always")}},
	{name: "containers of every policy that come back by themselves, by name", spoil: func(w *world) {
		without(&w.after, "db")
		without(&w.after, "jellyfin")
		without(&w.after, "pihole")
	}, want: []Finding{notReturnedF("db", "on-failure"), notReturnedF("jellyfin", "unless-stopped"), notReturnedF("pihole", "always")}},
	{name: "a container that does not start by itself did not", spoil: func(w *world) { without(&w.after, "scratch") }},
	{name: "a container with no policy at all did not", spoil: func(w *world) {
		w.bk[BKContainers] += "/bare policy= state=running\n"
	}},
	{name: "a container that did not run before", spoil: func(w *world) {
		w.bk[BKContainers] += "/never policy=always state=created\n/halted policy=always state=exited\n"
	}},
	{name: "a container that was restarting", spoil: func(w *world) { w.bk[BKContainers] += "/looping policy=always state=restarting\n" }},
	{name: "a container that is new", spoil: func(w *world) { addContainer(&w.after, Container{Name: "fresh", Policy: "always"}) }},
	{name: "Docker's own error in the list of containers", spoil: func(w *world) {
		w.bk[BKContainers] += "Error response from daemon: No such container: gone\n"
	}},
	{name: "the containers cannot be listed now", spoil: func(w *world) { w.after.Docker.Containers = nil; unknown(&w.after, FactContainers) }, want: []Finding{incompleteF("containers")}},
	{name: "no container is listed and Docker is down", spoil: func(w *world) { w.after.Docker = Docker{State: DockerDown}; unknown(&w.after, FactContainers) },
		want: []Finding{errorF(CodeDockerDown, "down")}},
	{name: "the containers before are not written", spoil: func(w *world) { delete(w.bk, BKContainers); without(&w.after, "pihole") }},

	// what could not be taken, said together
	{name: "several facts that could not be taken, by name", spoil: func(w *world) {
		w.after.SambaUsers, w.after.Route = 0, ""
		unknown(&w.after, FactSamba, FactRoute, FactDNS)
	}, want: []Finding{incompleteF("dns,route,samba")}},
	{name: "a file of the backup folder that could not be read", spoil: func(w *world) { unknown(&w.after, UnreadablePrefix+"containers.txt") },
		want: []Finding{incompleteF("bk:containers.txt")}},
	{name: "facts of after that were not taken at all", spoil: func(w *world) { w.after = Facts{} }, want: []Finding{incompleteF("all")}},

	// the mounts that apps and pools stand on
	{name: "a pool that lost a branch", spoil: func(w *world) {
		w.after.Pools = map[string][]string{"/DATA/pool": {"/mnt/disk1"}, "/media/Media Library": {"/mnt/disk3"}}
		w.after.Mounts = slices.DeleteFunc(slices.Clone(w.after.Mounts), func(m string) bool { return m == "/mnt/disk2" })
	}, want: []Finding{errorF(CodeMountsMissing, "/mnt/disk2", "jellyfin")}},
	{name: "a pool that lost a branch that was not a mount of its own", spoil: func(w *world) {
		w.before.Pools["/DATA/pool"] = []string{"/mnt/disk1", "/mnt/disk2", "/mnt/disk1/extra"}
		w.after.Pools = map[string][]string{"/DATA/pool": {"/mnt/disk1", "/mnt/disk2"}, "/media/Media Library": {"/mnt/disk3"}}
	}, want: []Finding{errorF(CodeMountsMissing, "/mnt/disk1/extra", "jellyfin")}},
	{name: "a pool that gained a branch", spoil: func(w *world) {
		w.after.Pools = map[string][]string{"/DATA/pool": {"/mnt/disk1", "/mnt/disk2", "/mnt/disk4"}, "/media/Media Library": {"/mnt/disk3"}}
	}},
	{name: "a branch that is not mounted any more, with the pool still up", spoil: func(w *world) {
		w.after.Mounts = slices.DeleteFunc(slices.Clone(w.after.Mounts), func(m string) bool { return m == "/mnt/disk3" })
	}, want: []Finding{errorF(CodeMountsMissing, "/mnt/disk3", "scratch")}},
	{name: "a disk that an app has a directory on is not mounted", spoil: func(w *world) {
		w.before.Mounts = append(w.before.Mounts, "/mnt/photos")
		slices.Sort(w.before.Mounts)
		addContainer(&w.before, Container{Name: "photos", Policy: "always", Binds: []string{"/mnt/photos/library"}})
	}, want: []Finding{errorF(CodeMountsMissing, "/mnt/photos", "photos")}},
	{name: "a mount that an app has a directory above is not mounted", spoil: func(w *world) {
		w.before.Mounts = append(w.before.Mounts, "/mnt/photos/library/2024")
		slices.Sort(w.before.Mounts)
		addContainer(&w.before, Container{Name: "photos", Policy: "always", Binds: []string{"/mnt/photos/library"}})
	}, want: []Finding{errorF(CodeMountsMissing, "/mnt/photos/library/2024", "photos")}},
	{name: "a disk that no pool and no app uses is not mounted", spoil: func(w *world) {
		w.before.Mounts = append(w.before.Mounts, "/mnt/usb")
		slices.Sort(w.before.Mounts)
	}},
	{name: "the root is not missed", spoil: func(w *world) {
		w.after.Mounts = slices.DeleteFunc(slices.Clone(w.after.Mounts), func(m string) bool { return m == "/" })
	}},
	{name: "a mount that the facts of before did not know of", spoil: func(w *world) { w.after.Mounts = append(w.after.Mounts, "/mnt/new") }},
	{name: "the mounts were not known before", spoil: func(w *world) { w.before.Mounts, w.before.Pools = nil, nil }},
	{name: "a pool and its branch and the app under it, named once each", spoil: func(w *world) {
		w.after.Mergerfs = []string{"/media/Media Library"}
		w.after.Pools = map[string][]string{"/media/Media Library": {"/mnt/disk3"}}
		w.after.Mounts = []string{"/", "/media/Media Library", "/mnt/disk3"}
	}, want: []Finding{errorF(CodeMountsMissing, "/DATA/pool", "jellyfin"), errorF(CodeMountsMissing, "/mnt/disk1", "jellyfin"), errorF(CodeMountsMissing, "/mnt/disk2", "jellyfin")}},

	// what the owner changed between the hop and the restart
	{name: "an app that the owner stopped before the restart", spoil: func(w *world) {
		without(&w.after, "pihole")
		w.restart = copyOf(w.before)
		without(&w.restart, "pihole")
	}, want: []Finding{infoF(CodeChangedSinceSnapshot, "container:pihole")}},
	{name: "an app that was running when the restart was asked for and did not come back", spoil: func(w *world) {
		without(&w.after, "pihole")
		w.restart = copyOf(w.before)
	}, want: []Finding{notReturnedF("pihole", "always")}},
	{name: "an app that the owner stopped, with the facts of the restart not taken", spoil: func(w *world) { without(&w.after, "pihole") },
		want: []Finding{notReturnedF("pihole", "always")}},
	{name: "an app that the owner stopped, when Docker could not be asked at the restart", spoil: func(w *world) {
		without(&w.after, "pihole")
		w.restart = copyOf(w.before)
		without(&w.restart, "pihole")
		unknown(&w.restart, FactContainers)
	}, want: []Finding{notReturnedF("pihole", "always")}},
	{name: "an app with no restart policy that the owner stopped", spoil: func(w *world) {
		without(&w.after, "scratch")
		w.restart = copyOf(w.before)
		without(&w.restart, "scratch")
	}},
	{name: "a unit that the owner stopped before the restart", spoil: func(w *world) {
		setUnit(w, "casaos-gateway.service", "inactive")
		w.restart = copyOf(w.before)
		w.restart.Units["casaos-gateway.service"] = "inactive"
	}, want: []Finding{infoF(CodeChangedSinceSnapshot, "unit:casaos-gateway.service")}},
	{name: "a unit that had failed by the time the restart was asked for", spoil: func(w *world) {
		setUnit(w, "casaos-gateway.service", "failed")
		w.restart = copyOf(w.before)
		w.restart.Units["casaos-gateway.service"] = "failed"
	}, want: []Finding{errorF(CodeCasaosUnitDown, "casaos-gateway.service")}},
	{name: "a unit that ran when the restart was asked for", spoil: func(w *world) {
		setUnit(w, "casaos-gateway.service", "inactive")
		w.restart = copyOf(w.before)
	}, want: []Finding{errorF(CodeCasaosUnitDown, "casaos-gateway.service")}},

	// the files that dpkg leaves
	{name: "dpkg left new files", spoil: func(w *world) {
		w.bk[BKDpkgFilesAfter] += "/etc/ssh/sshd_config.dpkg-dist\n/etc/samba/smb.conf.dpkg-dist\n/etc/ssh/sshd_config.dpkg-dist\n"
	}, want: []Finding{infoF(CodeDpkgDistNew, "/etc/samba/smb.conf.dpkg-dist"), infoF(CodeDpkgDistNew, "/etc/ssh/sshd_config.dpkg-dist")}},
	{name: "a file of dpkg that is not there any more", spoil: func(w *world) { w.bk[BKDpkgFilesAfter] = "" }},
	{name: "the files of dpkg are not written after", spoil: func(w *world) { delete(w.bk, BKDpkgFilesAfter) }},
	{name: "the files of dpkg are not written before", spoil: func(w *world) { delete(w.bk, BKDpkgFiles); w.bk[BKDpkgFilesAfter] += "/etc/x.dpkg-dist\n" }},
}

func TestTheJudgeOfAMachineThatCameBackAsItWasHasNothingToSay(t *testing.T) {
	w := newWorld(t)
	got := w.judge()
	if got == nil || len(got) != 0 {
		t.Errorf("findings = %#v, want an empty list that is not nil", got)
	}
	// and the files of the fixture are the ones of that machine: it ran the kernel that was written, and the gateway was up
	if !strings.Contains(w.bk[BKCasaosUnits], "casaos-gateway.service active") || strings.TrimSpace(w.bk[BKKernel]) != w.after.Kernel {
		t.Errorf("the fixture is not of a machine that came back as it was")
	}
}

func TestWhatTheJudgeFindsInEachScenario(t *testing.T) {
	for _, tc := range scenarios {
		w := newWorld(t)
		tc.spoil(w)
		got := w.judge()
		want := tc.want
		if want == nil {
			want = []Finding{}
		}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s:\n got  %+v\n want %+v", tc.name, got, want)
		}
	}
}

func TestEveryFindingCodeIsMadeBySomeScenario(t *testing.T) {
	made := map[string]bool{}
	for _, tc := range scenarios {
		for _, f := range tc.want {
			made[f.Code] = true
		}
	}
	for _, code := range Codes {
		if !made[code] {
			t.Errorf("no scenario makes %s", code)
		}
	}
	for code := range made {
		if !slices.Contains(Codes, code) {
			t.Errorf("a scenario makes %s, which is not a code", code)
		}
	}
}

func TestTheFindingsAreAClosedListWithTheirSeverity(t *testing.T) {
	want := map[string]string{
		"kernel_unexpected": "error", "casaos_unit_down": "error", "docker_down": "error", "mounts_missing": "error", "dpkg_not_quiet": "error", "system_not_ready": "error",
		"units_failed_new": "warn", "ports_missing": "warn", "samba_users_changed": "warn", "route_missing": "warn", "dns_missing": "warn",
		"docker_version_changed": "warn", "docker_source_unreachable": "warn", "container_not_returned": "warn", "facts_incomplete": "warn",
		"docker_source_not_published": "info", "docker_source_paused": "info", "dpkg_dist_new": "info", "sources_left_paused": "info", "changed_since_snapshot": "info", "prereboot_warning": "info",
	}
	if len(Codes) != len(want) {
		t.Errorf("%d codes, want %d", len(Codes), len(want))
	}
	rank := map[string]int{"error": 0, "warn": 1, "info": 2}
	last := 0
	seen := map[string]bool{}
	for _, code := range Codes {
		if seen[code] {
			t.Errorf("%s is listed twice", code)
		}
		seen[code] = true
		if SeverityOf(code) != want[code] || want[code] == "" {
			t.Errorf("%s is %q, want %q", code, SeverityOf(code), want[code])
		}
		if rank[want[code]] < last {
			t.Errorf("%s is listed after a finding that is less serious", code)
		}
		last = rank[want[code]]
	}
	if SeverityOf("made_up") != "" || SeverityOf("") != "" {
		t.Error("a code that is not one has a severity")
	}
}

func TestOnlyErrorsAndWarningsChangeWhatARunEndsAs(t *testing.T) {
	// a machine that keeps Docker's source paused, with files that dpkg left, and other sources paused, is a machine that
	// succeeded
	w := newWorld(t)
	w.after.Paused = []string{"docker.list.casaos-paused", "plex.list.casaos-paused"}
	w.bk[BKDockerSourceNote] = "not_published\n"
	w.bk[BKDpkgFilesAfter] += "/etc/ssh/sshd_config.dpkg-dist\n"
	got := w.judge()
	if len(got) != 4 {
		t.Fatalf("findings = %+v", got)
	}
	for _, f := range got {
		if f.Severity != "info" {
			t.Errorf("%+v is not information", f)
		}
	}
	if result := Summarize(got); result != (osrun.VerifyResult{}) {
		t.Errorf("information counts as %+v", result)
	}

	mixed := []Finding{errorF(CodeDockerDown, "down"), warnF(CodePortsMissing, "80"), warnF(CodeDNSMissing, ""), infoF(CodeDpkgDistNew, "/x"), errorF(CodeMountsMissing, "/p"), infoF(CodeDockerSourcePaused, "")}
	if result := Summarize(mixed); result != (osrun.VerifyResult{Errors: 2, Warnings: 2}) {
		t.Errorf("Summarize = %+v, want 2 errors and 2 warnings", result)
	}
	if result := Summarize(nil); result != (osrun.VerifyResult{}) {
		t.Errorf("Summarize(nil) = %+v", result)
	}
}

func TestTheFindingsAreListedByTheirSeverityAndThenByTheirCode(t *testing.T) {
	w := newWorld(t)
	w.after.Paused = []string{"docker.list.casaos-paused"}
	w.after.Docker.Version = "25.0.1"
	w.after.DpkgQuiet = false
	w.after.Route = ""
	w.after.Failed = []string{"smartd.service", "wsdd.service"}
	w.bk[BKDpkgFilesAfter] += "/etc/x.dpkg-dist\n"
	setUnit(w, "casaos.service", "failed")
	var codes []string
	for _, f := range w.judge() {
		codes = append(codes, f.Code)
	}
	want := []string{CodeCasaosUnitDown, CodeDpkgNotQuiet, CodeUnitsFailedNew, CodeRouteMissing, CodeDockerVersionChanged, CodeDockerSourcePaused, CodeDpkgDistNew}
	if !slices.Equal(codes, want) {
		t.Errorf("codes = %v, want %v", codes, want)
	}
}

func TestTheJudgeDoesNotSayWhatItDoesNotKnow(t *testing.T) {
	// facts that were lost, and nothing written: nothing to compare with, and nothing to conclude but that nothing was taken
	if got := Judge(Facts{}, Facts{}, nil); !reflect.DeepEqual(got, []Finding{incompleteF("all")}) {
		t.Errorf("no facts: %#v", got)
	}
	// the facts of before lost: what the machine is, by itself, is still judged
	w := newWorld(t)
	w.before = Facts{}
	w.after.DpkgQuiet = false
	w.after.System = "starting"
	w.after.Mergerfs = nil
	w.after.Route = ""
	w.after.SambaUsers = 0
	w.after.DNS = false
	w.after.Docker = Docker{State: DockerDown}
	var codes []string
	for _, f := range w.judge() {
		codes = append(codes, f.Code)
	}
	// (Docker is said down on the list that block 2 wrote: it had containers that ran)
	if want := []string{CodeDockerDown, CodeDpkgNotQuiet, CodeSystemNotReady}; !slices.Equal(codes, want) {
		t.Errorf("without the facts of before: %v, want %v", codes, want)
	}
	// the facts of after that were not taken are not a machine that is down, and are not a machine that is well
	w = newWorld(t)
	w.after = Facts{}
	if got := w.judge(); !reflect.DeepEqual(got, []Finding{incompleteF("all")}) {
		t.Errorf("no facts of after: %+v", got)
	}
}

func TestWhatComesFromTheBoxIsMadeIntoTextThatCanBeShown(t *testing.T) {
	w := newWorld(t)
	w.bk[BKDpkgFilesAfter] += "/etc/\x1b[31mred\x00.dpkg-dist\n/etc/" + strings.Repeat("é", 300) + ".dpkg-dist\n"
	w.after.Paused = []string{"a\nb.list.casaos-paused"}
	got := w.judge()
	for _, f := range got {
		if strings.ContainsFunc(f.Arg, func(r rune) bool { return r < ' ' || r == 0x7f || r == '\ufffd' }) {
			t.Errorf("an argument with a character that is not text: %q", f.Arg)
		}
		if len(f.Arg) > 200 {
			t.Errorf("an argument of %d bytes", len(f.Arg))
		}
	}
	if len(got) != 3 {
		t.Errorf("findings = %+v", got)
	}
}

func TestAMachineThatLostAHundredPortsSaysSoOnce(t *testing.T) {
	w := newWorld(t)
	var before strings.Builder
	var files strings.Builder
	for i := 1; i <= 150; i++ {
		before.WriteString("0.0.0.0:" + strconv.Itoa(i) + "\n")
		files.WriteString("/etc/f" + strconv.Itoa(i) + ".dpkg-dist\n")
	}
	w.bk[BKListening] = before.String()
	w.bk[BKListeningAfter] = ""
	w.bk[BKDpkgFilesAfter] += files.String()
	counts := map[string]int{}
	for _, f := range w.judge() {
		counts[f.Code]++
	}
	if counts[CodePortsMissing] != maxPerCode || counts[CodeDpkgDistNew] != maxPerCode {
		t.Errorf("counts = %v, want %d of each", counts, maxPerCode)
	}
}

func TestTheContainersThatDidNotReturnAreListedWithTheirPolicy(t *testing.T) {
	w := newWorld(t)
	without(&w.after, "pihole")
	without(&w.after, "scratch")
	without(&w.after, "db")
	w.bk[BKContainers] += "/bare policy= state=running\n/pihole policy=always state=running\nError: x\n"
	got := NotReturned(w.bk, w.after)
	want := []dockerpkg.NotReturned{{Name: "bare", RestartPolicy: ""}, {Name: "db", RestartPolicy: "on-failure"}, {Name: "pihole", RestartPolicy: "always"}, {Name: "scratch", RestartPolicy: "no"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("not returned = %+v, want %+v", got, want)
	}
	// and never more of them than the ones that were running
	if got := NotReturned(BKFiles{}, w.after); got == nil || len(got) != 0 {
		t.Errorf("without the file: %#v", got)
	}
	w.after.Docker.State = DockerDown
	if got := NotReturned(w.bk, w.after); got == nil || len(got) != 0 {
		t.Errorf("with Docker down: %#v", got)
	}
	w.after.Docker.State = DockerUp
	unknown(&w.after, FactContainers)
	if got := NotReturned(w.bk, w.after); got == nil || len(got) != 0 {
		t.Errorf("with the containers not known: %#v", got)
	}
	if got := NotReturned(w.bk, Facts{}); got == nil || len(got) != 0 {
		t.Errorf("with no facts: %#v", got)
	}
}

func TestAFindingIsWhatTheStatusShows(t *testing.T) {
	data, err := json.Marshal([]Finding{errorF(CodeMountsMissing, "/DATA/pool", "jellyfin", "plex"), notReturnedF("pihole", "always"), infoF(CodeDockerSourcePaused, "")})
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"code":"mounts_missing","severity":"error","arg":"/DATA/pool","apps":["jellyfin","plex"]},{"code":"container_not_returned","severity":"warn","arg":"pihole","policy":"always"},{"code":"docker_source_paused","severity":"info"}]`; string(data) != want {
		t.Errorf("json = %s\nwant   %s", data, want)
	}
}

func TestBoxesThatWentWrongAreJudgedFromTheMachineThroughTheFacts(t *testing.T) {
	// the same scenarios as above, from the box and not from the facts: what the commands print and the files say
	for _, tc := range []struct {
		name  string
		spoil func(f *fakeProbe)
		want  []string
	}{
		{"the gateway does not run", func(f *fakeProbe) { f.on("inactive\n", 0, argvUnit("casaos-gateway.service")...) }, []string{CodeCasaosUnitDown}},
		{"Docker is stopped", func(f *fakeProbe) {
			f.on("inactive\n", 0, argvDockerState...)
			f.on("Docker version 24.0.7\n", 0, "docker", "--version")
		}, []string{CodeDockerDown}},
		{"a pool is not mounted, Samba lost a user, the route is gone", func(f *fakeProbe) {
			f.set("/proc/mounts", strings.ReplaceAll(fixture(t, "proc-mounts"), "/DATA/pool", "/DATA/gone"))
			f.on("alice:1000:Alice Example\nmedia:1001:Media User\nguest:1002:\n", 0, "pdbedit", "-L")
			f.set("/proc/net/route", "Iface\tDestination\tGateway \tFlags\tRefCnt\tUse\tMetric\tMask\tMTU\tWindow\tIRTT\n")
		}, []string{CodeMountsMissing, CodeSambaUsersChanged, CodeRouteMissing}},
		{"dpkg is half way and the system still starts", func(f *fakeProbe) {
			f.on("libc6:amd64\n", 0, "dpkg", "--audit")
			f.on("starting\n", 1, "systemctl", "is-system-running")
		}, []string{CodeDpkgNotQuiet, CodeSystemNotReady}},
		{"another kernel and a pihole that is gone", func(f *fakeProbe) {
			f.on("6.1.0-26-amd64\n", 0, "uname", "-r")
			ids := containerIDs[:1:1]
			ids = append(ids, containerIDs[2:]...)
			f.on(strings.Join(ids, "\n")+"\n", 0, argvDockerPs...)
			f.forget(argvInspect(containerIDs...)...)
			var kept []string
			for _, line := range strings.Split(strings.TrimSpace(fixture(t, "docker-inspect")), "\n") {
				if !strings.Contains(line, `"/pihole"`) {
					kept = append(kept, line)
				}
			}
			f.on(strings.Join(kept, "\n")+"\n", 0, argvInspect(ids...)...)
		}, []string{CodeKernelUnexpected, CodeContainerNotReturned}},
	} {
		before := healthyBox(t)
		after := healthyBox(t)
		tc.spoil(after)
		bk := bkFixture(t)
		got := Judge(TakeWith(context.Background(), before, healthyResolver), TakeWith(context.Background(), after, healthyResolver), bk)
		var codes []string
		for _, f := range got {
			codes = append(codes, f.Code)
		}
		// the paused source of the healthy box is information that is always there
		codes = slices.DeleteFunc(codes, func(c string) bool { return c == CodeDockerSourcePaused })
		if !slices.Equal(codes, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, codes, tc.want)
		}
	}
}
