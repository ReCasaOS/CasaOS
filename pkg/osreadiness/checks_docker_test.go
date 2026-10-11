package osreadiness

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// engineOf is a machine that has the Docker engine from a package, from a policy.
func engineOf(pkgName, version, policy string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		f.installed(pkg(pkgName, version), "docker-ce", "docker.io")
		f.on(policy, 0, aptPolicyArgs(pkgName)...)
	}
}

func noEngine(f *fakeProbe) { f.installed("", "docker-ce", "docker.io") }

func TestDockerSwarm(t *testing.T) {
	runScenarios(t, "docker_swarm", []scenario{
		{name: "Docker that is not in a swarm", severity: OK},
		{name: "a node of a swarm", setup: func(f *fakeProbe) { f.on(daemonInfo("active"), 0, argvDockerInfo...) }, severity: Refuse},
		{name: "a swarm that is only asked about, or that errored, is not the active one", setup: func(f *fakeProbe) { f.on(daemonInfo("pending"), 0, argvDockerInfo...) }, severity: OK},
		{name: "a daemon that does not answer is not a swarm", setup: func(f *fakeProbe) {
			f.on("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n", 1, argvDockerInfo...)
		}, severity: OK},
		{name: "a docker command that says something that is not an answer", setup: func(f *fakeProbe) { f.on("<html>nope</html>\n", 0, argvDockerInfo...) }, severity: OK},
		{name: "no Docker", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvDockerInfo...) }, severity: OK},
		{name: "a daemon that hangs", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvDockerInfo...) }, severity: Refuse, unknown: ReasonTimeout},
		{name: "a docker command that breaks", setup: func(f *fakeProbe) { f.onError(errors.New("boom"), argvDockerInfo...) }, severity: Refuse, unknown: ReasonFailed},
	})
}

func TestDockerOrigin(t *testing.T) {
	distro := "docker.io:\n  Installed: 20.10.24+dfsg1-1+b3\n  Candidate: 20.10.24+dfsg1-1+b3\n  Version table:\n *** 20.10.24+dfsg1-1+b3 500\n        500 http://deb.debian.org/debian bullseye/main amd64 Packages\n"
	handMade := "docker-ce:\n  Installed: 5:28.0.4-1~debian.11~bullseye\n  Candidate: 5:28.0.4-1~debian.11~bullseye\n  Version table:\n *** 5:28.0.4-1~debian.11~bullseye 100\n        100 /var/lib/dpkg/status\n"
	mirror := strings.Replace(dockerPolicy(dockerVersion, dockerVersion), "https://download.docker.com/linux/debian", "http://mirror.example/docker", 1)
	runScenarios(t, "docker_origin", []scenario{
		{name: "docker-ce from Docker's repository", severity: OK, data: D{"installed": true, "origin": "docker-repository"}},
		{name: "docker-ce that apt offers from no repository", setup: engineOf("docker-ce", dockerVersion, handMade), severity: Refuse, data: D{"origin": "unknown"}},
		{name: "docker-ce from a mirror of Docker's repository", setup: engineOf("docker-ce", dockerVersion, mirror), severity: Refuse, data: D{"origin": "unknown"}},
		{name: "Docker's repository that only looks like it", setup: engineOf("docker-ce", dockerVersion, strings.Replace(dockerPolicy(dockerVersion, dockerVersion), "download.docker.com/linux", "download.docker.com.evil.example/linux", 1)), severity: Refuse},
		{name: "the docker.io of Debian", setup: engineOf("docker.io", "20.10.24+dfsg1-1+b3", distro), severity: Refuse, data: D{"origin": "distribution"}},
		{name: "docker-ce, and docker.io besides: docker-ce is the engine", setup: func(f *fakeProbe) {
			f.installed(pkg("docker-ce", dockerVersion)+pkg("docker.io", "20.10.24"), "docker-ce", "docker.io")
		}, severity: OK},
		{name: "the snap", setup: after(noEngine, func(f *fakeProbe) { f.on("Name    Version  Rev\ndocker  27.5.1   3265\n", 0, "snap", "list", "docker") }), severity: Refuse, data: D{"origin": "snap"}},
		{name: "snap that has no Docker, and no docker command", setup: after(noEngine, func(f *fakeProbe) {
			f.on("error: no matching snaps installed\n", 1, "snap", "list", "docker")
			f.onError(ErrToolMissing, argvDockerInfo...)
		}), severity: OK, data: D{"installed": false}},
		{name: "no Docker at all", setup: after(noEngine, func(f *fakeProbe) { f.onError(ErrToolMissing, argvDockerInfo...) }), severity: OK, data: D{"installed": false}},
		{name: "a docker command that no package brought", setup: noEngine, severity: Refuse, data: D{"origin": "unknown"}},
		{name: "dpkg-query that fails", setup: func(f *fakeProbe) { f.on("x", 2, argvQuery("docker-ce", "docker.io")...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "apt-cache that fails", setup: func(f *fakeProbe) { f.on("E: broken\n", 100, aptPolicyArgs("docker-ce")...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "no apt-cache", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, aptPolicyArgs("docker-ce")...) }, severity: Refuse, unknown: ReasonToolMissing},
		{name: "a snap command that hangs", setup: after(noEngine, func(f *fakeProbe) { f.onError(context.DeadlineExceeded, "snap", "list", "docker") }), severity: Refuse, unknown: ReasonTimeout},
		{name: "a docker command that hangs, for a Docker no package brought", setup: after(noEngine, func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvDockerInfo...) }), severity: Refuse, unknown: ReasonTimeout},
	})
}

func TestDockerDown(t *testing.T) {
	runScenarios(t, "docker_down", []scenario{
		{name: "a daemon that answers", severity: OK},
		{name: "a daemon that is stopped", setup: func(f *fakeProbe) {
			f.on("Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?\n", 1, argvDockerInfo...)
		}, severity: Warn, action: ActionNone},
		{name: "a daemon that says nothing", setup: func(f *fakeProbe) { f.on("", 0, argvDockerInfo...) }, severity: Warn},
		{name: "a daemon whose answer is not its version", setup: func(f *fakeProbe) { f.on(`{"ServerVersion":"<b>","SwarmState":"inactive"}`+"\n", 0, argvDockerInfo...) }, severity: Warn},
		{name: "a daemon that says it is fine and exits with a failure", setup: func(f *fakeProbe) { f.on(daemonInfo("inactive"), 1, argvDockerInfo...) }, severity: Warn},
		{name: "no Docker, so none that is down", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvDockerInfo...) }, severity: OK},
		{name: "a daemon that hangs", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvDockerInfo...) }, severity: Refuse, unknown: ReasonTimeout},
	})
}

func TestDockerUpdatePending(t *testing.T) {
	policy := func(installed, candidate string) func(*fakeProbe) {
		return engineOf("docker-ce", installed, dockerPolicy(installed, candidate))
	}
	runScenarios(t, "docker_update_pending", []scenario{
		{name: "up to date", severity: OK},
		{name: "a major version waits", setup: policy(dockerVersion, "5:29.8.0-1~debian.11~bullseye"), severity: Warn, data: D{"package": "docker-ce", "installed": dockerVersion, "candidate": "5:29.8.0-1~debian.11~bullseye"}, action: ActionNone},
		{name: "a version that is larger by its numbers and not by its letters", setup: policy("5:28.0.9-1~debian.11~bullseye", "5:28.0.10-1~debian.11~bullseye"), severity: Warn},
		{name: "a candidate that is older", setup: policy("5:29.8.0-1~debian.11~bullseye", dockerVersion), severity: OK},
		{name: "no candidate", setup: policy(dockerVersion, "(none)"), severity: OK},
		{name: "not installed, as apt says", setup: engineOf("docker-ce", dockerVersion, "docker-ce:\n  Installed: (none)\n  Candidate: 5:29.8.0-1\n"), severity: OK},
		{name: "the docker.io of Debian, newer in the archive", setup: engineOf("docker.io", "20.10.24", "docker.io:\n  Installed: 20.10.24\n  Candidate: 20.10.25\n"), severity: Warn, data: D{"package": "docker.io"}},
		{name: "no engine", setup: noEngine, severity: OK},
		{name: "a policy of nothing", setup: engineOf("docker-ce", dockerVersion, "\n"), severity: OK},
		{name: "a policy whose installed version cannot be shown", setup: engineOf("docker-ce", dockerVersion, "Installed: <script>\nCandidate: 99:xy\n"), severity: Warn, data: D{"candidate": "99:xy"}},
		{name: "a policy whose candidate has a blank in it", setup: engineOf("docker-ce", dockerVersion, "Installed: 1\nCandidate: 99:x y\n"), severity: OK},
		{name: "apt-cache that fails", setup: func(f *fakeProbe) { f.on("", 100, aptPolicyArgs("docker-ce")...) }, severity: Refuse, unknown: ReasonFailed},
	})
}

// running is a machine whose daemon has these containers, which the lines of "docker inspect" say.
func running(lines ...string) func(*fakeProbe) {
	return func(f *fakeProbe) {
		ids := make([]string, len(lines))
		for i := range lines {
			ids[i] = fmt.Sprintf("%012x", i+1)
		}
		f.on(strings.Join(ids, "\n")+"\n", 0, argvDockerPs...)
		f.on(strings.Join(lines, ""), 0, inspectArgv(ids...)...)
	}
}

func inspectArgv(ids ...string) []string {
	return append([]string{"docker", "inspect", "--format", dockerpkg.ContainerInspectFormat}, ids...)
}

func TestContainersPolicy(t *testing.T) {
	runScenarios(t, "containers_policy", []scenario{
		{name: "no container", severity: OK},
		{name: "containers that come back", setup: running(inspected("web", "always", false), inspected("db", "unless-stopped", false), inspected("job", "on-failure", false)), severity: OK},
		{name: "a container with no policy, as docker says it", setup: running(inspected("web", "no", false)), severity: Warn, data: D{"names": []string{"web"}, "count": 1}, action: ActionNone},
		{name: "a container with an empty policy", setup: running(inspected("web", "", false)), severity: Warn, data: D{"names": []string{"web"}}},
		{name: "several, sorted, and the ones that come back left out", setup: running(inspected("zeta", "no", false), inspected("alpha", "", false), inspected("mid", "always", false)), severity: Warn, data: D{"names": []string{"alpha", "zeta"}, "count": 2}},
		{name: "the same name twice is said once", setup: running(inspected("web", "no", false), inspected("web", "no", false)), severity: Warn, data: D{"names": []string{"web"}, "count": 2}},
		{name: "a name that is not a container's", setup: running(inspected("<script>alert(1)</script>", "no", false), inspected("ok", "no", false)), severity: Warn, data: D{"names": []string{"ok"}, "count": 1}},
		{name: "a container that stopped between the two commands: the others are said", setup: func(f *fakeProbe) {
			ids := []string{fmt.Sprintf("%012x", 1), fmt.Sprintf("%012x", 2)}
			f.on(strings.Join(ids, "\n")+"\n", 0, argvDockerPs...)
			f.on(inspected("web", "no", false), 1, inspectArgv(ids...)...)
		}, severity: Warn, data: D{"names": []string{"web"}}},
		{name: "ids that are not ids are not passed on", setup: func(f *fakeProbe) {
			f.on("--format\n-a\n0123456789AB\n"+fmt.Sprintf("%012x", 1)+"\n"+fmt.Sprintf("%012x", 1)+"\n../x\n", 0, argvDockerPs...)
			f.on(inspected("web", "no", false), 0, inspectArgv(fmt.Sprintf("%012x", 1))...)
		}, severity: Warn, data: D{"names": []string{"web"}}},
		{name: "ps that lists nothing valid", setup: func(f *fakeProbe) { f.on("--all\n", 0, argvDockerPs...) }, severity: OK},
		{name: "a daemon that is down: no container runs", setup: func(f *fakeProbe) { f.on("", 1, argvDockerInfo...) }, severity: OK},
		{name: "no Docker", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvDockerInfo...) }, severity: OK},
		{name: "ps that fails", setup: func(f *fakeProbe) { f.on("Error\n", 1, argvDockerPs...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "ps whose list was cut", setup: func(f *fakeProbe) { f.onTruncated(fmt.Sprintf("%012x\n", 1), argvDockerPs...) }, severity: Refuse, unknown: ReasonTooLarge},
		{name: "ps that hangs", setup: func(f *fakeProbe) { f.onError(context.DeadlineExceeded, argvDockerPs...) }, severity: Refuse, unknown: ReasonTimeout},
		{name: "inspect that fails and says nothing", setup: func(f *fakeProbe) {
			f.on(fmt.Sprintf("%012x\n", 1), 0, argvDockerPs...)
			f.on("Error: No such object\n", 1, inspectArgv(fmt.Sprintf("%012x", 1))...)
		}, severity: Refuse, unknown: ReasonFailed},
		{name: "inspect whose output was cut", setup: func(f *fakeProbe) {
			f.on(fmt.Sprintf("%012x\n", 1), 0, argvDockerPs...)
			f.onTruncated(inspected("web", "no", false), inspectArgv(fmt.Sprintf("%012x", 1))...)
		}, severity: Refuse, unknown: ReasonTooLarge},
		{name: "inspect that answers garbage with a good status: no container is known", setup: func(f *fakeProbe) {
			f.on(fmt.Sprintf("%012x\n", 1), 0, argvDockerPs...)
			f.on("not json\n{\"name\":5}\n[]\n", 0, inspectArgv(fmt.Sprintf("%012x", 1))...)
		}, severity: OK},
		{name: "more containers than are asked about", setup: func(f *fakeProbe) {
			var ids []string
			for i := 0; i < maxInspected+1; i++ {
				ids = append(ids, fmt.Sprintf("%012x", i+1))
			}
			f.on(strings.Join(ids, "\n")+"\n", 0, argvDockerPs...)
		}, severity: Refuse, unknown: ReasonTooLarge},
	})
}

func TestContainersPorts(t *testing.T) {
	runScenarios(t, "containers_ports", []scenario{
		{name: "no container", severity: OK},
		{name: "a web app on a port of its own", setup: running(inspected("web", "always", false, "80/tcp:8080", "443/tcp:8443")), severity: OK},
		{name: "a container that publishes port 80 of the machine", setup: running(inspected("web", "always", false, "80/tcp:80")), severity: Warn, data: D{"names": []string{"web"}, "ports": []string{"80"}, "host_network": false}, action: ActionNone},
		{name: "DNS, TCP and UDP, and a web server, sorted", setup: running(inspected("dns", "always", false, "53/udp:53", "53/tcp:53"), inspected("proxy", "always", false, "443/tcp:443", "80/tcp:80")), severity: Warn,
			data: D{"names": []string{"dns", "proxy"}, "ports": []string{"53", "80", "443"}}},
		{name: "a container on the machine's network", setup: running(inspected("homeassistant", "always", true)), severity: Warn, data: D{"names": []string{"homeassistant"}, "ports": []string{}, "host_network": true}},
		{name: "one that is, and one that is not", setup: running(inspected("a", "always", true), inspected("b", "always", false, "9000/tcp:9000")), severity: Warn, data: D{"names": []string{"a"}}},
		{name: "container port 80 on another host port, and host port 80 for another container port", setup: running(inspected("a", "always", false, "80/tcp:8080"), inspected("b", "always", false, "8080/tcp:80")), severity: Warn, data: D{"names": []string{"b"}, "ports": []string{"80"}}},
		{name: "a daemon that is down", setup: func(f *fakeProbe) { f.on("", 1, argvDockerInfo...) }, severity: OK},
		{name: "no Docker", setup: func(f *fakeProbe) { f.onError(ErrToolMissing, argvDockerInfo...) }, severity: OK},
		{name: "ps that fails", setup: func(f *fakeProbe) { f.on("", 1, argvDockerPs...) }, severity: Refuse, unknown: ReasonFailed},
		{name: "inspect that has no tool", setup: func(f *fakeProbe) {
			f.on(fmt.Sprintf("%012x\n", 1), 0, argvDockerPs...)
			f.onError(ErrToolMissing, inspectArgv(fmt.Sprintf("%012x", 1))...)
		}, severity: Refuse, unknown: ReasonToolMissing},
	})
}

// backups is a machine whose AppManagement log has the runs.
func backups(runs ...string) func(*fakeProbe) {
	return func(f *fakeProbe) { f.set(backupLog, "["+strings.Join(runs, ",")+"]") }
}

func run(finished, extra string) string {
	return fmt.Sprintf(`{"app":"jellyfin","destination":"/DATA","stamp":"s","started_at":%q,"finished_at":%q,"scheduled":true%s}`, finished, finished, extra)
}

func TestBackupAge(t *testing.T) {
	// the clock of the box is 2026-10-11 20:15:02
	runScenarios(t, "backup_age", []scenario{
		{name: "a backup of yesterday", severity: OK, data: D{"days": 1}},
		{name: "a backup that is seven days old", setup: backups(run("2026-10-04T20:15:02Z", "")), severity: OK, data: D{"days": 7}},
		{name: "a backup that is seven days and a second old", setup: backups(run("2026-10-04T20:15:01Z", "")), severity: Warn, data: D{"days": 7, "last_backup": "2026-10-04"}, action: ActionNone},
		{name: "a backup that is a month old", setup: backups(run("2026-09-11T01:00:00Z", "")), severity: Warn, data: D{"days": 30, "last_backup": "2026-09-11"}},
		{name: "the newest of several, whatever their order", setup: backups(run("2026-10-10T01:00:00Z", ""), run("2026-09-01T01:00:00Z", ""), run("2026-10-05T01:00:00Z", "")), severity: OK},
		{name: "an empty list", setup: backups(), severity: Warn, data: D{"none": true}},
		{name: "no log: nothing was ever backed up", setup: func(f *fakeProbe) { f.remove(backupLog) }, severity: Warn, data: D{"none": true}},
		{name: "a log with nothing in it", setup: func(f *fakeProbe) { f.set(backupLog, "\n") }, severity: Warn, data: D{"none": true}},
		{name: "a log that is null", setup: func(f *fakeProbe) { f.set(backupLog, "null") }, severity: Warn, data: D{"none": true}},
		{name: "runs that failed are no backup", setup: backups(run("2026-10-10T01:00:00Z", `,"error":"destination not mounted"`)), severity: Warn, data: D{"none": true}},
		{name: "a failed run of today, and a run of last month that did not fail", setup: backups(run("2026-10-11T01:00:00Z", `,"error":"x"`), run("2026-09-11T01:00:00Z", "")), severity: Warn, data: D{"last_backup": "2026-09-11"}},
		{name: "a restore is no backup", setup: backups(run("2026-10-10T01:00:00Z", `,"restore":true`)), severity: Warn, data: D{"none": true}},
		{name: "a run that has a start and no end", setup: backups(`{"app":"x","started_at":"2026-10-10T01:00:00Z"}`), severity: OK},
		{name: "a run that has no date", setup: backups(`{"app":"x"}`), severity: Warn, data: D{"none": true}},
		{name: "a date that has not come: a clock that was wrong", setup: backups(run("2030-01-01T00:00:00Z", "")), severity: Warn, data: D{"none": true}},
		{name: "a date that is only a few hours ahead", setup: backups(run("2026-10-12T01:00:00Z", "")), severity: OK},
		{name: "a log that is not JSON", setup: func(f *fakeProbe) { f.set(backupLog, "not json") }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a log that is cut", setup: func(f *fakeProbe) { f.set(backupLog, `[{"app":"x","finished_at":"2026-10-1`) }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a log that is an object", setup: func(f *fakeProbe) { f.set(backupLog, `{"runs":[]}`) }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a log with a date that is no date", setup: func(f *fakeProbe) { f.set(backupLog, `[{"finished_at":"yesterday"}]`) }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a log with an error that is no text", setup: func(f *fakeProbe) { f.set(backupLog, `[{"error":5}]`) }, severity: Refuse, unknown: ReasonUnexpectedOutput},
		{name: "a log that cannot be read", setup: func(f *fakeProbe) {
			f.fail(backupLog, &fs.PathError{Op: "open", Path: backupLog, Err: fs.ErrPermission})
		}, severity: Refuse, unknown: ReasonUnreadable},
		{name: "a log that is too large", setup: func(f *fakeProbe) { f.set(backupLog, strings.Repeat(" ", MaxFileBytes+1)) }, severity: Refuse, unknown: ReasonTooLarge},
	})
}

func TestSnapshotReminder(t *testing.T) {
	for name, setup := range map[string]func(*fakeProbe){"debian 11": nil, "debian 12": debian12, "ubuntu 22.04": ubuntu2204, "ubuntu 24.04": ubuntu2404} {
		f := newBox(t)
		if setup != nil {
			setup(f)
		}
		got := checkOn(t, f, "snapshot_reminder")
		if got.Severity != Info || got.Action != ActionNone || !strings.Contains(got.Detail, "snapshot") {
			t.Errorf("%s: finding = %+v", name, got)
		}
	}
}
