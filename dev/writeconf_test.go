package writeconf_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestWriteconf(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("Caller")
	}
	script := filepath.Join(filepath.Dir(thisFile), "writeconf.sh")
	root := t.TempDir()
	repo := t.TempDir()
	cmd := exec.Command("bash", script, root, repo)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("writeconf.sh: %v\n%s", err, out)
	}

	run := filepath.Join(root, "run")
	data := filepath.Join(root, "data")
	lib := filepath.Join(root, "lib")
	gateway, err := os.ReadFile(filepath.Join(root, "etc", "gateway.ini"))
	if err != nil {
		t.Fatal(err)
	}
	g := string(gateway)
	for _, want := range []string{
		"port=8081",
		"address=127.0.0.1",
		"httpsport=0",
		"runtimepath=" + run,
	} {
		if !strings.Contains(g, want) {
			t.Errorf("gateway.ini missing %q\n%s", want, g)
		}
	}

	casaos, err := os.ReadFile(filepath.Join(root, "etc", "casaos.conf"))
	if err != nil {
		t.Fatal(err)
	}
	c := string(casaos)
	for _, want := range []string{
		"[common]",
		"RuntimePath=" + run,
		"ManagementRoots=" + data,
	} {
		if !strings.Contains(c, want) {
			t.Errorf("casaos.conf missing %q\n%s", want, c)
		}
	}

	bus, err := os.ReadFile(filepath.Join(root, "etc", "message-bus.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bus), "DBPath="+lib) {
		t.Errorf("message-bus.conf DBPath\n%s", bus)
	}
	if strings.Contains(string(bus), "/var/lib/casaos") {
		t.Errorf("message-bus.conf still has /var/lib/casaos\n%s", bus)
	}

	users, err := os.ReadFile(filepath.Join(root, "etc", "user-service.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(users), "FirstAdmin=web") {
		t.Errorf("user-service.conf FirstAdmin\n%s", users)
	}

	ls, err := os.ReadFile(filepath.Join(root, "etc", "local-storage.conf"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(ls), "EnableMergerFS=false") {
		t.Errorf("local-storage.conf EnableMergerFS\n%s", ls)
	}
}
