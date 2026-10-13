package osplan

import (
	"context"
	"os"
	"os/exec"
	"path"
	"strconv"
	"strings"
	"testing"
)

// aptConfigDump is what apt itself (apt-config dump) reads out of the sandbox directory dir, with the environment of the
// sandbox's commands that matters here: its own configuration file, and nothing of the box's.
func aptConfigDump(t *testing.T, bin, dir string) string {
	t.Helper()
	cmd := exec.Command(bin, "dump")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "APT_CONFIG=" + dir + "/apt-config"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apt-config dump: %v\n%s", err, out)
	}
	return string(out)
}

// materialize writes the files of a sandbox to the disk, in the directory they name.
func materialize(t *testing.T, files []SandboxFile) {
	t.Helper()
	for _, f := range files {
		if f.Mode.IsDir() {
			if err := os.MkdirAll(f.Path, 0o755); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := os.MkdirAll(path.Dir(f.Path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(f.Path, f.Content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// The proof against the real apt (where there is one): the files of a box with a hook in every form that a filter on the text
// let through in a real run of apt 2.2.4 are read, kept or left out, and apt is then asked what it reads from the sandbox. It
// reads the proxy, and none of the hooks. A control gives apt the same files as they are, to show that it WOULD have read
// them: a proof that apt reads nothing of files that it reads nothing of anyway proves nothing.
func TestRealAptReadsOnlyWhatTheSandboxKept(t *testing.T) {
	bin, err := exec.LookPath("apt-config")
	if err != nil {
		t.Skip("no apt-config on this machine: this proof is made where apt is")
	}
	elsewhere := t.TempDir()
	realMain := path.Join(elsewhere, "real-apt.conf")
	hookFile := path.Join(elsewhere, "hook.conf")
	for p, c := range map[string]string{
		realMain: "Acquire::osp-canary-main \"1\";\n",
		hookFile: "APT::Update::Post-Invoke-Success {\"touch /var/lib/apt/periodic/osp-canary-include\";};\n",
	} {
		if err := os.WriteFile(p, []byte(c), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	box := map[string]string{
		"15update-stamp":       "APT::Update::Post-Invoke-Success {\n\t\"touch /var/lib/apt/periodic/osp-canary-stamp\";\n};\n",
		"98osp-canary-splice":  "APT::Update::Post-In/**/voke-Success {\"touch /var/lib/apt/periodic/osp-canary-splice\";};\n",
		"98osp-canary-incl":    "#include \"" + hookFile + "\";\n",
		"98osp-canary-main":    "Dir::Etc::main \"" + realMain + "\";\n",
		"98osp-canary-cache":   "Dir::Cache::pkgcache \"/osp-canary-pkgcache.bin\";\n",
		"98osp-canary-clear":   "#clear APT::Update;\n",
		"98osp-canary-detect":  "Acquire::http::Proxy-Auto-Detect \"/osp-canary-detect\";\n",
		"98osp-canary-plain":   "APT::Update::Pre-Invoke \"osp-canary-plain\";\n",
		"98osp-canary-hdetect": "Acquire::http::Proxy-Auto-Detect::deb.debian.org \"/osp-canary-hdetect\";\n",
		// gpgv is run by apt as _apt, and "--log-file" makes it write a file wherever it says: in the box's own
		// /var/lib/apt/lists/partial and in /var/tmp, in a run of the real update of apt 2.2.4 in a Debian 11 root; the options of
		// a method are the same door
		"90gpgv":        "Acquire::gpgv::Options::0 \"--log-file=/var/lib/apt/lists/partial/osp-canary-gpgv\";\nAcquire::gpgv::Options::1 \"--log-file=/var/tmp/osp-canary-gpgv-x\";\n",
		"90gpgv-method": "Acquire::http::Options::0 \"--osp-canary-method\";\n",
		"99proxy":       "// the office\nAcquire::http::Proxy \"http://cache.example.lan:3142\";\nAcquire::Retries \"5\";\n",
		// the proxy of one host, with dots in the name (squid-deb-proxy, apt-cacher): kept, written again, and read by apt as the
		// proxy of that host
		"99proxy-host": "// the office, host by host\nAcquire::http::Proxy::deb.debian.org \"http://cache.example.lan:3142\";\nAcquire::https::Proxy::download.docker.com   \"DIRECT\" ;\n",
	}
	cfg, err := ReadAptConfig(context.Background(), aptBox(box))
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	materialize(t, SandboxFiles(dir, nil, cfg))
	dump := aptConfigDump(t, bin, dir)
	for _, want := range []string{
		"Acquire::http::Proxy \"http://cache.example.lan:3142\";",
		"Acquire::Retries \"5\";",
		"Acquire::http::Proxy::deb.debian.org \"http://cache.example.lan:3142\";",
		"Acquire::https::Proxy::download.docker.com \"DIRECT\";",
		"Dir::Etc::parts \"" + dir + "/apt.conf.d\";",
		"Dir::Etc::main \"" + dir + "/apt.conf\";",
		"Dir::Log::Planner \"\";",
	} {
		if !strings.Contains(dump, want) {
			t.Errorf("apt does not read %q from the sandbox:\n%s", want, dump)
		}
	}
	if strings.Contains(dump, "osp-canary") {
		t.Errorf("apt reads a hook of the box from the sandbox:\n%s", dump)
	}

	// the control: the same files as they are on the box
	var verbatim AptConfig
	for name, content := range box {
		verbatim.Fragments = append(verbatim.Fragments, TreeFile{Path: "apt.conf.d/" + name, Content: content})
	}
	control := t.TempDir()
	materialize(t, SandboxFiles(control, nil, verbatim))
	cdump := aptConfigDump(t, bin, control)
	for _, canary := range []string{"Acquire::gpgv::Options::0 \"--log-file=/var/lib/apt/lists/partial/osp-canary-gpgv\"", "Acquire::gpgv::Options::1 \"--log-file=/var/tmp/osp-canary-gpgv-x\"", "osp-canary-method", "osp-canary-splice", "osp-canary-include", "osp-canary-main", "Dir::Cache::pkgcache \"/osp-canary-pkgcache.bin\"", "osp-canary-detect", "osp-canary-hdetect", "osp-canary-plain"} {
		if !strings.Contains(cdump, canary) {
			t.Errorf("the control: apt does not read %q from the files as they are, so this test cannot say no:\n%s", canary, cdump)
		}
	}
}

// The names that apt reads in apt.conf.d, by apt itself: the files are written in a directory that apt is told
// is its parts directory, each with a setting of its own, and apt-config dump says which of them it read. apt 2.2.4 lets a colon
// into a name ("99a:b.conf", "99c:d"), reads "99a.b.conf", and ignores "99a:b.save", the other cases of the case and the dots.
func TestRealAptReadsTheFragmentsOfTheNamesWeSay(t *testing.T) {
	bin, err := exec.LookPath("apt-config")
	if err != nil {
		t.Skip("no apt-config on this machine: this proof is made where apt is")
	}
	dir := t.TempDir()
	parts := path.Join(dir, "parts")
	if err := os.Mkdir(parts, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := path.Join(dir, "apt-config")
	if err := os.WriteFile(cfg, []byte("Dir::Etc::parts \""+parts+"\";\nDir::Etc::main \""+path.Join(dir, "none.conf")+"\";\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, name := range []string{
		"99proxy", "10named.conf", "99a:b.conf", "99c:d", "99g:", ":lead", "99a.b.conf", "01-x_y", "two.dots.conf", "UPPER", "x.CONF",
		".hidden", ".conf", "old.dpkg-old", "backup.conf.save", "with space", "tilde~", "ends-with-a-dot.", "a.b", "README.txt", "99a;b", "99a:b.save",
		"99dir.conf2", "a+b", "a=b", "a@b", "a,b", "a%b",
	} {
		if err := os.WriteFile(path.Join(parts, name), []byte("OspName::"+strconv.Itoa(len(names))+" \"1\";\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		names = append(names, name)
	}
	// a directory is passed over, whatever it is called, and a link is not a regular file
	if err := os.MkdirAll(path.Join(parts, "99dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "dump")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C", "APT_CONFIG=" + cfg}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("apt-config dump: %v\n%s", err, out)
	}
	for i, name := range names {
		read := strings.Contains(string(out), "OspName::"+strconv.Itoa(i)+" \"1\";")
		if got := aptFragmentName(name); got != read {
			t.Errorf("%q: apt reads it: %v, aptFragmentName says %v", name, read, got)
		}
	}
}
