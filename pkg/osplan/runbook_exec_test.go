package osplan

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// Blocks of the runbook, run: with the paths of the machine pointed at a directory of the test, and the commands of the machine
// replaced by small ones that answer what the test says. The text of the block is the runbook's own, but for those paths: the
// replacements are the ones of shellBox.paths, and nothing else of it is changed.
//
// DEVELOPING NOTES. These tests start a shell for every block they run, and a shell is cheap on Linux and dear on Windows with Git Bash: the
// package takes some 16 s under dash on Linux and some 2000 s on Windows (the suites run twice, on the owner's text and on the unit's: see
// auto_exec_test.go), well over the 10 minutes that go test allows by default. On Windows pass a longer limit for a full run (go test -timeout 60m
// ./pkg/osplan), run one test with -run (the unit's text is TestTheExecSuitesRunOnTheAutoText/<the name of the test>), or pass -short to skip every
// test that runs the shell (each says why it was skipped). The run under dash on Linux is the one that counts, and it is what CI does: build the
// test binary for Linux (GOOS=linux go test -c ./pkg/osplan) and run it where /bin/sh is dash (a Debian chroot), from a directory that has testdata
// and the two files of the suites (TestEveryExecTestIsInTheAutoSetOrManualOnly reads them).

// planMadeAt is the clock of the plans of these tests (the clock of newBox).
var planMadeAt = time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC)

// initrdOK is the size of an initrd that is over 5 MiB, by one byte: block 7 has nothing to say about it. initrdFloor is the
// size under which block 7 stops (1 MiB); from there to 5 MiB it only warns.
const (
	initrdOK    int64 = 5<<20 + 1
	initrdFloor int64 = 1 << 20
)

type shellBox struct {
	t   *testing.T
	dir string
	// env is what is added to the environment of the shell. The shell is in tmux unless noMux says it is not (blocks 5 and 6 ask).
	env   []string
	noMux bool
	// umask, when it is set, is the umask of the shell that runs the block.
	umask string
	// auto is set when the blocks that it runs are the ones of the unit (BuildAuto), in the unit of run testRunID: the environment says so, ./run is the folder of the run
	// (the blocks write the class of a stop there), and ./cgroup is the file that the gate reads and that names the unit.
	auto bool
}

func newShellBox(t *testing.T) *shellBox {
	t.Helper()
	b := &shellBox{t: t, dir: t.TempDir(), auto: suiteAuto}
	for _, d := range []string{"bin", "fake", "bk", "run", "etc-apt/sources.list.d", "boot/grub", "archives", "root/etc"} {
		if err := os.MkdirAll(filepath.Join(b.dir, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	b.write("cgroup", "0::/system.slice/casaos-os-upgrade.service\n")
	// sync says whether the marker of the point of no return was on the disk's way when it ran
	b.fake("sync", "echo \"sync begun-upgrade=$([ -e ./bk/begun-upgrade ] && echo yes || echo no)\" >> \"$FAKE/calls.log\"")
	b.fake("id", "[ \"$1\" = -u ] && echo 0 || echo root")
	b.fake("hostname", "echo testbox")
	b.fake("uname", "if [ \"$1\" = -r ]; then if [ -f \"$FAKE/uname-r\" ]; then cat \"$FAKE/uname-r\"; else echo 5.10.0-46-cloud-amd64; fi; else echo Linux; fi")
	b.fake("tmux", "exit 0")
	b.fake("df", "p=\"$2\"; k=\"$(echo \"$p\" | tr / _)\"; if [ -f \"$FAKE/avail$k\" ]; then a=$(cat \"$FAKE/avail$k\"); else a=$(cat \"$FAKE/avail\"); fi; echo Filesystem 1024-blocks Used Available Capacity Mounted; echo \"/dev/x 1 1 $a 1% $p\"")
	b.fake("ps", "[ -f \"$FAKE/ps.txt\" ] && cat \"$FAKE/ps.txt\" || true")
	b.fake("date", "if [ \"$1\" = +%s ]; then cat \"$FAKE/now\"; else /bin/date \"$@\"; fi")
	b.fake("tty", "cat \"$FAKE/tty\" 2>/dev/null || echo /dev/pts/0")
	b.fake("ss", "cat \"$FAKE/ss.txt\" 2>/dev/null || true")
	b.fake("readlink", "[ -f \"$FAKE/resolv-link\" ] && cat \"$FAKE/resolv-link\" || exit 1")
	b.fake("lsinitramfs", "exit $(cat \"$FAKE/lsinitramfs.rc\" 2>/dev/null || echo 0)")
	b.fake("update-alternatives", `case "$*" in
  *"--query iptables"*) if [ -f "$FAKE/iptables-now" ]; then printf 'Name: iptables\nLink: /usr/sbin/iptables\nStatus: auto\nValue: %s\n' "$(cat "$FAKE/iptables-now")"; else echo "update-alternatives: error: no alternatives for iptables" >&2; exit 2; fi ;;
esac`)
	b.fake("dpkg-query", `for last; do :; done
case "$last" in
  linux-image-*) grep " linux-image-[0-9]" "$FAKE/kernels.txt" 2>/dev/null || true ;;
  -f*) cat "$FAKE/packages.tsv" 2>/dev/null || true ;;
  *) case "$*" in
       *'${Essential}'*) echo " optional" ;;
       *) if [ -f "$FAKE/pkg-$last" ]; then cat "$FAKE/pkg-$last"; else echo "dpkg-query: no packages found matching $last" >&2; exit 1; fi ;;
     esac ;;
esac`)
	b.fake("docker", `echo "docker $*" >> "$FAKE/calls.log"
case "$1" in
  ps) if [ "$2" = -aq ]; then cat "$FAKE/docker-ids.txt" 2>/dev/null || true; else cat "$FAKE/docker-ps.txt" 2>/dev/null || true; fi ;;
  inspect) if [ "$#" -lt 4 ]; then echo "docker inspect requires at least 1 argument."; exit 1; fi; cat "$FAKE/docker-inspect.txt" ;;
esac
exit 0`)
	// curl says the status that it was asked to write (-w) as FAKE_CURL_CODE, and ends with FAKE_CURL_EXIT
	b.fake("curl", "echo \"curl $*\" >> \"$FAKE/calls.log\"; case \"$*\" in *http_code*) echo \"${FAKE_CURL_CODE:-200}\" ;; esac; exit ${FAKE_CURL_EXIT:-0}")
	b.fake("dkms", "cat \"$FAKE/dkms.txt\" 2>/dev/null || true")
	b.fake("debconf-communicate", `read -r cmd
case "$cmd" in
  "get grub-pc/install_devices") echo "0 $(cat "$FAKE/grub-devices" 2>/dev/null)" ;;
  "get grub-pc/install_devices_empty") echo "0 $(cat "$FAKE/grub-empty" 2>/dev/null || echo false)" ;;
  *) echo "10 $cmd doesn't exist" ;;
esac`)
	// sshd -t and sshd -T need /run/sshd, which only a running ssh.service makes: without it -t fails (255) and -T says so and prints nothing
	b.fake("sshd", "echo \"sshd $*\" >> \"$FAKE/calls.log\"\nif [ ! -d ./run-sshd ]; then echo 'Missing privilege separation directory: /run/sshd' >&2; [ \"$1\" = -t ] && exit 255; exit 0; fi\ncase \"$1\" in -t) exit $(cat \"$FAKE/sshd-t.rc\" 2>/dev/null || echo 0);; -T) cat \"$FAKE/sshd-T.txt\" 2>/dev/null || true;; esac")
	b.fake("dpkg", `echo "dpkg $*" >> "$FAKE/calls.log"
case "$1" in
  --audit) if [ -f "$FAKE/audit-error.txt" ]; then cat "$FAKE/audit-error.txt" >&2; exit 2; fi; cat "$FAKE/audit.txt" 2>/dev/null || true ;;
  --get-selections) ls -ld ./bk | awk '{ print $1 }' > "$FAKE/bkmode"; echo "bash install" ;;
  --configure) [ ! -f "$FAKE/dpkg-fails" ] || exit 1 ;;
esac
exit 0`)
	b.fake("systemctl", `echo "systemctl $*" >> "$FAKE/calls.log"
case "$1" in
  is-active) if [ -f "$FAKE/active-$2" ]; then echo active; else echo inactive; exit 3; fi ;;
  stop) rm -f "$FAKE/active-$2" ;;
  start) : > "$FAKE/active-$2" ;;
  --failed) cat "$FAKE/failed.txt" 2>/dev/null || true ;;
  list-units) cat "$FAKE/casaos-units.txt" 2>/dev/null || true ;;
esac
exit 0`)
	b.fake("apt-get", `echo "apt-get $*" >> "$FAKE/calls.log"
echo "DEBIAN_FRONTEND=${DEBIAN_FRONTEND-unset} APT_LISTCHANGES_FRONTEND=${APT_LISTCHANGES_FRONTEND-unset} NEEDRESTART_SUSPEND=${NEEDRESTART_SUSPEND-unset} for: $*" >> "$FAKE/env.log"
case "$*" in
  *"-s dist-upgrade"*)
    n=$(cat "$FAKE/sim.n" 2>/dev/null || echo 0); n=$((n + 1)); echo $n > "$FAKE/sim.n"
    if [ -f "$FAKE/sim-fails-$n" ]; then echo "E: Unable to correct problems, you have held broken packages."; exit 100; fi
    if [ -f "$FAKE/sim$n.txt" ]; then cat "$FAKE/sim$n.txt"; else cat "$FAKE/sim.txt"; fi ;;
  *--download-only*) if [ -f "$FAKE/download-fails" ]; then exit 100; fi ;;
  *"-s -f install"*)
    if [ -f "$FAKE/fixsim-fails" ]; then echo "E: Unmet dependencies."; exit 100; fi
    if [ -f "$FAKE/fixsim.txt" ]; then cat "$FAKE/fixsim.txt"; else cat "$FAKE/sim.txt"; fi ;;
  *" -f install"*) if [ -f "$FAKE/fix-fails" ]; then exit 100; fi ;;
  *" dist-upgrade"*)
    n=$(cat "$FAKE/dist.n" 2>/dev/null || echo 0); n=$((n + 1)); echo $n > "$FAKE/dist.n"
    if [ -f "$FAKE/dist-fails-$n" ]; then exit 100; fi ;;
  *" upgrade"*)
    if [ -e ./bk/begun-upgrade ]; then echo yes >> "$FAKE/begun-at-upgrade"; else echo no >> "$FAKE/begun-at-upgrade"; fi
    # an upgrade that did something before it failed: dpkg's status was rewritten, or it was killed and left its journal
    if [ -f "$FAKE/upgrade-touches-status" ]; then echo "Package: apt-touched" >> ./dpkg-status; fi
    if [ -f "$FAKE/upgrade-leaves-journal" ]; then mkdir -p ./dpkg-updates; : > ./dpkg-updates/0001; fi
    if [ -f "$FAKE/upgrade-fails" ]; then exit 100; fi ;;
  *update*) if [ -f "$FAKE/update-fails" ]; then exit 100; fi ;;
  *check*) if [ -f "$FAKE/check-fails" ]; then exit 100; fi ;;
esac
exit 0`)
	b.write("fake/avail", "99999999\n")
	b.write("dpkg-status", "Package: bash\nStatus: install ok installed\n")
	// the box is Debian 11 until a test says otherwise: every block after the first stops on a machine that is not, unless its plan has begun the upgrade
	b.write("os-release", "VERSION_CODENAME=bullseye\n")
	b.write("bootid", "boot-0\n")
	b.write("fake/sim.txt", converged)
	b.write("fake/now", strconv.FormatInt(planMadeAt.Add(time.Hour).Unix(), 10)+"\n")
	return b
}

const converged = "Reading package lists...\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"

func (b *shellBox) write(rel, content string) {
	b.t.Helper()
	p := filepath.Join(b.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		b.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		b.t.Fatal(err)
	}
}

// sized makes a file of n bytes (zeros, and no time spent on them).
func (b *shellBox) sized(rel string, n int64) {
	b.t.Helper()
	p := filepath.Join(b.dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		b.t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		b.t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(n); err != nil {
		b.t.Fatal(err)
	}
}

func (b *shellBox) fake(name, body string) {
	b.t.Helper()
	p := filepath.Join(b.dir, "bin", name)
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		b.t.Fatal(err)
	}
}

func (b *shellBox) exists(rel string) bool {
	_, err := os.Stat(filepath.Join(b.dir, filepath.FromSlash(rel)))
	return err == nil
}

func (b *shellBox) read(rel string) string {
	data, _ := os.ReadFile(filepath.Join(b.dir, filepath.FromSlash(rel)))
	return string(data)
}

// mode is the permission bits of a path of the box (not meaningful on Windows: the tests that read it skip there).
func (b *shellBox) mode(rel string) os.FileMode {
	b.t.Helper()
	st, err := os.Stat(filepath.Join(b.dir, filepath.FromSlash(rel)))
	if err != nil {
		b.t.Fatal(err)
	}
	return st.Mode().Perm()
}

// paths point the paths of the machine that a block names at the directory of the test.
func (b *shellBox) paths(code, planID string) string {
	r := strings.NewReplacer(
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH", "PATH=\"$PWD/bin:$PATH\"; export PATH",
		"BK=/var/backups/casaos-os-upgrade-"+planID, "BK=./bk",
		"TS="+timersState, "TS=./timers.state",
		"/var/lib/dpkg/status", "./dpkg-status",
		"/var/lib/dpkg/updates", "./dpkg-updates",
		"/proc/mounts", "./mounts",
		"/sys/firmware/efi", "./sys-efi",
		// the EFI system partition keeps its name (only the files of /boot are moved to the directory of the test): the fake df asks for avail_boot_efi
		"avail /boot/efi", "avail /boot/efi",
		"' /boot/efi '", "' /boot/efi '",
		"/proc/sys/kernel/random/boot_id", "./bootid",
		"/etc/os-release", "./os-release",
		"cd /etc/apt", "cd ./etc-apt",
		"APT_ETC=/etc/apt", "APT_ETC=./etc-apt",
		"/boot/", "./boot/",
		"/var/cache/apt/archives/*.deb", "./archives/*.deb",
		"find / -xdev", "find . -xdev",
		"/var/lib/apt/extended_states", "./extended_states",
		"/run/sshd", "./run-sshd",
		"[ -S /run/docker.sock ]", "[ -f ./docker.sock ]",
		"tar -C / -czf", "tar -C ./root -czf",
		// the folders that other plans left (blocks 0 and 3 look at them) are in ./others
		"/var/backups/casaos-os-upgrade-*", "./others/casaos-os-upgrade-*",
	)
	// the restore script is written as it is: the block reads it back and compares it with its hash
	parts := strings.Split(code, restoreScript)
	for i := range parts {
		parts[i] = r.Replace(parts[i])
	}
	return strings.Join(parts, restoreScript)
}

// run runs a block of the runbook and says what it printed and how it ended.
func (b *shellBox) run(rb *Runbook, id string) (string, int) {
	b.t.Helper()
	return b.runCode(b.paths(blockOf(b.t, rb.Text, id), rb.PlanID))
}

// environ is the environment of a block: the box's own, and in the unit the three words that the driver gives its blocks (the nonce is not one of them).
func (b *shellBox) environ() []string {
	env := slices.DeleteFunc(os.Environ(), func(e string) bool {
		return strings.HasPrefix(e, "TMUX=") || strings.HasPrefix(e, "STY=") || strings.HasPrefix(e, "CASAOS_OSU_")
	})
	env = append(env, "FAKE=./fake")
	if !b.noMux {
		env = append(env, "TMUX=/tmp/tmux-0/default,1,0")
	}
	if b.auto {
		env = append(env, "CASAOS_OSU_RUN="+testRunID, "CASAOS_OSU_DIR=./run", "CASAOS_OSU_ATTEMPT=1")
	}
	return append(env, b.env...)
}

// runCode runs the code of a block, whose paths have been pointed at the box, and says what it printed and how it ended.
func (b *shellBox) runCode(code string) (string, int) {
	b.t.Helper()
	if b.umask != "" {
		code = "umask " + b.umask + "\n" + code
	}
	cmd := exec.Command(shellOrSkip(b.t), writeScript(b.t, code))
	cmd.Dir = b.dir
	cmd.Env = b.environ()
	out, err := cmd.CombinedOutput()
	exit := 0
	if ee, ok := err.(*exec.ExitError); ok {
		exit = ee.ExitCode()

	} else if err != nil {
		b.t.Fatalf("sh: %v", err)
	}
	return string(out), exit
}

// asRunbookWith is the runbook of the real plan with the typed words that the owner types: a variable that is set between
// the quotes is what the owner's edit does.
func typed(rb *Runbook, name, value string) *Runbook {
	out := *rb
	out.Text = strings.Replace(rb.Text, "\n"+name+"=\"\"\n", "\n"+name+"=\""+value+"\"\n", 1)
	return &out
}

// withText is the runbook with one part of its text replaced by another, once: the damage that a console can do to a paste.
func withText(t *testing.T, rb *Runbook, from, to string) *Runbook {
	t.Helper()
	if !strings.Contains(rb.Text, from) {
		t.Fatalf("%q is not in the runbook", from)
	}
	out := *rb
	out.Text = strings.Replace(rb.Text, from, to, 1)
	return &out
}

// setupVerify makes the box that block 0 is happy with: Debian 11, the sources of the plan.
func setupVerify(t *testing.T, b *shellBox) {
	t.Helper()
	b.write("os-release", "PRETTY_NAME=\"Debian GNU/Linux 11 (bullseye)\"\nVERSION_CODENAME=bullseye\nID=debian\n")
	for _, f := range d11Box(t) {
		name := f.Name
		if f.InParts {
			name = "sources.list.d/" + name
		}
		b.write("etc-apt/"+name, string(f.Content))
	}
}

// psTable is a table of processes as ps -eo comm=,args= prints it: the name of the command, padded, and its arguments.
func psTable(rows ...string) string {
	return "systemd         /sbin/init\nkthreadd        [kthreadd]\nsshd            sshd: root@pts/0\n" + strings.Join(rows, "\n") + "\n"
}

func TestBlock0ChecksTheMachineBeforeAnythingElse(t *testing.T) {
	p, rb := realRunbook(t)
	good := typed(rb, "TYPED_HOSTNAME", "testbox")
	b := newShellBox(t)
	setupVerify(t, b)
	out, code := b.run(good, "verify")
	if code != 0 || !b.exists("bk/done-verify") || !strings.Contains(out, "Block 0 done.") {
		t.Fatalf("a good machine: exit %d, %q", code, out)
	}
	expires := p.ExpiresAt.Unix()
	for _, tc := range []struct {
		name  string
		rb    *Runbook
		set   func(b *shellBox)
		stops string
	}{
		{"no name typed", rb, func(b *shellBox) {}, "this machine is called testbox"},
		{"another name typed", typed(rb, "TYPED_HOSTNAME", "other"), func(b *shellBox) {}, "this machine is called testbox"},
		{"not root", good, func(b *shellBox) { b.fake("id", "echo 1000") }, "run this as root"},
		{"not bullseye", good, func(b *shellBox) { b.write("os-release", "VERSION_CODENAME=bookworm\n") }, "not Debian 11"},
		{"dpkg is not quiet", good, func(b *shellBox) { b.write("fake/audit.txt", "The following packages are only half installed\n") }, "half installed"},
		{"apt-get check fails", good, func(b *shellBox) { b.write("fake/check-fails", "") }, "broken dependency"},
		{"no room for the packages", good, func(b *shellBox) { b.write("fake/avail_var_cache_apt_archives", "1000\n") }, "/var/cache/apt/archives needs"},
		{"no room in /usr", good, func(b *shellBox) { b.write("fake/avail_usr", "1000\n") }, "/usr needs"},
		{"no room in /", good, func(b *shellBox) { b.write("fake/avail_", "1000\n") }, "/ needs"},
		{"the sources changed", good, func(b *shellBox) {
			b.write("etc-apt/sources.list", "deb http://deb.debian.org/debian bullseye main contrib\n")
		}, "not the ones the plan was made from"},
		{"a source file added", good, func(b *shellBox) { b.write("etc-apt/sources.list.d/new.list", "# new\n") }, "not the ones the plan was made from"},
		{"a file that apt does not read is added", good, func(b *shellBox) { b.write("etc-apt/sources.list.d/new.list.save", "# new\n") }, ""},
		// the plan is good for its 24 hours, strictly: Plan.Expired is true at the very second of its end
		{"the last second of the plan", good, func(b *shellBox) { b.write("fake/now", strconv.FormatInt(expires-1, 10)+"\n") }, ""},
		{"the second the plan ends", good, func(b *shellBox) { b.write("fake/now", strconv.FormatInt(expires, 10)+"\n") }, "older than its 24 hours"},
		{"a plan from last week", good, func(b *shellBox) { b.write("fake/now", strconv.FormatInt(expires+7*86400, 10)+"\n") }, "older than its 24 hours"},
		// the package managers: the processes that do the work stop it, and the line says what was seen (the line of the process list)
		{"apt-get runs", good, func(b *shellBox) { b.write("fake/ps.txt", psTable("apt-get         /usr/bin/apt-get -y upgrade")) }, "a package manager is running (the process list says: apt-get /usr/bin/apt-get -y upgrade)"},
		{"apt runs", good, func(b *shellBox) { b.write("fake/ps.txt", psTable("apt             /usr/bin/apt install foo")) }, "a package manager is running (the process list says: apt /usr/bin/apt install foo)"},
		{"aptitude runs", good, func(b *shellBox) { b.write("fake/ps.txt", psTable("aptitude        aptitude")) }, "a package manager is running (the process list says: aptitude aptitude)"},
		{"dpkg runs", good, func(b *shellBox) { b.write("fake/ps.txt", psTable("dpkg            /usr/bin/dpkg --configure -a")) }, "a package manager is running (the process list says: dpkg /usr/bin/dpkg --configure -a)"},
		{"the daily job runs", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("apt.systemd.dai /bin/sh /usr/lib/apt/apt.systemd.daily lock_is_held update"))
		}, "a package manager is running (the process list says: apt.systemd.dai /bin/sh /usr/lib/apt/apt.systemd.daily lock_is_held update)"},
		{"unattended-upgrade runs", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("unattended-upgr /usr/bin/python3 /usr/bin/unattended-upgrade -d"))
		}, "a package manager is running (the process list says: unattended-upgr /usr/bin/python3 /usr/bin/unattended-upgrade -d)"},
		{"unattended-upgrade runs under the name of its interpreter", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("python3         python3 /usr/bin/unattended-upgrade"))
		}, "a package manager is running (the process list says: python3 python3 /usr/bin/unattended-upgrade)"},
		{"unattended-upgrade runs as a command of its own", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("unattended-upgr /usr/bin/unattended-upgrade --download-only"))
		}, "a package manager is running (the process list says: unattended-upgr /usr/bin/unattended-upgrade --download-only)"},
		// what the line says of a process is printable ASCII and short: a process name is no way to write on the owner's terminal
		{"a process line with a control character and a long line", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("apt-get         /usr/bin/apt-get \x1b[2J "+strings.Repeat("x", 300)))
		}, "a package manager is running (the process list says: apt-get /usr/bin/apt-get [2J xxx"},
		// and what only looks like one does not: the idle helper of unattended-upgrades runs on every box that has the package
		{"the idle helper of unattended-upgrades", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("unattended-upgr /usr/bin/python3 /usr/share/unattended-upgrades/unattended-upgrade-shutdown --wait-for-signal"))
		}, ""},
		{"the idle helper of unattended-upgrades, run as a command", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("unattended-upgr /usr/share/unattended-upgrades/unattended-upgrade-shutdown --wait-for-signal"))
		}, ""},
		{"apt-cacher-ng", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("apt-cacher-ng   /usr/sbin/apt-cacher-ng -c /etc/apt-cacher-ng ForeGround=1"))
		}, ""},
		{"other names that start with apt or dpkg", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("aptly           /usr/bin/aptly serve", "apt-listchanges /usr/bin/python3 /usr/bin/apt-listchanges", "dpkg-query      dpkg-query -W", "tail            tail -f /var/log/unattended-upgrades/unattended-upgrades.log", "vim             vim /etc/apt/apt.conf.d/50unattended-upgrades"))
		}, ""},
		// an editor or a pager that has the file of unattended-upgrade among its arguments is no run of it
		{"an editor on the file of unattended-upgrade", good, func(b *shellBox) {
			b.write("fake/ps.txt", psTable("vim             vim /usr/bin/unattended-upgrade", "less            less /usr/bin/unattended-upgrade", "python3         python3 -m pyflakes /usr/bin/unattended-upgrade"))
		}, ""},
		// ps is run on its own: a box without it, or one where it fails, is a stop and not "nothing is running"
		{"ps fails", good, func(b *shellBox) { b.fake("ps", "echo 'Error, do this: mount -t proc proc /proc' >&2; exit 1") }, "apt-get install procps"},
		{"ps is not there", withText(t, good, "psout=\"$(ps -eo", "psout=\"$(ps-which-is-not-there -eo"), func(b *shellBox) {}, "apt-get install procps"},
		// the tools that blocks 2 and 8 use, looked for before anything is changed
		{"no ss", withText(t, good, "command -v ss ", "command -v ss-which-is-not-there "), func(b *shellBox) {}, "apt-get install iproute2"},
		{"no curl, and Docker's source is to be put back", withText(t, good, "command -v curl ", "command -v curl-which-is-not-there "), func(b *shellBox) {}, "apt-get install curl"},
		// block 3 has begun: this block is for before it
		{"block 3 has begun", good, func(b *shellBox) { os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755) }, "block 3 has begun and did not end"},
		{"block 3 ended", good, func(b *shellBox) {
			os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755)
			b.write("bk/done-sources", "")
		}, "block 3 has begun and ended"},
		{"block 5 has begun", good, func(b *shellBox) { b.write("bk/begun-upgrade", "") }, "the upgrade has begun (block 5 was started): this block is for before it. Do not restore anything. Only forward: "},
		{"block 5 ended", good, func(b *shellBox) { b.write("bk/done-minimal", "") }, "the upgrade has begun (block 5 was started)"},
	} {
		// the name of the machine is typed by the owner in the owner's text, and written by the core in the unit's (TestAutoBlock0StopsWhenTheNameThatIsWrittenIsNotTheMachinesOwn)
		if suiteAuto && (tc.name == "no name typed" || tc.name == "another name typed") {
			continue
		}
		b := newShellBox(t)
		setupVerify(t, b)
		tc.set(b)
		out, code := b.run(tc.rb, "verify")
		if tc.stops == "" {
			if code != 0 || !b.exists("bk/done-verify") {
				t.Errorf("%s: exit %d, %q", tc.name, code, out)
			}
			continue
		}
		if code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || b.exists("bk/done-verify") {
			t.Errorf("%s: exit %d, %q, done %v", tc.name, code, out, b.exists("bk/done-verify"))
		}
	}
	_ = p
}

func TestBlock0SaysThatItDoesNotSeeTheLocksOfApt(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	setupVerify(t, b)
	b.write("fake/ps.txt", psTable("apt-get         apt-get update"))
	out, _ := b.run(typed(rb, "TYPED_HOSTNAME", "testbox"), "verify")
	if !strings.Contains(out, "looks at the processes, not at the file locks of apt and dpkg") {
		t.Errorf("the line says nothing of the locks: %q", out)
	}
	if !suiteAuto && !strings.Contains(rb.Text, "it does not see their file locks") {
		t.Error("the prose of block 0 does not say that the locks are not seen")
	}
}

// withAll is the runbook with a part of its text replaced everywhere: a command that is not there, in every block that asks for it.
func withAll(t *testing.T, rb *Runbook, from, to string) *Runbook {
	t.Helper()
	if !strings.Contains(rb.Text, from) {
		t.Fatalf("%q is not in the runbook", from)
	}
	out := *rb
	out.Text = strings.ReplaceAll(rb.Text, from, to)
	return &out
}

// dpkg is a package manager at work when it was given an option that changes packages; the identity gates of the core run dpkg --print-architecture
// at every GET of the plan, and a look at the database (--audit, -l, -s) changes nothing: none of them is a reason to wait.
func TestBlock0TakesDpkgForAPackageManagerOnlyWhenItChangesPackages(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(rb, "TYPED_HOSTNAME", "testbox")
	// what the stop says of a process is at most 120 printable characters of its line, whatever the process wrote in its arguments
	{
		b := newShellBox(t)
		setupVerify(t, b)
		b.write("fake/ps.txt", psTable("apt-get         /usr/bin/apt-get \x1b[2J "+strings.Repeat("x", 300)))
		out, _ := b.run(good, "verify")
		i := strings.Index(out, "(the process list says: ")
		j := strings.Index(out, "): wait until it ends")
		if i < 0 || j < i {
			t.Fatalf("the stop does not say what it saw: %q", out)
		}
		saw := out[i+len("(the process list says: ") : j]
		if len(saw) != 120-1 || strings.ContainsRune(saw, 0x1b) {
			t.Errorf("what the stop says it saw is %d characters, %q", len(saw), saw)
		}
	}
	for _, tc := range []struct {
		args string
		busy bool
	}{
		{"--configure -a", true}, {"--configure --pending", true}, {"-i /tmp/x.deb", true}, {"--install /tmp/x.deb", true}, {"--unpack /tmp/x.deb", true},
		{"-r foo", true}, {"--remove foo", true}, {"-P foo", true}, {"--purge foo", true}, {"--triggers-only --pending", true},
		{"--status-fd 23 --no-triggers --unpack --auto-deconfigure --recursive /var/cache/apt/archives/partial", true},
		{"--print-architecture", false}, {"--audit", false}, {"-l", false}, {"--compare-versions 1 lt 2", false}, {"-s bash", false}, {"-L bash", false},
		{"--get-selections", false}, {"--print-foreign-architectures", false}, {"--version", false},
	} {
		b := newShellBox(t)
		setupVerify(t, b)
		b.write("fake/ps.txt", psTable("dpkg            /usr/bin/dpkg "+tc.args))
		out, code := b.run(good, "verify")
		switch {
		case tc.busy && (code == 0 || !strings.Contains(out, "a package manager is running (the process list says: dpkg /usr/bin/dpkg "+tc.args+")")):
			t.Errorf("dpkg %s should stop block 0: exit %d, %q", tc.args, code, out)
		case !tc.busy && (code != 0 || !b.exists("bk/done-verify")):
			t.Errorf("dpkg %s is no package manager at work: exit %d, %q", tc.args, code, out)
		}
	}
}

// Blocks 5 and 6 run in tmux, in screen or on a console, and the time to install one is before block 3: block 0 stops, before anything is changed,
// on a machine that has no console here and neither tmux nor screen. It does not ask for the multiplexer to be running (that is blocks 5 and 6).
func TestBlock0AsksForAConsoleOrTmuxOrScreenWhileTheMachineIsStillDebian11(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(rb, "TYPED_HOSTNAME", "testbox")
	// the command names of the check are the ones of the machine; the test machine may have a tmux of its own, so each is made absent by name
	neither := withAll(t, withAll(t, good, "command -v tmux ", "command -v tmux-which-is-not-there "), "command -v screen ", "command -v screen-which-is-not-there ")
	noTmux := withAll(t, good, "command -v tmux ", "command -v tmux-which-is-not-there ")
	for _, tc := range []struct {
		name  string
		rb    *Runbook
		tty   string
		env   []string
		noMux bool
		fake  string
		stops bool
	}{
		{"a bare ssh session, neither installed", neither, "/dev/pts/3", nil, true, "", true},
		{"no terminal at all, neither installed", neither, "not a tty", nil, true, "", true},
		{"a USB serial adapter, neither installed", neither, "/dev/ttyUSB0", nil, true, "", true},
		{"a bare ssh session, tmux installed", good, "/dev/pts/3", nil, true, "", false},
		{"a bare ssh session, only screen installed", noTmux, "/dev/pts/3", nil, true, "screen", false},
		{"inside tmux, neither installed", neither, "/dev/pts/3", nil, false, "", false},
		{"inside screen, neither installed", neither, "/dev/pts/3", []string{"STY=1234.pts-0.box"}, true, "", false},
		{"the console, neither installed", neither, "/dev/tty1", nil, true, "", false},
		{"a serial line, neither installed", neither, "/dev/ttyS0", nil, true, "", false},
		{"the console of a hypervisor, neither installed", neither, "/dev/hvc0", nil, true, "", false},
	} {
		b := newShellBox(t)
		setupVerify(t, b)
		b.write("fake/tty", tc.tty+"\n")
		b.noMux, b.env = tc.noMux, tc.env
		if tc.fake != "" {
			b.fake(tc.fake, "exit 0")
		}
		out, code := b.run(tc.rb, "verify")
		switch {
		case tc.stops && (code == 0 || b.exists("bk/done-verify") || !strings.Contains(out, "STOP: there is no console here, and neither tmux nor screen is installed") || !strings.Contains(out, "apt-get install tmux") || !strings.Contains(out, "still Debian 11")):
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		case !tc.stops && (code != 0 || !b.exists("bk/done-verify")):
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
}

func TestABlockDoesNotRunWhenTheOneBeforeItDidNotEnd(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	for _, id := range []string{"timers", "snapshot", "sources", "download", "minimal", "full", "prereboot", "after"} {
		out, code := b.run(typed(typed(rb, "ACCEPT_REMOVALS", "fuse"), "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON"), id)
		if code == 0 || !strings.Contains(out, "did not end") {
			t.Errorf("block %s: exit %d, %q", id, code, out)
		}
	}
	if b.exists("fake/calls.log") {
		t.Errorf("a command ran: %s", b.read("fake/calls.log"))
	}
}

// ---- the backup directory is private --------------------------------------------------------------------------------------

func TestBlock0MakesTheBackupDirectoryPrivateWhateverTheUmaskAndWhateverChmodDoes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits mean nothing on this file system: proved on Linux")
	}
	_, rb := realRunbook(t)
	b := newShellBox(t)
	setupVerify(t, b)
	os.RemoveAll(filepath.Join(b.dir, "bk"))
	b.umask = "022"
	// chmod does nothing here: what makes the new directory private is mkdir -m 700 itself, with no moment when it is not
	b.fake("chmod", "exit 0")
	out, code := b.run(typed(rb, "TYPED_HOSTNAME", "testbox"), "verify")
	if code != 0 || !b.exists("bk/done-verify") {
		t.Fatalf("exit %d, %q", code, out)
	}
	if m := b.mode("bk"); m != 0o700 {
		t.Errorf("the backup directory made by block 0 is %o", m)
	}
}

func TestEveryBlockMakesTheBackupDirectoryPrivateBeforeItWritesInIt(t *testing.T) {
	_, rb := realRunbook(t)
	write := regexp.MustCompile(`(>>? |cp [^\n]*|tar [^\n]*|mv [^\n]*)"\$BK/`)
	for i, c := range blocks(t, rb.Text) {
		_, rest := heredocs(c)
		private := strings.Index(rest, "mkdir -p -m 700 \"$BK\"; chmod 700 \"$BK\"\n")
		if private < 0 {
			t.Errorf("block %d never makes the backup directory private", i)
			continue
		}
		if w := write.FindStringIndex(rest); w != nil && w[0] < private {
			t.Errorf("block %d writes in the backup directory before it makes it private: %q", i, rest[w[0]:min(len(rest), w[0]+60)])
		}
	}
}

func TestBlock2WritesTheStateInAPrivateDirectoryAndTheArchiveOfEtcIsPrivateToo(t *testing.T) {
	_, rb := realRunbook(t)
	setup := func(b *shellBox) {
		b.umask = "022"
		b.write("bk/done-timers", "")
		b.write("root/etc/hosts", "127.0.0.1 localhost\n")
		b.write("root/etc/shadow", "root:*:19000:0:99999:7:::\n")
		b.write("fake/packages.tsv", "ii \tbash\t5.1-2\nhi \tfuse\t2.9.9-5\n")
		b.write("fake/ss.txt", "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\nLISTEN 0 4096 *:8080 *:*\n")
		b.write("fake/sshd-T.txt", "port 22\npermitrootlogin without-password\n")
		b.write("fake/iptables-now", "/usr/sbin/iptables-nft\n")
		b.write("fake/docker-ids.txt", "abc123\n")
		b.write("fake/docker-inspect.txt", "/p-always policy=always state=running\n")
		b.write("docker.sock", "")
	}
	b := newShellBox(t)
	setup(b)
	if runtime.GOOS != "windows" {
		// an earlier run of the hop, or a directory that someone made, that others can read
		if err := os.Chmod(filepath.Join(b.dir, "bk"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	out, code := b.run(rb, "snapshot")
	if code != 0 || !b.exists("bk/done-snapshot") {
		t.Fatalf("exit %d, %q", code, out)
	}
	for name, want := range map[string]string{
		"bk/holds.txt":                "fuse\n",
		"bk/listening.txt":            "*:8080\n0.0.0.0:22\n",
		"bk/iptables-alternative.txt": "/usr/sbin/iptables-nft\n",
		"bk/containers.txt":           "/p-always policy=always state=running\n",
	} {
		if got := b.read(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	// the directory is private from the first thing that is written in it, the archive of /etc is private whatever the umask
	if got := b.read("fake/bkmode"); got != "drwx------\n" {
		t.Errorf("the backup directory when the first file was written: %q", got)
	}
	if m := b.mode("bk"); m != 0o700 {
		t.Errorf("the backup directory is %o", m)
	}
	if m := b.mode("bk/etc.tar.gz"); m != 0o600 {
		t.Errorf("the archive of /etc is %o", m)
	}
	// and the umask is that of the archive only: the files written before it, and the ones written after it, are written as any file is
	for _, name := range []string{"bk/selections.txt", "bk/listening.txt", "bk/iptables-alternative.txt"} {
		if m := b.mode(name); m != 0o644 {
			t.Errorf("the umask of the block was changed for what is not the archive: %s is %o", name, m)
		}
	}
}

func TestBlock2RunsDockerInspectOnlyWhenThereAreContainers(t *testing.T) {
	_, rb := realRunbook(t)
	for _, tc := range []struct {
		name    string
		ids     string
		want    string
		inspect bool
	}{
		{"none", "", "", false},
		{"two", "abc123\ndef456\n", "/p-always policy=always state=running\n", true},
	} {
		b := newShellBox(t)
		b.write("bk/done-timers", "")
		b.write("root/etc/hosts", "x\n")
		b.write("fake/packages.tsv", "ii \tbash\t5.1-2\n")
		b.write("fake/ss.txt", "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n")
		b.write("fake/docker-ids.txt", tc.ids)
		b.write("fake/docker-inspect.txt", "/p-always policy=always state=running\n")
		b.write("docker.sock", "")
		out, code := b.run(rb, "snapshot")
		if code != 0 {
			t.Fatalf("%s: exit %d, %q", tc.name, code, out)
		}
		if got := b.read("bk/containers.txt"); got != tc.want {
			t.Errorf("%s: containers.txt = %q, want %q", tc.name, got, tc.want)
		}
		if got := strings.Contains(b.read("fake/calls.log"), "docker inspect"); got != tc.inspect {
			t.Errorf("%s: docker inspect called: %v", tc.name, got)
		}
	}
}

func TestBlock2StopsOnceTheUpgradeHasWentOnAndTakesItBackAfterTheRestore(t *testing.T) {
	_, rb := realRunbook(t)
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		stops string
	}{
		{"block 3 ended", func(b *shellBox) { b.write("bk/done-sources", "") }, "done-sources is in"},
		{"block 4 ended", func(b *shellBox) { b.write("bk/done-download", "") }, "done-download is in"},
		{"the point of no return was passed", func(b *shellBox) { b.write("bk/done-minimal", "") }, "the upgrade has begun (block 5 was started)"},
		{"the machine was restarted", func(b *shellBox) { b.write("bk/done-prereboot", "") }, "done-prereboot is in"},
		{"block 3 began and did not end", func(b *shellBox) { os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755) }, "apt-orig is in"},
		// the marker that block 5 writes before apt-get upgrade: from it on the answer is forward, whatever else is or is not there
		{"the upgrade has begun (block 5 crashed in apt)", func(b *shellBox) { b.write("bk/begun-upgrade", "") }, "the upgrade has begun (block 5 was started)"},
	} {
		b := newShellBox(t)
		b.write("bk/done-timers", "")
		b.write("bk/selections.txt", "the state of before\n")
		tc.set(b)
		out, code := b.run(rb, "snapshot")
		if code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || b.exists("bk/done-snapshot") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		if got := b.read("bk/selections.txt"); got != "the state of before\n" {
			t.Errorf("%s: the state of before was replaced: %q", tc.name, got)
		}
	}
}

// The archive of /etc: tar says 1 when a file changed while it was read (a lease, resolv.conf rewritten by DHCP), which leaves a good archive; any
// other failure is a stop in words, and an archive that is not there is a stop too. The extended states are copied when the box has them, and sshd is
// asked with its directory made (a stopped sshd has none).
func TestBlock2SaysWhatTarSaidAndCopiesWhatItShould(t *testing.T) {
	_, rb := realRunbook(t)
	prep := func(b *shellBox) {
		b.write("bk/done-timers", "")
		b.write("root/etc/hosts", "127.0.0.1 localhost\n")
		b.write("fake/packages.tsv", "ii \tbash\t5.1-2\n")
		b.write("fake/ss.txt", "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n")
		b.write("fake/sshd-T.txt", "port 22\npermitrootlogin without-password\n")
	}
	b := newShellBox(t)
	prep(b)
	b.write("extended_states", "Package: fuse\nAuto-Installed: 1\n")
	out, code := b.run(rb, "snapshot")
	if code != 0 || !b.exists("bk/done-snapshot") {
		t.Fatalf("exit %d, %q", code, out)
	}
	if got := b.read("bk/extended_states"); got != "Package: fuse\nAuto-Installed: 1\n" {
		t.Errorf("extended_states was not copied: %q", got)
	}
	// sshd was asked with its directory there, and its answer is in the file
	if !b.exists("run-sshd") || b.read("bk/sshd-keys.txt") != "permitrootlogin without-password\nport 22\n" {
		t.Errorf("sshd: directory %v, keys %q (calls %q)", b.exists("run-sshd"), b.read("bk/sshd-keys.txt"), b.read("fake/calls.log"))
	}
	// no extended states on the box: nothing is copied, and the block still ends
	b = newShellBox(t)
	prep(b)
	out, code = b.run(rb, "snapshot")
	if code != 0 || b.exists("bk/extended_states") {
		t.Errorf("without extended_states: exit %d, %q", code, out)
	}
	// tar
	for _, tc := range []struct {
		name  string
		fake  string
		stops string
	}{
		{"tar says a file changed as it was read", "echo archive > \"$4\"; echo 'tar: etc/resolv.conf: file changed as we read it' >&2; exit 1", ""},
		{"tar fails (the disk is full)", "echo 'tar: write error: No space left on device' >&2; exit 2", "STOP: tar could not make the copy of /etc (exit 2)"},
		{"tar fails hard", "exit 3", "STOP: tar could not make the copy of /etc (exit 3)"},
		{"tar says nothing and writes nothing", "exit 0", "STOP: ./bk/etc.tar.gz was not written: is the disk full (df -h /var/backups)? See what the commands above printed, free some room, then run this block again."},
	} {
		b := newShellBox(t)
		prep(b)
		b.fake("tar", tc.fake)
		out, code := b.run(rb, "snapshot")
		switch {
		case tc.stops == "" && (code != 0 || !b.exists("bk/done-snapshot") || !strings.Contains(out, "a file changed while it archived it")):
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		case tc.stops != "" && (code == 0 || b.exists("bk/done-snapshot") || !strings.Contains(out, tc.stops)):
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
}

func TestBlock1StopsTheTimersThatRunAndRemembersWhichOnes(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	b.write("bk/done-verify", "")
	b.write("fake/active-apt-daily.timer", "")
	b.write("fake/active-unattended-upgrades.service", "")
	out, code := b.run(rb, "timers")
	if code != 0 || !b.exists("bk/done-timers") {
		t.Fatalf("exit %d, %q", code, out)
	}
	// the state is ONE file, outside the folder of the plan, root's alone
	const want = "apt-daily.timer active\napt-daily-upgrade.timer inactive\nunattended-upgrades.service active\n"
	if got := b.read("timers.state"); got != want {
		t.Errorf("the state of the timers = %q", got)
	}
	if b.exists("bk/units.state") {
		t.Error("the state of the timers is in the folder of the plan")
	}
	if runtime.GOOS != "windows" {
		if m := b.mode("timers.state"); m != 0o600 {
			t.Errorf("the state of the timers is %o", m)
		}
	}
	calls := b.read("fake/calls.log")
	if !strings.Contains(calls, "systemctl stop apt-daily.timer") || !strings.Contains(calls, "systemctl stop unattended-upgrades.service") || strings.Contains(calls, "systemctl stop apt-daily-upgrade.timer") {
		t.Errorf("calls = %q", calls)
	}
	// run again, the timers are stopped now: what the first run saw is kept, and block 8 starts those back
	os.Remove(filepath.Join(b.dir, "bk", "done-timers"))
	out, code = b.run(rb, "timers")
	if code != 0 || !b.exists("bk/done-timers") {
		t.Fatalf("the second run: exit %d, %q", code, out)
	}
	if got := b.read("timers.state"); got != want {
		t.Errorf("the second run replaced the state of the timers: %q", got)
	}
	if left := stateLeftovers(t, b); len(left) > 0 {
		t.Errorf("a part of the state of the timers is left: %v", left)
	}
	// another plan is another id and another folder (a removal or a hold that changed): its block 1 finds the timers stopped, and adopts the state of the first
	os.RemoveAll(filepath.Join(b.dir, "bk"))
	os.MkdirAll(filepath.Join(b.dir, "bk"), 0o755)
	b.write("bk/done-verify", "")
	out, code = b.run(rb, "timers")
	if code != 0 || !b.exists("bk/done-timers") || !strings.Contains(out, "is there from an earlier run of this block (by this plan or by another)") {
		t.Fatalf("the block of another plan: exit %d, %q", code, out)
	}
	if got := b.read("timers.state"); got != want {
		t.Errorf("the block of another plan replaced the state of the timers: %q", got)
	}
	// a timer that does not stop is a stop
	b2 := newShellBox(t)
	b2.write("bk/done-verify", "")
	b2.write("fake/active-apt-daily.timer", "")
	b2.fake("systemctl", `case "$1" in is-active) if [ -f "$FAKE/active-$2" ]; then echo active; else echo inactive; exit 3; fi;; esac; exit 0`)
	out, code = b2.run(rb, "timers")
	if code == 0 || !strings.Contains(out, "STOP: apt-daily.timer is still running") || b2.exists("bk/done-timers") {
		t.Errorf("a timer that stays: exit %d, %q", code, out)
	}
}

// ---- block 6, 5: the terminal that survives, the summary, the ladder --------------------------------------------------------

func TestBlock6GoesOnUntilNothingIsLeftAndStopsWhenSomethingIs(t *testing.T) {
	_, rb := realRunbook(t)
	left := "Reading package lists...\n12 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		stops string
		calls []string
		not   []string
	}{
		{"converges at once", func(b *shellBox) {}, "",
			[]string{"apt-get " + aptqOpts() + " --download-only dist-upgrade",
				"apt-get " + aptqOpts() + " dist-upgrade"},
			[]string{"dpkg --configure", "install"}},
		{"converges at the second pass", func(b *shellBox) { b.write("fake/sim1.txt", left) }, "",
			[]string{"dist-upgrade"}, []string{"dpkg --configure"}},
		{"converges at the third pass", func(b *shellBox) { b.write("fake/sim1.txt", left); b.write("fake/sim2.txt", left) }, "", nil, nil},
		{"never converges", func(b *shellBox) { b.write("fake/sim.txt", left) }, "apt still has something to do", nil, nil},
		{"the first dist-upgrade fails: the ladder", func(b *shellBox) { b.write("fake/dist-fails-1", "") }, "",
			[]string{"dpkg --configure -a --force-confdef --force-confold", "-f install"}, nil},
		// the check at the end waits for the lock of dpkg, as the other commands do (DPkg::Lock::Timeout is in the options of aptq)
		{"the check at the end waits for the lock", func(b *shellBox) {}, "",
			[]string{"-o DPkg::Lock::Timeout=300 -o Acquire::Retries=3 -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold check"}, nil},
		{"the download fails", func(b *shellBox) { b.write("fake/download-fails", "") }, "the download of the full upgrade did not go through", nil, nil},
		{"the ladder: dpkg fails", func(b *shellBox) { b.write("fake/dist-fails-1", ""); b.write("fake/dpkg-fails", "") }, "dpkg could not configure", nil, nil},
		{"the ladder: the repair fails", func(b *shellBox) { b.write("fake/dist-fails-1", ""); b.write("fake/fix-fails", "") }, "apt could not repair", nil, nil},
		{"the ladder: it fails again", func(b *shellBox) { b.write("fake/dist-fails-1", ""); b.write("fake/dist-fails-2", "") }, "failed again after the repair", nil, nil},
		{"a catch-up pass fails", func(b *shellBox) { b.write("fake/sim1.txt", left); b.write("fake/dist-fails-2", "") }, "a catch-up pass failed", nil, nil},
		{"dpkg is not quiet at the end", func(b *shellBox) { b.write("fake/audit.txt", "half\n") }, "half installed", nil, nil},
		{"apt-get check fails at the end", func(b *shellBox) { b.write("fake/check-fails", "") }, "broken dependency", nil, nil},
		// the catch-up passes look at their own simulation first, as block 5 did: nothing is installed when it would remove a protected package or an essential one,
		// downgrade, or when it is not a text that is understood
		{"a catch-up pass would remove a protected package", func(b *shellBox) { b.write("fake/sim1.txt", resimOf("fuse", "samba-libs")) }, "samba-libs, which is protected", nil, nil},
		{"a catch-up pass would remove an essential package", func(b *shellBox) {
			b.write("fake/sim1.txt", resimOf("an-essential"))
			b.fake("dpkg-query", "echo 'yes required'")
		}, "an-essential, which is essential or required", nil, nil},
		{"a catch-up pass would downgrade", func(b *shellBox) {
			b.write("fake/sim1.txt", "Reading package lists...\nThe following packages will be DOWNGRADED:\n  docker-ce\n0 upgraded, 0 newly installed, 1 downgraded, 0 to remove and 0 not upgraded.\n")
		}, "apt would downgrade packages", nil, nil},
		{"a catch-up pass whose simulation has no summary", func(b *shellBox) {
			b.write("fake/sim1.txt", "E: Unable to correct problems, you have held broken packages.\n")
		}, "apt printed no summary line", nil, nil},
		{"a catch-up pass whose summary and names disagree", func(b *shellBox) {
			b.write("fake/sim1.txt", "1 upgraded, 0 newly installed, 1 to remove and 0 not upgraded.\n")
		}, "not as many as its summary says", nil, nil},
	} {
		b := newShellBox(t)
		b.write("bk/done-minimal", "")
		tc.set(b)
		out, code := b.run(rb, "full")
		calls := b.read("fake/calls.log")
		if tc.stops != "" && (strings.HasPrefix(tc.name, "a catch-up pass would") || strings.HasPrefix(tc.name, "a catch-up pass whose")) && strings.Count(calls, "Dpkg::Options::=--force-confold dist-upgrade\n") > 1 {
			t.Errorf("%s: the catch-up pass ran after the stop: %s", tc.name, calls)
		}
		switch {
		case tc.stops != "" && (code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || b.exists("bk/done-full")):
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		case tc.stops == "" && (code != 0 || !b.exists("bk/done-full")):
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		for _, want := range tc.calls {
			if !strings.Contains(calls, want) {
				t.Errorf("%s: no call %q in\n%s", tc.name, want, calls)
			}
		}
		for _, not := range tc.not {
			if strings.Contains(calls, not) {
				t.Errorf("%s: a call %q in\n%s", tc.name, not, calls)
			}
		}
	}
	// the timers come back after a restart (they are enabled units): block 6 stops them again before it installs anything, and a timer that stays up is a stop
	b := newShellBox(t)
	b.write("bk/done-minimal", "")
	b.write("fake/active-apt-daily.timer", "")
	if out, code := b.run(rb, "full"); code != 0 || b.exists("fake/active-apt-daily.timer") || !strings.Contains(b.read("fake/calls.log"), "systemctl stop apt-daily.timer") {
		t.Errorf("a timer that was running: exit %d, %q", code, out)
	}
	b = newShellBox(t)
	b.write("bk/done-minimal", "")
	b.fake("systemctl", `case "$1" in is-active) echo active;; esac; exit 0`)
	if out, code := b.run(rb, "full"); code == 0 || !strings.Contains(out, "STOP: apt-daily.timer is still running") || strings.Contains(b.read("fake/calls.log"), "dist-upgrade") || b.exists("bk/done-full") {
		t.Errorf("a timer that stays: exit %d, %q", code, out)
	}
	// block 6 writes no boot: block 7 asks the packages of block 2 whether the kernel that runs is a new one, so a block 6 that is run again on the new boot (after a restart
	// in the middle of it) ends the same way
	b = newShellBox(t)
	b.write("bk/done-minimal", "")
	b.write("bootid", "boot-A\n")
	if out, code := b.run(rb, "full"); code != 0 || b.exists("bk/boot_id-full") {
		t.Errorf("block 6 wrote a boot: exit %d, %q, boot_id-full %q", code, out, b.read("bk/boot_id-full"))
	}
	// at most three simulations and two catch-up installations
	b = newShellBox(t)
	b.write("bk/done-minimal", "")
	b.write("fake/sim.txt", left)
	b.run(rb, "full")
	calls := b.read("fake/calls.log")
	if n := strings.Count(calls, "-s dist-upgrade"); n != 3 {
		t.Errorf("%d simulations", n)
	}
	if n := strings.Count(calls, " dist-upgrade\n") - strings.Count(calls, "-s dist-upgrade\n") - strings.Count(calls, "--download-only dist-upgrade\n"); n != 3 {
		t.Errorf("%d installations of dist-upgrade (the first, and two catch-up passes)", n)
	}
}

// A terminal that dies with the connection kills apt in the middle of the upgrade: blocks 5 and 6 go on in tmux, in screen (the variables
// are the ones that they set), or on a console, and in nothing else.
func TestBlocks5And6RunOnlyWhereADroppedConnectionDoesNotKillApt(t *testing.T) {
	_, rb := realRunbook(t)
	ok := typed(rb, "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	for _, blk := range []struct{ id, prev string }{{"minimal", "download"}, {"full", "minimal"}} {
		for _, tc := range []struct {
			name  string
			tty   string
			env   []string
			noMux bool
			runs  bool
		}{
			{"tmux", "/dev/pts/0", nil, false, true},
			{"screen", "/dev/pts/0", []string{"STY=1234.pts-0.box"}, true, true},
			{"the console", "/dev/tty1", nil, true, true},
			{"the second console", "/dev/tty12", nil, true, true},
			{"a serial line", "/dev/ttyS0", nil, true, true},
			{"the console of a hypervisor", "/dev/hvc0", nil, true, true},
			{"a bare ssh session", "/dev/pts/3", nil, true, false},
			{"no terminal at all", "not a tty", nil, true, false},
			{"a USB serial adapter, which is not a console of the machine", "/dev/ttyUSB0", nil, true, false},
			{"something called tty", "/dev/tty", nil, true, false},
		} {
			b := newShellBox(t)
			b.write("bk/done-"+blk.prev, "")
			b.write("fake/tty", tc.tty+"\n")
			b.noMux = tc.noMux
			b.env = tc.env
			out, code := b.run(ok, blk.id)
			if tc.runs {
				if code != 0 || !b.exists("bk/done-"+blk.id) {
					t.Errorf("%s in %s: exit %d, %q", tc.name, blk.id, code, out)
				}
				continue
			}
			if code == 0 || !strings.Contains(out, "STOP:") || b.exists("bk/done-"+blk.id) || b.exists("fake/calls.log") {
				t.Errorf("%s in %s: exit %d, %q, calls %q", tc.name, blk.id, code, out, b.read("fake/calls.log"))
			}
			// it says what to do, in words that can be typed
			for _, want := range []string{"tmux new-session -s casaos-upgrade", "screen -S casaos-upgrade", "Become root first", "tmux attach -t casaos-upgrade"} {
				if !strings.Contains(out, want) {
					t.Errorf("%s in %s: the stop does not say %q: %q", tc.name, blk.id, want, out)
				}
			}
		}
	}
}

// With neither tmux nor screen installed, and no console, blocks 5 and 6 do not tell to install one where they are: the sources are Debian 12's since
// block 3, and a package installed from them upgrades libc6 with it, outside what block 5 protects. Before the upgrade has begun the answer is to put the
// sources back, install tmux on Debian 11 and redo blocks 3 and 4; once it has begun there is no going back, and the answer is the console.
func TestBlocks5And6DoNotTellToInstallTmuxWhereTheSourcesAreDebian12(t *testing.T) {
	_, rb := realRunbook(t)
	ok := typed(rb, "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	neither := withAll(t, withAll(t, ok, "command -v tmux ", "command -v tmux-which-is-not-there "), "command -v screen ", "command -v screen-which-is-not-there ")
	for _, tc := range []struct {
		name, id, prev string
		begun          bool
	}{
		{"block 5, before the upgrade has begun", "minimal", "download", false},
		{"block 5 run again after it began", "minimal", "download", true},
		{"block 6", "full", "minimal", true},
	} {
		b := newShellBox(t)
		b.write("bk/done-"+tc.prev, "")
		if tc.begun {
			b.write("bk/begun-upgrade", "")
		}
		b.noMux = true
		b.write("fake/tty", "/dev/pts/3\n")
		out, code := b.run(neither, tc.id)
		if code == 0 || !strings.Contains(out, "STOP: this block must not run in a terminal that dies with the connection") || !strings.Contains(out, "Neither tmux nor screen is installed") ||
			b.exists("bk/done-"+tc.id) || b.exists("fake/calls.log") || strings.Contains(out, "tmux new-session") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		back := strings.Contains(out, "restore-sources.sh   then   apt-get install tmux") && strings.Contains(out, "redo blocks 3 and 4")
		if back == tc.begun || (tc.begun && !strings.Contains(out, "do not install anything by hand now, and do not restore the sources")) {
			t.Errorf("%s: the way out it says is wrong (the sources go back: %v, the upgrade has begun: %v): %q", tc.name, back, tc.begun, out)
		}
	}
}

func TestBlock5NeedsTheConfirmationAndStopsAfterTheMinimalUpgradeIfAptSaysSo(t *testing.T) {
	_, rb := realRunbook(t)
	ok := typed(rb, "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	b := newShellBox(t)
	b.write("bk/done-download", "")
	var out string
	var code int
	// the confirmation is typed by the owner in the owner's text, and baked into the unit's (TestAutoBlock5HasTheConfirmationBakedIn)
	if !suiteAuto {
		out, code = b.run(rb, "minimal")
		if code == 0 || !strings.Contains(out, "point of no return") || b.exists("fake/calls.log") || b.exists("bk/begun-upgrade") {
			t.Errorf("without the confirmation: exit %d, %q, calls %q", code, out, b.read("fake/calls.log"))
		}
		out, code = b.run(typed(rb, "CONFIRM", "yes"), "minimal")
		if code == 0 || b.exists("fake/calls.log") || b.exists("bk/begun-upgrade") {
			t.Errorf("with another word: exit %d, %q", code, out)
		}
	}
	out, code = b.run(ok, "minimal")
	if code != 0 || !b.exists("bk/done-minimal") || !strings.Contains(b.read("fake/calls.log"), " upgrade\n") {
		t.Errorf("confirmed: exit %d, %q", code, out)
	}
	// the marker of the point of no return is there before the first command that changes a package, and the timers were stopped before it
	if b.read("fake/begun-at-upgrade") != "yes\n" || !b.exists("bk/begun-upgrade") {
		t.Errorf("begun-upgrade at the time of apt-get upgrade: %q", b.read("fake/begun-at-upgrade"))
	}
	if calls := b.read("fake/calls.log"); !strings.Contains(calls, "systemctl is-active apt-daily.timer") {
		t.Errorf("block 5 did not look at the timers: %q", calls)
	}
	// a timer that comes back after a restart is stopped again; one that cannot be stopped is a stop, before anything has begun
	b = newShellBox(t)
	b.write("bk/done-download", "")
	b.write("fake/active-apt-daily.timer", "")
	out, code = b.run(ok, "minimal")
	if code != 0 || b.exists("fake/active-apt-daily.timer") || !strings.Contains(b.read("fake/calls.log"), "systemctl stop apt-daily.timer") {
		t.Errorf("a timer that was running: exit %d, %q", code, out)
	}
	b = newShellBox(t)
	b.write("bk/done-download", "")
	b.fake("systemctl", `case "$1" in is-active) echo active;; esac; exit 0`)
	out, code = b.run(ok, "minimal")
	if code == 0 || !strings.Contains(out, "STOP: apt-daily.timer is still running") || b.exists("bk/begun-upgrade") || b.exists("bk/done-minimal") || strings.Contains(b.read("fake/calls.log"), "upgrade") {
		t.Errorf("a timer that stays: exit %d, %q, calls %q", code, out, b.read("fake/calls.log"))
	}
	// the minimal upgrade fails after it changed something (dpkg's status was rewritten): it has begun, nothing is restored, and the text says what mends it: the commands
	// of the measured ladder, and the confirmation to type again (the block asks for it at every run)
	b = newShellBox(t)
	b.write("bk/done-download", "")
	b.write("fake/upgrade-fails", "")
	b.write("fake/upgrade-touches-status", "")
	out, code = b.run(ok, "minimal")
	if code == 0 || !strings.Contains(out, "do not restore anything") || b.exists("bk/done-minimal") || !b.exists("bk/begun-upgrade") {
		t.Errorf("a failed minimal upgrade: exit %d, %q", code, out)
	}
	for _, want := range []string{
		"dpkg --configure -a --force-confdef --force-confold",
		"DEBIAN_FRONTEND=noninteractive apt-get -y -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold -f install",
		"type the confirmation again and run this block again",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("a failed minimal upgrade does not say %q: %q", want, out)
		}
	}
	if strings.Contains(out, "without the confirmation") {
		t.Errorf("it tells to run the block without a confirmation, which the block refuses: %q", out)
	}
	// a protected package in the simulation after it
	b = newShellBox(t)
	b.write("bk/done-download", "")
	b.write("fake/sim.txt", resimOf("fuse", "samba-libs"))
	out, code = b.run(ok, "minimal")
	if code == 0 || !strings.Contains(out, "samba-libs, which is protected") || !strings.Contains(out, "DO NOT UNDO ANYTHING") || b.exists("bk/done-minimal") {
		t.Errorf("a protected removal: exit %d, %q", code, out)
	}
}

// What the guard of block 5 reads is the names that apt lists as removed, and it reads no name when the text is not the one it knows: a text that
// has a removal in its summary and no list of names, or no summary at all, is no "nothing is removed" (the plan's parser refuses it as plan_unparsed).
func TestBlock5DoesNotTakeATextItDoesNotKnowForNothingToRemove(t *testing.T) {
	_, rb := realRunbook(t)
	ok := typed(rb, "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	for _, tc := range []struct {
		name  string
		sim   string
		stops string
	}{
		{"a removal in the summary and no names", "Reading package lists...\nRemv samba-libs [2:4.13.13+dfsg-1~deb11u6]\n1 upgraded, 0 newly installed, 1 to remove and 0 not upgraded.\n", "not as many as its summary says"},
		{"fewer names than the summary says", "The following packages will be REMOVED:\n  fuse\n1 upgraded, 0 newly installed, 2 to remove and 0 not upgraded.\n", "not as many as its summary says"},
		{"more names than the summary says", "The following packages will be REMOVED:\n  fuse cron\n1 upgraded, 0 newly installed, 1 to remove and 0 not upgraded.\n", "not as many as its summary says"},
		{"no summary", "Reading package lists...\nThe following packages will be REMOVED:\n  samba-libs\n", "apt printed no summary line"},
		{"nothing", "", "apt printed no summary line"},
		{"as many as it says", "The following packages will be REMOVED:\n  fuse libsemanage1\n1 upgraded, 0 newly installed, 2 to remove and 0 not upgraded.\n", ""},
		// a downgrade is a stop (the -y of the hop stops at it): by its section or by its count
		{"downgrades in the summary", "The following packages will be REMOVED:\n  fuse\n1 upgraded, 0 newly installed, 2 downgraded, 1 to remove and 0 not upgraded.\n", "apt would downgrade packages"},
		{"a downgraded section", "The following packages will be DOWNGRADED:\n  docker-ce\n0 upgraded, 0 newly installed, 1 downgraded, 0 to remove and 0 not upgraded.\n", "apt would downgrade packages"},
		{"a downgraded section and no count", "The following packages will be DOWNGRADED:\n  docker-ce\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n", "apt would downgrade packages"},
	} {
		b := newShellBox(t)
		b.write("bk/done-download", "")
		b.write("fake/sim.txt", tc.sim)
		out, code := b.run(ok, "minimal")
		if tc.stops == "" {
			if code != 0 || !b.exists("bk/done-minimal") {
				t.Errorf("%s: exit %d, %q", tc.name, code, out)
			}
			continue
		}
		if code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || !strings.Contains(out, "do not undo anything") || b.exists("bk/done-minimal") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
}

// ---- block 7 -----------------------------------------------------------------------------------------------------------------

func TestBlock7ChecksTheKernelTheInitrdAndSshdBeforeTheRestart(t *testing.T) {
	_, rb := realRunbook(t)
	setup := func(b *shellBox) {
		b.write("bk/done-full", "")
		b.write("bk/sshd-keys.txt", "port 22\nlistenaddress 0.0.0.0:22\npermitrootlogin without-password\npasswordauthentication no\npubkeyacceptedkeytypes ssh-ed25519\n")
		b.write("bk/iptables-alternative.txt", "/usr/sbin/iptables-nft\n")
		b.write("fake/iptables-now", "/usr/sbin/iptables-nft\n")
		b.write("fake/sshd-T.txt", "port 22\nlistenaddress 0.0.0.0:22\npermitrootlogin without-password\npasswordauthentication no\npubkeyacceptedalgorithms ssh-ed25519,rsa-sha2-512\n")
		// what dpkg has installed: the old kernel, the new one, a metapackage, and what is no kernel to start
		b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-9-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\n")
		// the packages of before the hop (block 2): the kernel that runs was there, the new ones were not
		b.write("bk/packages.tsv", "ii \tbash\t5.1-2\nii \tlinux-image-5.10.0-46-cloud-amd64\t5.10.209-2\nii \tlinux-image-cloud-amd64\t5.10.209-2\n")
		b.write("fake/pkg-systemd-timesyncd", "ii ")
		b.write("boot/vmlinuz-6.1.0-53-cloud-amd64", "k")
		b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", initrdOK)
		b.write("boot/vmlinuz-5.10.0-46-cloud-amd64", "k")
		b.write("boot/grub/grub.cfg", "menuentry { linux /boot/vmlinuz-6.1.0-53-cloud-amd64 }\nmenuentry { linux /boot/vmlinuz-5.10.0-46-cloud-amd64 }\n")
		b.write("bootid-now", "boot-1\n")
	}
	run := func(b *shellBox) (string, int) { return runBlock7(b, rb) }
	b := newShellBox(t)
	setup(b)
	out, code := run(b)
	if code != 0 || !b.exists("bk/done-prereboot") || b.read("bk/boot_id") != "boot-1\n" || b.read("bk/kernel") != "6.1.0-53-cloud-amd64\n" || !b.exists("run-sshd") ||
		!strings.Contains(out, "The checks above passed. What this block does not check (the NOT checked line above) is yours: look at it first. When you have, restart the machine yourself") || strings.Contains(out, "EXCEPT") || strings.Contains(out, "ALL CHECKS PASSED") || !strings.Contains(out, "named differently") ||
		!strings.Contains(out, "The kernel that runs: 5.10.0-46-cloud-amd64. The newest installed: 6.1.0-53-cloud-amd64.") || !strings.Contains(out, "NOT checked") ||
		strings.Contains(out, "WARNING") {
		t.Fatalf("a good machine (an initrd over 5 MiB, so no warning): exit %d, %q", code, out)
	}
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		stops string
	}{
		{"no kernel", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "boot", "vmlinuz-6.1.0-53-cloud-amd64")) }, "vmlinuz-6.1.0-53-cloud-amd64 is not there"},
		{"no initrd", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "boot", "initrd.img-6.1.0-53-cloud-amd64")) }, "initrd.img-6.1.0-53-cloud-amd64 is not there"},
		{"an empty initrd", func(b *shellBox) { b.write("boot/initrd.img-6.1.0-53-cloud-amd64", "") }, "initrd.img-6.1.0-53-cloud-amd64 is not there"},
		{"an initrd that is cut short", func(b *shellBox) { b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", 1000) }, "is only 1000 bytes"},
		// the floor of the stop is 1 MiB: under it by one byte is a stop, and it is a stop for the one byte of a file that was cut
		{"an initrd of one byte under 1 MiB", func(b *shellBox) { b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", initrdFloor-1) }, "is only 1048575 bytes"},
		{"an initrd of one byte", func(b *shellBox) { b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", 1) }, "is only 1 bytes"},
		{"an initrd that lsinitramfs cannot open", func(b *shellBox) { b.write("fake/lsinitramfs.rc", "1") }, "lsinitramfs cannot open"},
		// a small initrd is no excuse for one that cannot be opened: the warning is for the size, the stop for what lsinitramfs says
		{"a small initrd that lsinitramfs cannot open", func(b *shellBox) {
			b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", 2<<20)
			b.write("fake/lsinitramfs.rc", "1")
		}, "lsinitramfs cannot open"},
		{"grub does not name the kernel", func(b *shellBox) {
			b.write("boot/grub/grub.cfg", "menuentry { linux /boot/vmlinuz-5.10.0-46-cloud-amd64 }\n")
		}, "grub.cfg does not name the kernel"},
		{"the old kernel is not kept", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "boot", "vmlinuz-5.10.0-46-cloud-amd64")) }, "not kept in /boot"},
		{"sshd does not accept its configuration", func(b *shellBox) { b.write("fake/sshd-t.rc", "255") }, "sshd does not accept its configuration"},
		{"the port of sshd changed", func(b *shellBox) {
			b.write("fake/sshd-T.txt", "port 2222\nlistenaddress 0.0.0.0:22\npermitrootlogin without-password\npasswordauthentication no\n")
		}, "settings of sshd"},
		{"passwords are allowed now", func(b *shellBox) {
			b.write("fake/sshd-T.txt", "port 22\nlistenaddress 0.0.0.0:22\npermitrootlogin without-password\npasswordauthentication yes\n")
		}, "settings of sshd"},
		// the newest kernel is the one of dpkg at the time of the block, not the plan's: the mirror moved on, and its files are not there
		{"the mirror moved on and its kernel is not there", func(b *shellBox) {
			b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-9-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\nii  linux-image-6.1.0-54-cloud-amd64\n")
		}, "vmlinuz-6.1.0-54-cloud-amd64 is not there"},
		{"the plan expects a new kernel and dpkg has none", func(b *shellBox) { b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\n") }, "the plan installs a new kernel"},
		{"dpkg has no kernel at all", func(b *shellBox) { b.write("fake/kernels.txt", "") }, "dpkg has no linux-image package installed"},
		// the systemd-resolved that served as the resolver, and is not installed on Debian 12
		{"the resolver was systemd-resolved (resolv.conf) and it is not installed", func(b *shellBox) {
			b.write("fake/resolv-link", "../run/systemd/resolve/stub-resolv.conf\n")
		}, "Fix: apt-get install systemd-resolved"},
		{"the resolver was systemd-resolved (running) and it is not installed", func(b *shellBox) { b.write("fake/active-systemd-resolved", "") }, "Fix: apt-get install systemd-resolved"},
		{"systemd-resolved was removed and left its configuration", func(b *shellBox) {
			b.write("fake/resolv-link", "/run/systemd/resolve/resolv.conf\n")
			b.write("fake/pkg-systemd-resolved", "rc ")
		}, "Fix: apt-get install systemd-resolved"},
		{"no time daemon", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "fake", "pkg-systemd-timesyncd")) }, "Fix: apt-get install systemd-timesyncd"},
		{"a time daemon that was removed and left its configuration", func(b *shellBox) { b.write("fake/pkg-systemd-timesyncd", "rc ") }, "no time daemon is installed"},
		{"the iptables alternative changed", func(b *shellBox) { b.write("fake/iptables-now", "/usr/sbin/iptables-legacy\n") },
			"Fix: update-alternatives --set iptables /usr/sbin/iptables-nft"},
		{"the iptables alternative appeared", func(b *shellBox) { b.write("bk/iptables-alternative.txt", "") }, "the iptables alternative is not the one of block 2"},
		{"the iptables alternative went", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "fake", "iptables-now")) }, "the iptables alternative is not the one of block 2"},
	} {
		b := newShellBox(t)
		setup(b)
		tc.set(b)
		out, code := run(b)
		if code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || b.exists("bk/done-prereboot") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
	// before the restart the machine still has its DNS: the words are the short ones, with no word of a nameserver by hand
	b = newShellBox(t)
	setup(b)
	b.write("fake/resolv-link", "../run/systemd/resolve/stub-resolv.conf\n")
	if out, code := run(b); code == 0 || !strings.Contains(out, "Fix: apt-get install systemd-resolved   then run this block again.") || strings.Contains(out, "nameserver") {
		t.Errorf("the resolver is not installed, before the restart: exit %d, %q", code, out)
	}
	// what is not a stop
	for _, tc := range []struct {
		name string
		set  func(b *shellBox)
	}{
		{"an initrd of 5 MiB and a byte", func(b *shellBox) {}},
		{"resolv.conf is a file of the box and nothing runs", func(b *shellBox) { b.write("fake/pkg-systemd-timesyncd", "ii ") }},
		{"the resolver was systemd-resolved and it is installed", func(b *shellBox) {
			b.write("fake/resolv-link", "../run/systemd/resolve/stub-resolv.conf\n")
			b.write("fake/pkg-systemd-resolved", "ii ")
		}},
		{"chrony is the time daemon", func(b *shellBox) {
			os.Remove(filepath.Join(b.dir, "fake", "pkg-systemd-timesyncd"))
			b.write("fake/pkg-chrony", "ii ")
		}},
		// each of the daemons that the block names is a time daemon (a mutant that forgets one is seen)
		{"ntp is the time daemon", func(b *shellBox) {
			os.Remove(filepath.Join(b.dir, "fake", "pkg-systemd-timesyncd"))
			b.write("fake/pkg-ntp", "ii ")
		}},
		{"ntpsec is the time daemon", func(b *shellBox) {
			os.Remove(filepath.Join(b.dir, "fake", "pkg-systemd-timesyncd"))
			b.write("fake/pkg-ntpsec", "ii ")
		}},
		{"openntpd is the time daemon", func(b *shellBox) {
			os.Remove(filepath.Join(b.dir, "fake", "pkg-systemd-timesyncd"))
			b.write("fake/pkg-openntpd", "ii ")
		}},
		{"the iptables alternative is the same, whatever it is", func(b *shellBox) {
			b.write("bk/iptables-alternative.txt", "")
			os.Remove(filepath.Join(b.dir, "fake", "iptables-now"))
		}},
		// the mirror moved on to a kernel that the plan did not know, and it is there
		{"the mirror moved on and its kernel is there", func(b *shellBox) {
			b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-9-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\nii  linux-image-6.1.0-54-cloud-amd64\n")
			b.write("boot/vmlinuz-6.1.0-54-cloud-amd64", "k")
			b.sized("boot/initrd.img-6.1.0-54-cloud-amd64", initrdOK)
			b.write("boot/grub/grub.cfg", "menuentry { linux /boot/vmlinuz-6.1.0-54-cloud-amd64 }\n")
		}},
		// what is no kernel to start: the metapackage, the debug symbols, the unsigned twin, a kernel that was removed
		{"packages that are named like kernels and are not", func(b *shellBox) {
			b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-9-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\nii  linux-image-6.1.0-60-cloud-amd64-dbg\nii  linux-image-6.1.0-60-cloud-amd64-unsigned\nrc  linux-image-6.1.0-61-cloud-amd64\nii  linux-image-amd64\n")
		}},
	} {
		b := newShellBox(t)
		setup(b)
		tc.set(b)
		out, code := run(b)
		if code != 0 || !b.exists("bk/done-prereboot") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
	// a small initrd is a machine that was set up with few modules (MODULES=dep) as often as a file that was cut short: from 1 MiB up to
	// 5 MiB included the block warns, with the words to check by hand, and goes on; it stops under 1 MiB and when lsinitramfs fails
	for _, size := range []int64{initrdFloor, 2 << 20, 5 << 20} {
		b := newShellBox(t)
		setup(b)
		b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", size)
		out, code := run(b)
		warn := "initrd.img-6.1.0-53-cloud-amd64 is " + strconv.FormatInt(size, 10) + " bytes"
		if code != 0 || !b.exists("bk/done-prereboot") || strings.Contains(out, "STOP:") || !strings.Contains(out, "WARNING: ") || !strings.Contains(out, warn) || !strings.Contains(out, "check by hand") ||
			!strings.Contains(out, "initrd.img-6.1.0-53-cloud-amd64 | grep") {
			t.Errorf("an initrd of %d bytes should be a warning and no stop: exit %d, %q", size, code, out)
		}
		// the last words do not say that all went well: they say that all did but the warning, which stands above them
		last := out[strings.LastIndex(strings.TrimRight(out, "\n"), "\n")+1:]
		if !strings.HasPrefix(last, "The checks above passed, except what the WARNING above says") || !strings.Contains(last, "restart the machine yourself") || strings.Contains(last, "The checks above passed. ") {
			t.Errorf("an initrd of %d bytes: the last line is %q", size, last)
		}
		// the warning comes after the size is read and before the end of the block
		if i, j := strings.Index(out, "WARNING"), strings.Index(out, "The checks above passed"); i < 0 || j < i {
			t.Errorf("an initrd of %d bytes: the warning is not before the end of the block: %q", size, out)
		}
	}
	// a plan that installs no kernel: the newest kernel is the one that runs, and its files are looked at all the same
	p := *realPlan(t)
	p.NewPackages = slices.DeleteFunc(slices.Clone(p.NewPackages), func(n string) bool { return strings.HasPrefix(n, "linux-image-") })
	noKernel := buildFor(t, p)
	b = newShellBox(t)
	setup(b)
	b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\n")
	b.sized("boot/initrd.img-5.10.0-46-cloud-amd64", initrdOK)
	out, code = runBlock7(b, noKernel)
	if code != 0 || !b.exists("bk/done-prereboot") {
		t.Errorf("a plan without a new kernel: exit %d, %q", code, out)
	}
	os.Remove(filepath.Join(b.dir, "boot", "initrd.img-5.10.0-46-cloud-amd64"))
	os.Remove(filepath.Join(b.dir, "bk", "done-prereboot"))
	out, code = runBlock7(b, noKernel)
	if code == 0 || !strings.Contains(out, "initrd.img-5.10.0-46-cloud-amd64 is not there") {
		t.Errorf("a plan without a new kernel and no initrd for the running one: exit %d, %q", code, out)
	}
	// no lsinitramfs on the box: it is said, and the machine is not declared fit
	b = newShellBox(t)
	setup(b)
	out, code = runBlock7(b, withText(t, rb, "command -v lsinitramfs ", "command -v lsinitramfs-which-is-not-there "))
	if code == 0 || !strings.Contains(out, "lsinitramfs is not there") {
		t.Errorf("without lsinitramfs: exit %d, %q", code, out)
	}
}

// runBlock7 runs block 7, with the boot id read from a file of the test.
func runBlock7(b *shellBox, rb *Runbook) (string, int) {
	rb2 := *rb
	rb2.Text = strings.ReplaceAll(rb.Text, "/proc/sys/kernel/random/boot_id", "./bootid-now")
	return b.run(&rb2, "prereboot")
}

// A restart that comes before block 7 ends (a power cut in the middle of block 5 or 6, a habit) leaves the machine on the new kernel, and block 7 tells it by the
// packages of before the hop (block 2 wrote them): the newest kernel that dpkg has is the one that runs, and it was not installed then. It is no question of the boot of
// block 6: a block 6 that is run again on the new boot, as the table at the end says to do after a restart in the middle of it, must not hide the restart.
func TestBlock7SeesARestartThatCameBeforeItEnded(t *testing.T) {
	_, rb := realRunbook(t)
	const before = "ii \tbash\t5.1-2\nii \tlinux-image-5.10.0-46-cloud-amd64\t5.10.209-2\n"
	setup := func(b *shellBox) {
		b.write("bk/done-full", "")
		b.write("bk/packages.tsv", before)
		b.write("bootid-now", "boot-B\n")
		b.write("fake/uname-r", "6.1.0-53-cloud-amd64\n")
		b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\n")
		b.write("fake/pkg-systemd-timesyncd", "ii ")
		b.write("bk/iptables-alternative.txt", "")
	}
	// the machine runs a kernel that was not there before the hop: whatever the boots say (block 6 writes none), it already runs the new kernel
	for _, tc := range []struct {
		name string
		set  func(b *shellBox)
	}{
		{"a restart before block 7", func(b *shellBox) {}},
		{"the same boot as an earlier text of block 6 wrote: a block 6 run again on the new boot", func(b *shellBox) {
			b.write("bk/boot_id-full", "boot-B\n")
		}},
		{"a boot that block 6 wrote and that is another", func(b *shellBox) { b.write("bk/boot_id-full", "boot-A\n") }},
		{"the kernel that runs was only configuration before the hop", func(b *shellBox) {
			b.write("bk/packages.tsv", "ii \tbash\t5.1-2\nrc \tlinux-image-6.1.0-53-cloud-amd64\t6.1.0\n")
		}},
		// a package whose name only starts with the one of the kernel that runs is another package
		{"a twin of the kernel that runs was there before the hop", func(b *shellBox) {
			b.write("bk/packages.tsv", "ii \tbash\t5.1-2\nii \tlinux-image-6.1.0-53-cloud-amd64-dbg\t6.1.0\n")
		}},
	} {
		b := newShellBox(t)
		setup(b)
		tc.set(b)
		out, code := runBlock7(b, rb)
		if code != 0 || !b.exists("bk/done-prereboot") || b.exists("bk/boot_id") || b.read("bk/kernel") != "6.1.0-53-cloud-amd64\n" ||
			!strings.Contains(out, "it already runs it") || !strings.Contains(out, "it was not installed before the hop") || !strings.Contains(out, "Go on with block 8") || !strings.Contains(out, "Block 7 done.") ||
			strings.Contains(out, "The checks above passed") || !strings.Contains(out, "this block skips them, and makes the ones that stay true after a boot") {
			t.Errorf("%s: exit %d, %q, boot_id %q", tc.name, code, out, b.read("bk/boot_id"))
		}
		// what stays true after a boot is looked at (sshd is asked), and what is for before a restart (the kernel, the initrd, grub: this box has none of them) is not
		if !strings.Contains(b.read("fake/calls.log"), "sshd -t") || strings.Contains(out, "is not there: the machine would not start") {
			t.Errorf("%s: the checks of what stays true did not run, or the ones of before a restart did: %q, %q", tc.name, b.read("fake/calls.log"), out)
		}
	}
	// ... and each of those four is a stop in that path too, with its fix: a machine that started on the new kernel without its resolver, its clock, its firewall or its sshd is no machine to go on with
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		stops string
	}{
		// the machine has started without its resolver: it has no DNS, and the package cannot be fetched until /etc/resolv.conf names a nameserver
		{"the resolver was systemd-resolved and it is not installed", func(b *shellBox) { b.write("fake/resolv-link", "../run/systemd/resolve/stub-resolv.conf\n") },
			"the machine has started without it: it has no DNS, and apt cannot fetch the package"},
		{"no time daemon", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "fake", "pkg-systemd-timesyncd")) }, "Fix: apt-get install systemd-timesyncd"},
		{"the iptables alternative changed", func(b *shellBox) { b.write("bk/iptables-alternative.txt", "/usr/sbin/iptables-nft\n") }, "the iptables alternative is not the one of block 2"},
		{"sshd does not accept its configuration", func(b *shellBox) { b.write("fake/sshd-t.rc", "255") }, "sshd does not accept its configuration"},
		{"the settings of sshd changed", func(b *shellBox) {
			b.write("bk/sshd-keys.txt", "port 22\n")
			b.write("fake/sshd-T.txt", "port 2222\n")
		}, "settings of sshd"},
	} {
		b := newShellBox(t)
		setup(b)
		tc.set(b)
		out, code := runBlock7(b, rb)
		if code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || b.exists("bk/done-prereboot") || b.exists("bk/kernel") {
			t.Errorf("early restart, %s: exit %d, %q", tc.name, code, out)
		}
	}
	// the words of that stop, in their order: the nameserver by hand first (apt-get cannot resolve a name until then), then the package, then the block again
	{
		b := newShellBox(t)
		setup(b)
		b.write("fake/resolv-link", "../run/systemd/resolve/stub-resolv.conf\n")
		out, _ := runBlock7(b, rb)
		order := []string{"replace /etc/resolv.conf", "echo 'nameserver <the address of a DNS server of your network>' > /etc/resolv.conf", "then apt-get install systemd-resolved", "then run this block again."}
		at := -1
		for _, want := range order {
			i := strings.Index(out, want)
			if i < 0 || i < at {
				t.Errorf("early restart, no resolver: %q is not where it should be (%d after %d) in %q", want, i, at, out)
				break
			}
			at = i
		}
		if strings.Contains(out, "Fix: apt-get install systemd-resolved") {
			t.Errorf("early restart, no resolver: the stop tells to install the package first: %q", out)
		}
	}
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		stops string
	}{
		// the machine runs the kernel that it ran before the hop, and dpkg has nothing newer: the new kernel is not installed (whatever the boot is)
		{"the old kernel runs and no new one is installed", func(b *shellBox) {
			b.write("fake/uname-r", "5.10.0-46-cloud-amd64\n")
			b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\n")
		}, "the new kernel is not installed, or its installation did not end. Run block 6 again"},
		// restarted, but on the old kernel (the default of grub) with the new one installed: the machine did not start on the new kernel, and the checks run for the next restart
		{"restarted on the old kernel", func(b *shellBox) { b.write("fake/uname-r", "5.10.0-46-cloud-amd64\n") }, "vmlinuz-6.1.0-53-cloud-amd64 is not there"},
		// a kernel that was on hold before the hop was installed (the second letter of its status says so, the first is the owner's wish)
		{"the old kernel was on hold before the hop, and no new one is installed", func(b *shellBox) {
			b.write("fake/uname-r", "5.10.0-46-cloud-amd64\n")
			b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\n")
			b.write("bk/packages.tsv", "ii \tbash\t5.1-2\nhi \tlinux-image-5.10.0-46-cloud-amd64\t5.10.209-2\n")
		}, "the new kernel is not installed, or its installation did not end. Run block 6 again"},
		{"block 2 left no list of packages", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "bk", "packages.tsv")) }, "packages.tsv is not there"},
	} {
		b := newShellBox(t)
		setup(b)
		tc.set(b)
		out, code := runBlock7(b, rb)
		if code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || b.exists("bk/done-prereboot") || strings.Contains(out, "it already runs it") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
	}
	// the kernel that runs was installed before the hop and is the newest that dpkg has, in a plan that installs no kernel: the usual flow, the checks run and the boot is written
	p := *realPlan(t)
	p.NewPackages = slices.DeleteFunc(slices.Clone(p.NewPackages), func(n string) bool { return strings.HasPrefix(n, "linux-image-") })
	noKernel := buildFor(t, p)
	b := newShellBox(t)
	setup(b)
	b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\n")
	b.write("fake/uname-r", "5.10.0-46-cloud-amd64\n")
	b.write("bootid-now", "boot-A\n")
	b.write("bk/iptables-alternative.txt", "")
	b.write("bk/sshd-keys.txt", "")
	b.write("boot/vmlinuz-5.10.0-46-cloud-amd64", "k")
	b.sized("boot/initrd.img-5.10.0-46-cloud-amd64", initrdOK)
	b.write("boot/grub/grub.cfg", "menuentry { linux /boot/vmlinuz-5.10.0-46-cloud-amd64 }\n")
	b.write("fake/pkg-systemd-timesyncd", "ii ")
	if out, code := runBlock7(b, noKernel); code != 0 || b.read("bk/boot_id") != "boot-A\n" || strings.Contains(out, "it already runs it") {
		t.Errorf("a plan with no new kernel, the kernel that was there: exit %d, %q, boot_id %q", code, out, b.read("bk/boot_id"))
	}
}

// Block 7 says when it cannot look at the boot loader: a machine that starts with another loader than grub has no grub.cfg, and the block does not pass that in silence
// (the closing lines do not say that all went well either).
func TestBlock7SaysWhenThereIsNoGrubConfigToLookAt(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	b.write("bk/done-full", "")
	b.write("bk/packages.tsv", "ii \tlinux-image-5.10.0-46-cloud-amd64\t5.10.209-2\n")
	b.write("bk/sshd-keys.txt", "")
	b.write("bk/iptables-alternative.txt", "")
	b.write("bootid-now", "boot-A\n")
	b.write("fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\n")
	b.write("fake/pkg-systemd-timesyncd", "ii ")
	b.write("boot/vmlinuz-6.1.0-53-cloud-amd64", "k")
	b.write("boot/vmlinuz-5.10.0-46-cloud-amd64", "k")
	b.sized("boot/initrd.img-6.1.0-53-cloud-amd64", initrdOK)
	os.Remove(filepath.Join(b.dir, "boot", "grub", "grub.cfg"))
	out, code := runBlock7(b, rb)
	last := out[strings.LastIndex(strings.TrimRight(out, "\n"), "\n")+1:]
	if code != 0 || !b.exists("bk/done-prereboot") || !strings.Contains(out, "grub.cfg is not there, so this block cannot look at whether the boot loader names the kernel") || !strings.Contains(out, "systemd-boot") ||
		!strings.HasPrefix(last, "The checks above passed, except what the WARNING above says") {
		t.Errorf("no grub.cfg: exit %d, %q", code, out)
	}
	// with the file, and naming the kernel, there is no warning
	b.write("boot/grub/grub.cfg", "menuentry { linux /boot/vmlinuz-6.1.0-53-cloud-amd64 }\n")
	os.Remove(filepath.Join(b.dir, "bk", "done-prereboot"))
	out, code = runBlock7(b, rb)
	if code != 0 || strings.Contains(out, "WARNING") || strings.Contains(out, "except what the WARNING") {
		t.Errorf("a grub.cfg that names the kernel: exit %d, %q", code, out)
	}
}

// The list of new packages is cut at 600 names, sorted, and a kernel sorts late: a plan whose list was cut cannot say that it installs no kernel, so it expects one.
func TestAPlanWhoseListOfNewPackagesWasCutExpectsANewKernel(t *testing.T) {
	p := *realPlan(t)
	p.NewPackages = slices.DeleteFunc(slices.Clone(p.NewPackages), func(n string) bool { return strings.HasPrefix(n, "linux-image-") })
	if expectsNewKernel(p) {
		t.Fatal("a plan whose list has no kernel and is whole expects one")
	}
	p.NewTruncated = true
	if !expectsNewKernel(p) {
		t.Error("a plan whose list was cut, with no kernel in it, does not expect one")
	}
	rb := buildFor(t, p)
	if !strings.Contains(blockOf(t, rb.Text, "prereboot"), "the plan installs a new kernel") {
		t.Error("block 7 of a plan whose list was cut has no stop for a kernel that did not come")
	}
}

// ---- block 8 -----------------------------------------------------------------------------------------------------------------

// setupBlock8 is the machine that block 8 is happy with: restarted, Debian 12 on the kernel that block 7 looked at, the sources of apt naming bookworm, the file of Docker
// put aside, and the lists of before.
func setupBlock8(b *shellBox) {
	b.write("bk/done-prereboot", "")
	b.write("bk/begun-upgrade", "")
	b.write("bk/boot_id", "boot-1\n")
	b.write("bk/kernel", "6.1.0-53-cloud-amd64\n")
	b.write("fake/uname-r", "6.1.0-53-cloud-amd64\n")
	b.write("etc-apt/sources.list", "deb http://deb.debian.org/debian bookworm main\n")
	b.write("bootid", "boot-2\n")
	b.write("os-release", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_CODENAME=bookworm\n")
	b.write("bk/containers.txt", "/p-always policy=always state=running\n")
	b.write("bk/listening.txt", "0.0.0.0:22\n0.0.0.0:8080\n")
	b.write("fake/ss.txt", "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\nLISTEN 0 128 0.0.0.0:9090 0.0.0.0:*\n")
	b.write("timers.state", "apt-daily.timer active\napt-daily-upgrade.timer inactive\nunattended-upgrades.service active\n")
	b.write("etc-apt/sources.list.d/docker.list.casaos-paused", "deb https://download.docker.com/linux/debian bullseye stable\n")
	// what block 2 wrote down: the units that failed, the ReCasaOS units, the files that dpkg had left
	b.write("bk/failed-units.txt", "e2scrub_reap.service\n")
	b.write("bk/casaos-units.txt", "casaos-gateway.service active\n")
	b.write("bk/dpkg-files.txt", "/etc/ssh/sshd_config.dpkg-old\n")
}

// The source of Docker that is put back is 644 as it was, whatever the umask of the shell that the text was pasted into: the file that awk writes is written with umask 022.
func TestBlock8PutsDockersSourceBackWithTheModeItHad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits mean nothing on this file system: proved on Linux")
	}
	_, rb := realRunbook(t)
	for _, umask := range []string{"022", "027", "077"} {
		b := newShellBox(t)
		setupBlock8(b)
		b.umask = umask
		out, code := b.run(rb, "after")
		if code != 0 || !b.exists("bk/done-after") {
			t.Fatalf("umask %s: exit %d, %q", umask, code, out)
		}
		if m := b.mode("etc-apt/sources.list.d/docker.list"); m != 0o644 {
			t.Errorf("umask %s: docker.list is %o", umask, m)
		}
	}
}

func TestBlock8NeedsARestartAndDebian12(t *testing.T) {
	// the plan of a box whose Docker source is a file, and whose other source is a line
	_, rb := realRunbook(t)
	setup := setupBlock8
	b := newShellBox(t)
	setup(b)
	out, code := b.run(rb, "after")
	if code != 0 || !b.exists("bk/done-after") || !strings.Contains(out, "The machine runs: PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"") {
		t.Fatalf("a good restart: exit %d, %q", code, out)
	}
	if b.read("etc-apt/sources.list.d/docker.list") != "deb https://download.docker.com/linux/debian bookworm stable\n" {
		t.Errorf("docker.list = %q", b.read("etc-apt/sources.list.d/docker.list"))
	}
	calls := b.read("fake/calls.log")
	if !strings.Contains(calls, "systemctl start apt-daily.timer") || !strings.Contains(calls, "systemctl start unattended-upgrades.service") || strings.Contains(calls, "systemctl start apt-daily-upgrade.timer") {
		t.Errorf("the timers: %q", calls)
	}
	// the ports that listened before and do not now, and the ones that are new, are said
	if !strings.Contains(out, "do not now (a service may start a little after the machine does):\n0.0.0.0:8080\n") || !strings.Contains(out, "did not before:\n0.0.0.0:9090\n") {
		t.Errorf("the ports are not compared: %q", out)
	}
	if !strings.Contains(out, "/p-always policy=always state=running") {
		t.Errorf("the containers of before are not shown: %q", out)
	}
	for _, tc := range []struct {
		name  string
		set   func(b *shellBox)
		stops string
	}{
		{"not restarted", func(b *shellBox) { b.write("bootid", "boot-1\n") }, "has not been restarted"},
		{"still Debian 11", func(b *shellBox) { b.write("os-release", "VERSION_CODENAME=bullseye\n") }, "not Debian 12"},
		{"dpkg is not quiet", func(b *shellBox) { b.write("fake/audit.txt", "half\n") }, "half installed"},
		// the kernel that runs is the one block 7 looked at: a machine that started on the old one (grub's default) is not done
		{"the old kernel runs", func(b *shellBox) { b.write("fake/uname-r", "5.10.0-46-cloud-amd64\n") }, "the machine runs the kernel 5.10.0-46-cloud-amd64, not 6.1.0-53-cloud-amd64"},
		{"block 7 wrote no kernel", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "bk", "kernel")) }, "kernel is not there"},
		// the sources of apt name the new release: after a restore script run after the upgrade they do not
		{"the sources are Debian 11's again", func(b *shellBox) {
			b.write("etc-apt/sources.list", "deb http://deb.debian.org/debian bullseye main\n")
		}, "the sources of apt do not name bookworm"},
		{"no sources at all", func(b *shellBox) { os.Remove(filepath.Join(b.dir, "etc-apt", "sources.list")) }, "the sources of apt do not name bookworm"},
		{"bookworm only in a comment", func(b *shellBox) {
			b.write("etc-apt/sources.list", "# deb http://deb.debian.org/debian bookworm main\ndeb http://deb.debian.org/debian bullseye main\n")
		}, "the sources of apt do not name bookworm"},
		// Docker cannot be asked: the block stops, and says what to do (an error status from Docker is another thing, below)
		{"curl cannot resolve the name", func(b *shellBox) { b.fake("curl", "exit 6") }, "curl could not reach download.docker.com (exit 6"},
		{"curl cannot connect", func(b *shellBox) { b.fake("curl", "exit 7") }, "curl could not reach download.docker.com (exit 7"},
		{"curl times out", func(b *shellBox) { b.fake("curl", "exit 28") }, "curl could not reach download.docker.com (exit 28"},
		{"curl is not there", func(b *shellBox) {}, ""},
	} {
		b := newShellBox(t)
		setup(b)
		tc.set(b)
		run := rb
		if tc.name == "curl is not there" {
			run = withAll(t, rb, "command -v curl ", "command -v curl-which-is-not-there ")
			tc.stops = "curl is not installed, so Docker's repository cannot be asked about bookworm"
		}
		out, code := b.run(run, "after")
		if suiteAuto && strings.HasPrefix(tc.name, "curl") {
			// the unit goes on: a Docker that cannot be asked is a note for the judge, and its source stays paused (TestAutoBlock8WritesWhyDockersSourceStaysPaused says each answer)
			if code != 0 || strings.Contains(out, "STOP:") || !b.exists("bk/done-after") || !b.exists("bk/docker-source-note") {
				t.Errorf("%s: exit %d, %q", tc.name, code, out)
			}
		} else if code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || b.exists("bk/done-after") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		if strings.HasPrefix(tc.name, "curl") && (b.exists("etc-apt/sources.list.d/docker.list") || !b.exists("etc-apt/sources.list.d/docker.list.casaos-paused")) {
			t.Errorf("%s: Docker's source moved though Docker was not asked", tc.name)
		}
	}
	// block 7 did not write the boot (it found the machine restarted already and ended without the checks): there is no boot to compare, and block 8 goes on
	b = newShellBox(t)
	setup(b)
	os.Remove(filepath.Join(b.dir, "bk", "boot_id"))
	if out, code := b.run(rb, "after"); code != 0 || !b.exists("bk/done-after") {
		t.Errorf("no boot written by block 7: exit %d, %q", code, out)
	}
	// but a boot that was written, and is the one that runs, is a machine that was not restarted
	b = newShellBox(t)
	setup(b)
	b.write("bk/boot_id", "boot-2\n")
	if out, code := b.run(rb, "after"); code == 0 || !strings.Contains(out, "has not been restarted") {
		t.Errorf("the boot of block 7 is the boot that runs: exit %d, %q", code, out)
	}
	// the sources of apt may be in sources.list.d only (an empty sources.list)
	b = newShellBox(t)
	setup(b)
	b.write("etc-apt/sources.list", "# nothing here\n")
	b.write("etc-apt/sources.list.d/debian.list", "deb http://deb.debian.org/debian bookworm main\n")
	if out, code := b.run(rb, "after"); code != 0 || !b.exists("bk/done-after") {
		t.Errorf("sources in sources.list.d only: exit %d, %q", code, out)
	}
	// curl says 22: Docker answers with an error status, which is its way of saying that it publishes nothing for the release: the source stays paused, and the block ends
	b = newShellBox(t)
	setup(b)
	b.fake("curl", "echo 404; exit 22")
	if out, code := b.run(rb, "after"); code != 0 || !b.exists("bk/done-after") || !strings.Contains(out, "publishes nothing for bookworm yet") || b.exists("etc-apt/sources.list.d/docker.list") {
		t.Errorf("curl 22: exit %d, %q", code, out)
	}
	// no container before: it is said, not an empty line
	b = newShellBox(t)
	setup(b)
	b.write("bk/containers.txt", "")
	out, code = b.run(rb, "after")
	if code != 0 || !strings.Contains(out, "(there were none)") {
		t.Errorf("no container before: exit %d, %q", code, out)
	}
	// Docker has nothing for the new release: its source stays paused, and the block still ends
	b = newShellBox(t)
	setup(b)
	b.fake("curl", "echo 404; exit 22")
	out, code = b.run(rb, "after")
	if code != 0 || !strings.Contains(out, "publishes nothing") || !b.exists("etc-apt/sources.list.d/docker.list.casaos-paused") || b.exists("etc-apt/sources.list.d/docker.list") {
		t.Errorf("no Docker release: exit %d, %q", code, out)
	}
}

// ---- block 3 -----------------------------------------------------------------------------------------------------------------

// resimKept is the simulation of the full upgrade of a box where something is kept back: the section and the count that goes with it.
func resimKept(notUpgraded int, section bool, removed ...string) string {
	text := "Reading package lists...\nCalculating upgrade...\n"
	if len(removed) > 0 {
		text += "The following packages will be REMOVED:\n  " + strings.Join(removed, " ") + "\n"
	}
	if section {
		text += "The following packages have been kept back:\n  openssh-server\n"
	}
	text += "The following packages will be upgraded:\n  bash\n"
	return text + "1 upgraded, 0 newly installed, " + itoa(len(removed)) + " to remove and " + itoa(notUpgraded) + " not upgraded.\n"
}

func TestBlock3WritesTheTreeAndStopsOnItsOwnErrors(t *testing.T) {
	p, rb := realRunbook(t)
	setup := func(b *shellBox) {
		b.write("bk/done-snapshot", "")
		for _, f := range d11Box(t) {
			name := f.Name
			if f.InParts {
				name = "sources.list.d/" + name
			}
			b.write("etc-apt/"+name, string(f.Content))
		}
		// apt says what the plan expected
		b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
	}
	good := typed(rb, "ACCEPT_REMOVALS", "fuse")
	b := newShellBox(t)
	setup(b)
	out, code := b.run(good, "sources")
	if code != 0 || !b.exists("bk/done-sources") {
		t.Fatalf("a good block 3: exit %d, %q", code, out)
	}
	if got := b.read("etc-apt/sources.list"); got != p.Sources.Tree[0].Content {
		t.Errorf("sources.list = %q", got)
	}
	if b.exists("etc-apt/sources.list.d/docker.list") || !b.exists("etc-apt/sources.list.d/docker.list.casaos-paused") {
		t.Error("Docker's file is not put aside")
	}
	if b.read("bk/apt-orig/sources.list.d/docker.list") == "" || !b.exists("bk/restore-sources.sh") {
		t.Error("no copy of /etc/apt, or no restore script")
	}
	if !strings.Contains(out, "The plan expected: 384 upgraded") || !strings.Contains(out, "The plan's hash of the sources it wrote for apt: "+p.Sources.HashGenerated) {
		t.Errorf("the expected counts and the hash are not said: %q", out)
	}
	for _, tc := range []struct {
		name  string
		rb    *Runbook
		set   func(b *shellBox)
		stops string
	}{
		{"the removals are not accepted", rb, func(b *shellBox) {}, "type the names of the packages that are removed"},
		{"another name accepted", typed(rb, "ACCEPT_REMOVALS", "fuse libsemanage1"), func(b *shellBox) {}, "type the names of the packages that are removed"},
		{"the block ran before", good, func(b *shellBox) {
			os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755)
			b.write("bk/restore-sources.sh", "# the script\n")
		}, "apt-orig is there already: this block was run before, and the sources of apt may be the ones it wrote. Run sh ./bk/restore-sources.sh"},
		// apt-orig is there and the script is not: the copy is whole (it is put in place only when it is complete), and the words say what to do with it
		{"the block ran before, and the restore script is gone", good, func(b *shellBox) { os.MkdirAll(filepath.Join(b.dir, "bk", "apt-orig"), 0o755) }, "restore-sources.sh is not. apt-orig is the whole copy"},
		// the way is forward once block 5 has begun: no restore, and the block does not write the sources again
		{"the upgrade has begun", good, func(b *shellBox) { b.write("bk/begun-upgrade", "") }, "the upgrade has begun (block 5 was started)"},
		{"block 5 ended", good, func(b *shellBox) { b.write("bk/done-minimal", "") }, "the upgrade has begun (block 5 was started)"},
		// what the block relies on is looked at again, before anything is changed: the 24 hours, and the sources that the plan was made from
		{"the plan is past its 24 hours", good, func(b *shellBox) { b.write("fake/now", strconv.FormatInt(p.ExpiresAt.Unix(), 10)+"\n") }, "this plan is older than its 24 hours"},
		{"the sources changed since block 0", good, func(b *shellBox) {
			b.write("etc-apt/sources.list", "deb http://deb.debian.org/debian bullseye main contrib\n")
		}, "something changed them since block 0"},
		{"a source file was added since block 0", good, func(b *shellBox) { b.write("etc-apt/sources.list.d/new.list", "deb http://x.example/ bullseye main\n") }, "something changed them since block 0"},
		// a downgrade is a stop, as it is for the plan
		{"a package would be downgraded", good, func(b *shellBox) {
			b.write("fake/sim.txt", "Reading package lists...\nThe following packages will be REMOVED:\n  fuse libsemanage1\nThe following packages will be DOWNGRADED:\n  docker-ce\n1 upgraded, 0 newly installed, 1 downgraded, 2 to remove and 0 not upgraded.\n")
		}, "apt would downgrade packages"},
		{"a downgrade in the summary only", good, func(b *shellBox) {
			b.write("fake/sim.txt", "The following packages will be REMOVED:\n  fuse libsemanage1\n1 upgraded, 0 newly installed, 1 downgraded, 2 to remove and 0 not upgraded.\n")
		}, "apt would downgrade packages"},
		{"apt-get update fails", good, func(b *shellBox) { b.write("fake/update-fails", "") }, "apt-get update does not go through"},
		{"another removal", good, func(b *shellBox) { b.write("fake/sim.txt", resimOf("fuse", "libsemanage1", "samba")) }, "a different set of packages"},
		// something is kept back, or is not upgraded, where the plan has none: the plan blocks on it, the box has changed since
		{"a package is kept back", good, func(b *shellBox) { b.write("fake/sim.txt", resimKept(3, true, "fuse", "libsemanage1")) }, "keep packages back, or not upgrade them"},
		{"a package is kept back, with no count", good, func(b *shellBox) { b.write("fake/sim.txt", resimKept(0, true, "fuse", "libsemanage1")) }, "keep packages back, or not upgrade them"},
		{"packages are not upgraded, and none is named (a downgrade on hold)", good, func(b *shellBox) { b.write("fake/sim.txt", resimKept(1, false, "fuse", "libsemanage1")) }, "keep packages back, or not upgrade them"},
		{"no summary", good, func(b *shellBox) {
			b.write("fake/sim.txt", "Reading package lists...\nThe following packages will be REMOVED:\n  fuse libsemanage1\n")
		}, "apt printed no summary line"},
		{"a summary and no names", good, func(b *shellBox) {
			b.write("fake/sim.txt", "1 upgraded, 0 newly installed, 2 to remove and 0 not upgraded.\n")
		}, "not as many as its summary says"},
	} {
		// the removals are accepted by the owner in the owner's text, and written by the core in the unit's (TestAutoBlock3HasTheAcceptedRemovalsWrittenIn)
		if suiteAuto && (tc.name == "the removals are not accepted" || tc.name == "another name accepted") {
			continue
		}
		b := newShellBox(t)
		setup(b)
		tc.set(b)
		out, code := b.run(tc.rb, "sources")
		if code == 0 || !strings.Contains(out, "STOP:") || !strings.Contains(out, tc.stops) || b.exists("bk/done-sources") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		if tc.name == "the removals are not accepted" || tc.name == "another name accepted" {
			// nothing of /etc/apt was touched before the owner accepted
			if b.exists("bk/apt-orig") || b.read("etc-apt/sources.list") != string(d11Box(t)[1].Content) {
				t.Errorf("%s: /etc/apt was touched", tc.name)
			}
		}
		if tc.name == "a package is kept back" && !strings.Contains(out, "sh $BK/restore-sources.sh") && !strings.Contains(out, "restore-sources.sh") {
			t.Errorf("%s: the stop does not say how to put the sources back: %q", tc.name, out)
		}
	}
}

// The files that the block wrote for apt are read back and compared with the hash that the plan made of them, before any of them is put in place:
// a text that a console changed (a line lost, a line wrapped) is seen, where the names of the removals would not see it.
func TestBlock3ReadsWhatItWroteBeforeItMovesAnything(t *testing.T) {
	p, rb := realRunbook(t)
	tree := p.Sources.Tree[len(p.Sources.Tree)-1]
	if tree.Path != "sources.list" {
		t.Fatalf("the last file of the tree is %s", tree.Path)
	}
	var victim string
	for _, line := range strings.Split(tree.Content, "\n") {
		if strings.HasPrefix(line, "deb ") && strings.Contains(line, "security") {
			victim = line
		}
	}
	if victim == "" {
		t.Fatalf("no security line in the tree: %q", tree.Content)
	}
	for _, tc := range []struct {
		name string
		rb   *Runbook
	}{
		{"a line is lost", withText(t, typed(rb, "ACCEPT_REMOVALS", "fuse"), "\n"+victim+"\n", "\n")},
		{"a line is wrapped", withText(t, typed(rb, "ACCEPT_REMOVALS", "fuse"), victim+"\n", victim[:20]+"\n"+victim[20:]+"\n")},
		{"a character is changed", withText(t, typed(rb, "ACCEPT_REMOVALS", "fuse"), victim, strings.Replace(victim, "bookworm", "bookwurm", 1))},
	} {
		b := newShellBox(t)
		b.write("bk/done-snapshot", "")
		setupVerify(t, b)
		b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
		before := b.read("etc-apt/sources.list")
		out, code := b.run(tc.rb, "sources")
		// the stop says what a tab in a comment line does to a paste in bash (type sh first), and that nothing is in place yet
		if code == 0 || !strings.Contains(out, "STOP: the files written for apt are not the plan's") || !strings.Contains(out, "takes a TAB in a comment line of the sources for completion: type sh first, then paste this block again") || b.exists("bk/done-sources") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		// nothing was put in place: the sources are the box's, Docker's file is where it was, and apt was not asked
		if b.read("etc-apt/sources.list") != before || !b.exists("etc-apt/sources.list.d/docker.list") || b.exists("etc-apt/sources.list.d/docker.list.casaos-paused") {
			t.Errorf("%s: something was moved before the check", tc.name)
		}
		if strings.Contains(b.read("fake/calls.log"), "update") {
			t.Errorf("%s: apt was asked: %q", tc.name, b.read("fake/calls.log"))
		}
	}
	// the restore script is read back too: not shell, or not the one of the text, and nothing is changed
	for _, tc := range []struct {
		name  string
		from  string
		to    string
		stops string
	}{
		{"a restore script that is not shell", `if [ -e "$HERE/apt-orig/sources.list" ]; then cp`, `if [ -e "$HERE/apt-orig/sources.list" ]; then then cp`, "the restore script was damaged on its way here"},
		{"a restore script that is another one", "The sources are back as they were. Block 3 can be run again.", "The sources are back.", "the restore script is not the one of this text"},
		{"a restore script that lost a line", "rm -f \"$HERE/done-sources\" \"$HERE/done-download\"\n", "", "the restore script is not the one of this text"},
	} {
		b := newShellBox(t)
		b.write("bk/done-snapshot", "")
		setupVerify(t, b)
		b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
		before := b.read("etc-apt/sources.list")
		out, code := b.run(withText(t, typed(rb, "ACCEPT_REMOVALS", "fuse"), tc.from, tc.to), "sources")
		if code == 0 || !strings.Contains(out, tc.stops) || b.exists("bk/done-sources") {
			t.Errorf("%s: exit %d, %q", tc.name, code, out)
		}
		if b.read("etc-apt/sources.list") != before || !b.exists("etc-apt/sources.list.d/docker.list") || b.exists("etc-apt/sources.list.casaos-new") {
			t.Errorf("%s: /etc/apt was touched", tc.name)
		}
	}
}

// apt-orig is all or nothing: the copy of /etc/apt is made under another name and renamed when it is whole, after the restore script is written and checked.
// A copy that dies half way leaves no apt-orig for a restore script (this one, or one of an earlier cycle) to take for the truth, and the run again makes it.
func TestBlock3MakesAptOrigWholeOrNotAtAll(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(rb, "ACCEPT_REMOVALS", "fuse")
	b := newShellBox(t)
	b.write("bk/done-snapshot", "")
	setupVerify(t, b)
	b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
	// cp -a dies after it made a part of the copy
	b.fake("cp", "mkdir -p \"$3\"; echo partial > \"$3/sources.list\"; exit 1")
	out, code := b.run(good, "sources")
	if code == 0 || b.exists("bk/apt-orig") || b.exists("bk/done-sources") {
		t.Fatalf("a copy that dies: exit %d, %q, apt-orig %v", code, out, b.exists("bk/apt-orig"))
	}
	if !b.exists("bk/restore-sources.sh") || b.read("etc-apt/sources.list") != string(d11Box(t)[1].Content) {
		t.Error("the restore script is not there, or /etc/apt was touched, after a copy that died")
	}
	// run again with a cp that works: the part that was left is not the copy
	os.Remove(filepath.Join(b.dir, "bin", "cp"))
	out, code = b.run(good, "sources")
	if code != 0 || !b.exists("bk/done-sources") {
		t.Fatalf("the run again: exit %d, %q", code, out)
	}
	if b.exists("bk/apt-orig.new") || b.read("bk/apt-orig/sources.list") != string(d11Box(t)[1].Content) || b.read("bk/apt-orig/sources.list.d/docker.list") == "" {
		t.Errorf("apt-orig is not the copy of /etc/apt of before: new %v, sources.list %q", b.exists("bk/apt-orig.new"), b.read("bk/apt-orig/sources.list"))
	}
}

// The files of /etc/apt are 644 as they were, whatever the umask of the shell that the text was pasted into: block 3 writes them with umask 022 (in a subshell,
// so that nothing else in the block changes its umask), and it reads them back where they are put, after the moves, as it read them before.
func TestBlock3WritesTheFilesOfEtcAptWithTheModeTheyHad(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("permission bits mean nothing on this file system: proved on Linux")
	}
	_, rb := realRunbook(t)
	good := typed(rb, "ACCEPT_REMOVALS", "fuse")
	for _, umask := range []string{"022", "027", "077"} {
		b := newShellBox(t)
		b.umask = umask
		b.write("bk/done-snapshot", "")
		setupVerify(t, b)
		b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
		out, code := b.run(good, "sources")
		if code != 0 {
			t.Fatalf("umask %s: exit %d, %q", umask, code, out)
		}
		if m := b.mode("etc-apt/sources.list"); m != 0o644 {
			t.Errorf("umask %s: sources.list is %o", umask, m)
		}
		// and only the files of /etc/apt are written so: what the block writes after them keeps the umask of the shell (dpkg and the scripts of the packages keep theirs too)
		if m := b.mode("bk/done-sources"); m != 0o666&^mode(umask) {
			t.Errorf("umask %s: the marker of the block is %o, the umask of the shell was changed for the rest of the block", umask, m)
		}
	}
}

// mode is a umask, as the number it is.
func mode(umask string) os.FileMode {
	n, _ := strconv.ParseUint(umask, 8, 32)
	return os.FileMode(n)
}

// A mv that does not replace (an alias mv='mv -i' that is answered by the next pasted line, or any other reason) ends the block in "Block 3 done" with the old
// sources in place, which only the names of the removals could notice: the files are read back where they are, and the block uses mv -f besides.
func TestBlock3ReadsTheSourcesBackAfterTheMoves(t *testing.T) {
	_, rb := realRunbook(t)
	good := typed(rb, "ACCEPT_REMOVALS", "fuse")
	// a mv that does what mv -i does when it is not told -f and the destination is there: nothing, with status 0
	mvI := "case \"$1\" in -f) shift; exec /bin/mv \"$@\";; esac\nif [ -e \"$2\" ]; then exit 0; fi\nexec /bin/mv \"$@\""
	setup := func(b *shellBox) {
		b.write("bk/done-snapshot", "")
		setupVerify(t, b)
		b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
		b.fake("mv", mvI)
	}
	b := newShellBox(t)
	setup(b)
	if out, code := b.run(good, "sources"); code != 0 || !b.exists("bk/done-sources") {
		t.Fatalf("with a mv that asks (the text uses mv -f): exit %d, %q", code, out)
	}
	if !strings.Contains(good.Text, "mv -f \"$APT_ETC/sources.list.casaos-new\" \"$APT_ETC/sources.list\"") {
		t.Fatal("the move of sources.list is not a mv -f")
	}
	plain := withText(t, good, "mv -f \"$APT_ETC/sources.list.casaos-new\" \"$APT_ETC/sources.list\"", "mv \"$APT_ETC/sources.list.casaos-new\" \"$APT_ETC/sources.list\"")
	b = newShellBox(t)
	setup(b)
	out, code := b.run(plain, "sources")
	if code == 0 || b.exists("bk/done-sources") || !strings.Contains(out, "STOP: the files now in /etc/apt are not the ones that were written") || !strings.Contains(out, "restore-sources.sh") {
		t.Errorf("a mv that does not replace: exit %d, %q", code, out)
	}
	// the one that put aside Docker's file: its hash is not in the check (it is no file of the tree), but the file must be gone from where apt reads
	if b.exists("etc-apt/sources.list.d/docker.list.casaos-paused") == false {
		t.Error("Docker's file is not put aside")
	}
}

// The timers come back after a restart (they are enabled units): block 3 stops them again, as blocks 5 and 6 do.
func TestBlock3StopsTheTimersAgain(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	b.write("bk/done-snapshot", "")
	setupVerify(t, b)
	b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
	b.write("fake/active-apt-daily-upgrade.timer", "")
	b.write("fake/active-unattended-upgrades.service", "")
	out, code := b.run(typed(rb, "ACCEPT_REMOVALS", "fuse"), "sources")
	calls := b.read("fake/calls.log")
	if code != 0 || !strings.Contains(calls, "systemctl stop apt-daily-upgrade.timer") || !strings.Contains(calls, "systemctl stop unattended-upgrades.service") || strings.Contains(calls, "systemctl stop apt-daily.timer") {
		t.Errorf("exit %d, %q, calls %q", code, out, calls)
	}
	// and one that stays up is a stop, before /etc/apt is touched
	b = newShellBox(t)
	b.write("bk/done-snapshot", "")
	setupVerify(t, b)
	b.fake("systemctl", `case "$1" in is-active) echo active;; esac; exit 0`)
	out, code = b.run(typed(rb, "ACCEPT_REMOVALS", "fuse"), "sources")
	if code == 0 || !strings.Contains(out, "is still running") || b.exists("bk/apt-orig") {
		t.Errorf("a timer that stays: exit %d, %q", code, out)
	}
}

// What the apt-get commands of the blocks run with: no question asked by debconf, by apt-listchanges or by needrestart, and the options of the measured hop but one: the hop ran
// them with Dpkg::Use-Pty=0, and the blocks leave apt's pty on so that term.log is written (TestAptRunsWithItsPtySoThatTermLogHoldsWhatThePackagesPrint). The golden pins the
// text; these pin what the shell does with it.
func TestTheAptCommandsOfTheBlocksRunWithoutAQuestionAndWithTheOptionsOfTheHopButItsPty(t *testing.T) {
	_, rb := realRunbook(t)
	ok := typed(typed(rb, "ACCEPT_REMOVALS", "fuse"), "CONFIRM", "I-HAVE-A-SNAPSHOT-OR-A-CONSOLE-AND-GO-ON")
	for _, tc := range []struct {
		id, prev string
		set      func(b *shellBox)
	}{
		{"sources", "snapshot", func(b *shellBox) { setupVerify(t, b); b.write("fake/sim.txt", resimOf("fuse", "libsemanage1")) }},
		{"download", "sources", func(b *shellBox) { b.write("archives/a.deb", "x") }},
		{"minimal", "download", func(b *shellBox) {}},
		{"full", "minimal", func(b *shellBox) {}},
		{"after", "prereboot", func(b *shellBox) {
			b.write("bk/begun-upgrade", "")
			b.write("bk/boot_id", "boot-1\n")
			b.write("bk/kernel", "5.10.0-46-cloud-amd64\n")
			b.write("bootid", "boot-2\n")
			b.write("os-release", "VERSION_CODENAME=bookworm\n")
			b.write("etc-apt/sources.list", "deb http://deb.debian.org/debian bookworm main\n")
			b.write("timers.state", "apt-daily.timer inactive\n")
			b.write("bk/failed-units.txt", "")
			b.write("bk/casaos-units.txt", "")
			b.write("bk/dpkg-files.txt", "")
			b.write("bk/listening.txt", "")
		}},
	} {
		b := newShellBox(t)
		b.write("bk/done-"+tc.prev, "")
		tc.set(b)
		out, code := b.run(ok, tc.id)
		if code != 0 {
			t.Errorf("block %s: exit %d, %q", tc.id, code, out)
			continue
		}
		env := b.read("fake/env.log")
		if env == "" {
			t.Errorf("block %s ran no apt-get", tc.id)
		}
		for _, line := range strings.Split(strings.TrimSpace(env), "\n") {
			if !strings.HasPrefix(line, "DEBIAN_FRONTEND=noninteractive APT_LISTCHANGES_FRONTEND=none NEEDRESTART_SUSPEND=1 for: ") {
				t.Errorf("block %s: apt-get ran with %q", tc.id, line)
			}
		}
		// the real runs (not the checks, not the simulations) have the options of the measured hop, but its Dpkg::Use-Pty=0
		for _, line := range strings.Split(strings.TrimSpace(b.read("fake/calls.log")), "\n") {
			if !strings.HasPrefix(line, "apt-get ") || strings.HasPrefix(line, "apt-get check") {
				continue
			}
			if !strings.HasPrefix(line, "apt-get "+aptqOpts()+" ") {
				t.Errorf("block %s: %q", tc.id, line)
			}
		}
	}
	// block 3 asks apt-get update to stop at any error
	b := newShellBox(t)
	b.write("bk/done-snapshot", "")
	setupVerify(t, b)
	b.write("fake/sim.txt", resimOf("fuse", "libsemanage1"))
	if out, code := b.run(ok, "sources"); code != 0 || !strings.Contains(b.read("fake/calls.log"), "Dpkg::Options::=--force-confold update --error-on=any\n") {
		t.Errorf("block 3: exit %d, %q, calls %q", code, out, b.read("fake/calls.log"))
	}
}

func TestBlock4DownloadsAndStopsWhenNothingWasDownloaded(t *testing.T) {
	_, rb := realRunbook(t)
	b := newShellBox(t)
	b.write("bk/done-sources", "")
	b.write("archives/bash_5.2_amd64.deb", "x")
	out, code := b.run(rb, "download")
	if code != 0 || !b.exists("bk/done-download") || !strings.Contains(b.read("fake/calls.log"), "--download-only upgrade") {
		t.Fatalf("a download: exit %d, %q", code, out)
	}
	if strings.Contains(b.read("fake/calls.log"), " upgrade\n") && !strings.Contains(b.read("fake/calls.log"), "--download-only upgrade\n") {
		t.Error("an upgrade was installed")
	}
	// the download fails
	b = newShellBox(t)
	b.write("bk/done-sources", "")
	b.write("archives/bash_5.2_amd64.deb", "x")
	b.write("fake/download-fails", "")
	out, code = b.run(rb, "download")
	if code == 0 || !strings.Contains(out, "STOP: the download did not go through") || b.exists("bk/done-download") {
		t.Errorf("a failed download: exit %d, %q", code, out)
	}
	// it says it went through and there is nothing in the cache
	b = newShellBox(t)
	b.write("bk/done-sources", "")
	out, code = b.run(rb, "download")
	if code == 0 || !strings.Contains(out, "STOP: no package was downloaded") || b.exists("bk/done-download") {
		t.Errorf("nothing downloaded: exit %d, %q", code, out)
	}
}

func TestTheShellReadsTheNamesOfSourceFilesAsAptAndTheCodeDo(t *testing.T) {
	_, rb := realRunbook(t)
	fragment := between(t, blockOf(t, rb.Text, "verify"), "srchash() {", "\n}\n") + "\nsrchash\n"
	names := []string{
		"a.list", "with:colon.list", "under_score.list", "dash-ed.list", "UPPER.list", "9digit.list", ":colon.list", "-dash.list", "_us.list",
		"a.sources", "x.y.list", "two.dots.list",
		".hidden.list", "no-ext", "a.list.save", "a.list~", "a.list.dpkg-old", "my file.list", "a;b.list", "a$b.list", "a.LIST", "list", ".list", "a.list.d", "é.list", "a'b.list",
		// a name with a newline in it is no name that apt reads, and the shell must not read the line that ends in .list as one
		"x\nfoo.list", "foo.list\nx", "x\n.list", "\nfoo.list",
	}
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "etc-apt", "sources.list.d"), 0o755)
	var files []hashed
	os.WriteFile(filepath.Join(dir, "etc-apt", "sources.list"), []byte("deb http://x bullseye main\n"), 0o644)
	files = append(files, hashed{path: "sources.list", content: "deb http://x bullseye main\n"})
	made := 0
	for _, name := range names {
		content := "# " + name + "\n"
		if runtime.GOOS == "windows" && (strings.Contains(name, ":") || name == "a.LIST") {
			continue // a colon there makes a stream of a file, and a.LIST is a.list on this file system
		}
		if err := os.WriteFile(filepath.Join(dir, "etc-apt", "sources.list.d", name), []byte(content), 0o644); err != nil {
			continue // a name that this file system cannot have
		}
		made++
		if osreadiness.AptSourceName(name) {
			files = append(files, hashed{path: "sources.list.d/" + name, content: content})
		}
	}
	out, code := runSh(t, dir, "set -eu\nLC_ALL=C; export LC_ALL\n"+strings.Replace(fragment, "cd /etc/apt", "cd ./etc-apt", 1))
	if want := canonicalHash(files); code != 0 || strings.TrimSpace(out) != want {
		t.Errorf("the shell reads the names of apt differently from the code:\n shell %q (exit %d)\n  Go   %s\n  files %d of %d", strings.TrimSpace(out), code, want, len(files), made)
	}
	for _, name := range []string{"x\nfoo.list", "foo.list\nx", "x\n.list", "\nfoo.list"} {
		if osreadiness.AptSourceName(name) {
			t.Errorf("the code reads %q as a name of apt", name)
		}
	}
}
