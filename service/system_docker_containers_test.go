package service

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// The docker command's answers for the list of containers

const (
	inspectedWeb      = `{"name":"/web","image":"nginx:1.27","restart":"always","network":"bridge","ports":{"80/tcp":[{"HostIp":"0.0.0.0","HostPort":"8080"}],"443/tcp":null}}`
	inspectedPihole   = `{"name":"/pihole","image":"pihole/pihole:latest","restart":"unless-stopped","network":"host","ports":{}}`
	inspectedBatch    = `{"name":"/batch-1","image":"alpine","restart":"no","network":"bridge","ports":null}`
	inspectedHTMLName = `{"name":"/<img src=x onerror=alert(1)>","image":"x","restart":"no","network":"bridge","ports":null}`
	inspectedShell    = `{"name":"/x;rm -rf /","image":"x","restart":"no","network":"bridge","ports":null}`
	inspectedPolicy   = `{"name":"/odd","image":"x","restart":"always; reboot","network":"bridge","ports":null}`
	inspectedImage    = "{\"name\":\"/imagebox\",\"image\":\"<img src=x onerror=alert(1)>\\u0000\\u001b[31m\",\"restart\":\"\",\"network\":\"bridge\",\"ports\":null}"
	// a container with Docker's socket mounted, as the format prints its mounts
	inspectedTraefik = `{"name":"/traefik","image":"traefik:v3","restart":"unless-stopped","network":"bridge","mounts":[{"Type":"bind","Source":"/srv/traefik","Destination":"/etc/traefik","Mode":"","RW":true,"Propagation":"rprivate"},{"Type":"bind","Source":"/var/run/docker.sock","Destination":"/var/run/docker.sock","Mode":"ro","RW":false,"Propagation":"rprivate"}],"ports":{}}`
)

func containersOf(t *testing.T, box *aptBox, tweak func(*systemPackageUpdater)) SystemDockerContainers {
	t.Helper()
	updater := newTestSystemPackageUpdater(t)
	updater.command = box.command
	if tweak != nil {
		tweak(updater)
	}
	return (&systemService{packageUpdates: updater}).GetSystemDockerContainers()
}

func marshalled(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDockerContainersWhenTheDaemonDoesNotAnswer(t *testing.T) {
	for name, c := range map[string]struct {
		box   *aptBox
		tweak func(*systemPackageUpdater)
	}{
		"stopped":   {&aptBox{strict: t, noDaemon: true}, nil},
		"no answer": {&aptBox{strict: t, daemonHangs: true}, nil},
		"gibberish": {&aptBox{strict: t, daemonInfo: "nothing useful\n"}, nil},
		"no command": {&aptBox{strict: t}, func(u *systemPackageUpdater) {
			u.lookPath = func(string) (string, error) { return "", errors.New("not found") }
		}},
	} {
		got := containersOf(t, c.box, c.tweak)
		if got.Running || got.Containers == nil || len(got.Containers) != 0 || marshalled(t, got) != `{"running":false,"containers":[]}` {
			t.Errorf("%s: %s", name, marshalled(t, got))
		}
		for _, call := range c.box.calls {
			if strings.Contains(call, " ps ") || strings.Contains(call, " inspect ") {
				t.Errorf("%s: %q was run for a daemon that did not answer", name, call)
			}
		}
	}
}

func TestDockerContainersOfADaemonWithNone(t *testing.T) {
	box := &aptBox{strict: t}
	got := containersOf(t, box, nil)
	if marshalled(t, got) != `{"running":true,"containers":[]}` {
		t.Errorf("got %s", marshalled(t, got))
	}
	for _, call := range box.calls {
		if strings.Contains(call, "inspect") {
			t.Errorf("inspect was run with nothing to inspect: %q", call)
		}
	}
}

func TestDockerContainersAreListedAndValidated(t *testing.T) {
	box := &aptBox{
		strict: t,
		// what `docker ps -q` printed, and what a hostile or broken command might add to it
		runningIDs: []string{"0123456789ab", "--format={{.}}", "a1b2c3d4e5f6a1b2c3d4e5f6", "$(reboot)", "NOTHEX123456", "fedcba987654", " ", "-a1b2c3d4e5f6"},
		inspected: strings.Join([]string{
			inspectedWeb, inspectedPihole, inspectedBatch, inspectedHTMLName, inspectedShell, inspectedPolicy, inspectedImage, inspectedTraefik,
			"Error: No such object: gone", "{not json",
		}, "\n") + "\n",
	}
	got := containersOf(t, box, nil)

	var names []string
	for _, c := range got.Containers {
		names = append(names, c.Name)
	}
	if !got.Running || !reflect.DeepEqual(names, []string{"batch-1", "imagebox", "pihole", "traefik", "web"}) {
		t.Fatalf("running = %v, containers = %s", got.Running, marshalled(t, got.Containers))
	}
	// the containers that talk to Docker are said to, and only they: the key is not there for the others
	if all := marshalled(t, got.Containers); strings.Count(all, "docker_socket") != 1 || !strings.Contains(all, `"name":"traefik","image":"traefik:v3","restart_policy":"unless-stopped","host_network":false,"docker_socket":true`) {
		t.Errorf("containers = %s", all)
	}
	byName := map[string]dockerpkg.Container{}
	for _, c := range got.Containers {
		byName[c.Name] = c
	}
	if web := byName["web"]; web.RestartPolicy != "always" || web.HostNetwork || web.Image != "nginx:1.27" || !reflect.DeepEqual(web.Ports, []dockerpkg.Port{{Port: 80, Protocol: "tcp", HostPort: 8080}}) {
		t.Errorf("web = %#v", web)
	}
	if pihole := byName["pihole"]; !pihole.HostNetwork || pihole.RestartPolicy != "unless-stopped" || pihole.Ports == nil || len(pihole.Ports) != 0 {
		t.Errorf("pihole = %#v", pihole)
	}
	if batch := byName["batch-1"]; batch.RestartPolicy != "no" || batch.Ports == nil {
		t.Errorf("batch-1 = %#v", batch)
	}
	// an image is text for display: no control character, whatever else it holds
	if image := byName["imagebox"].Image; image != "<img src=x onerror=alert(1)>[31m" {
		t.Errorf("image = %q", image)
	}
	// a name that is not a Docker name, or a policy that is not a policy, is not listed
	if strings.Contains(marshalled(t, got), "rm -rf") || strings.Contains(marshalled(t, got), "reboot") {
		t.Errorf("a hostile string was passed on: %s", marshalled(t, got))
	}

	// only ids went to docker inspect, after its fixed options
	want := "/usr/bin/docker inspect --format " + dockerpkg.ContainerInspectFormat + " 0123456789ab a1b2c3d4e5f6a1b2c3d4e5f6 fedcba987654"
	var inspects []string
	for _, call := range box.calls {
		if strings.Contains(call, " inspect ") {
			inspects = append(inspects, call)
		}
	}
	if len(inspects) != 1 || inspects[0] != want {
		t.Errorf("inspect calls = %#v, want %q", inspects, want)
	}
}

func TestDockerContainersWhenInspectFails(t *testing.T) {
	// a container stopped between the two commands: the others are still listed
	partial := &aptBox{strict: t, runningIDs: []string{"0123456789ab", "fedcba987654"}, inspected: inspectedWeb + "\nError: No such object: fedcba987654\n", inspectFailure: true}
	if got := containersOf(t, partial, nil); !got.Running || len(got.Containers) != 1 || got.Containers[0].Name != "web" {
		t.Errorf("partial: %s", marshalled(t, got))
	}
	// nothing at all came back: the daemon is not answering
	none := &aptBox{strict: t, runningIDs: []string{"0123456789ab"}, inspected: "Cannot connect to the Docker daemon\n", inspectFailure: true}
	if got := containersOf(t, none, nil); got.Running || got.Containers == nil || len(got.Containers) != 0 {
		t.Errorf("none: %s", marshalled(t, got))
	}
}

func TestDockerContainersAreAskedQuicklyAndWithoutTheLock(t *testing.T) {
	box := &aptBox{strict: t, runningIDs: []string{"0123456789ab"}, inspected: inspectedWeb + "\n"}
	updater := newTestSystemPackageUpdater(t)
	var withoutDeadline []string
	updater.command = func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if deadline, ok := ctx.Deadline(); !ok || time.Until(deadline) > 5*time.Second {
			withoutDeadline = append(withoutDeadline, name+" "+strings.Join(args, " "))
		}
		return box.command(ctx, name, args...)
	}
	system := &systemService{packageUpdates: updater}

	// a check or an update holds the lock for minutes; the list must not wait for it
	updater.mu.Lock()
	defer updater.mu.Unlock()
	done := make(chan SystemDockerContainers, 1)
	go func() { done <- system.GetSystemDockerContainers() }()
	select {
	case got := <-done:
		if !got.Running || len(got.Containers) != 1 {
			t.Errorf("got %s", marshalled(t, got))
		}
	case <-time.After(3 * time.Second):
		t.Fatal("the list of containers waited for the updater's lock")
	}
	if len(withoutDeadline) != 0 {
		t.Errorf("docker was run without a deadline of at most 5 s: %#v", withoutDeadline)
	}
}
