// Package boxtest is the machine that the tests of the blocks of the upgrade run them on: a folder with the commands of a Debian 11 box replaced by small ones that answer what a test
// says (they are first in the PATH of the shell), the files that the blocks read at the places the folder gives them, and the way to point the text of a block at them. The suites that
// run the blocks (pkg/osplan) and the one that runs a whole upgrade through the core (service) have one machine, and this is it.
//
// It is for tests only: nothing in the product imports it.
package boxtest

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Converged is what the stub of apt-get says of a simulation that has nothing left to do.
const Converged = "Reading package lists...\n0 upgraded, 0 newly installed, 0 to remove and 0 not upgraded.\n"

// Write writes a file of the box, under dir, making the folders of it.
func Write(t testing.TB, dir, rel, content string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// Fake makes a command of the box: a shell script, first in the PATH of the blocks.
func Fake(t testing.TB, dir, name, body string) {
	t.Helper()
	p := filepath.Join(dir, "bin", name)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// Install makes the machine in dir: Debian 11 until a test says otherwise, with every command that a block of the hop or of the refresh runs, and room. now is the clock that the
// stub of date gives (seconds since the epoch), which is the time of the plan, an hour later.
func Install(t testing.TB, dir string, now int64) {
	t.Helper()
	for _, d := range []string{"bin", "fake", "bk", "run", "etc-apt/sources.list.d", "boot/grub", "archives", "root/etc"} {
		if err := os.MkdirAll(filepath.Join(dir, filepath.FromSlash(d)), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	Write(t, dir, "cgroup", "0::/system.slice/casaos-os-upgrade.service\n")
	// sync says whether the marker of the point of no return was on the disk's way when it ran
	Fake(t, dir, "sync", "echo \"sync begun-upgrade=$([ -e ./bk/begun-upgrade ] && echo yes || echo no)\" >> \"$FAKE/calls.log\"")
	Fake(t, dir, "id", "[ \"$1\" = -u ] && echo 0 || echo root")
	Fake(t, dir, "hostname", "echo testbox")
	Fake(t, dir, "uname", "if [ \"$1\" = -r ]; then if [ -f \"$FAKE/uname-r\" ]; then cat \"$FAKE/uname-r\"; else echo 5.10.0-46-cloud-amd64; fi; else echo Linux; fi")
	Fake(t, dir, "tmux", "exit 0")
	Fake(t, dir, "df", "p=\"$2\"; k=\"$(echo \"$p\" | tr / _)\"; if [ -f \"$FAKE/avail$k\" ]; then a=$(cat \"$FAKE/avail$k\"); else a=$(cat \"$FAKE/avail\"); fi; echo Filesystem 1024-blocks Used Available Capacity Mounted; echo \"/dev/x 1 1 $a 1% $p\"")
	Fake(t, dir, "ps", "[ -f \"$FAKE/ps.txt\" ] && cat \"$FAKE/ps.txt\" || true")
	Fake(t, dir, "date", "if [ \"$1\" = +%s ]; then cat \"$FAKE/now\"; else /bin/date \"$@\"; fi")
	Fake(t, dir, "tty", "cat \"$FAKE/tty\" 2>/dev/null || echo /dev/pts/0")
	Fake(t, dir, "ss", "cat \"$FAKE/ss.txt\" 2>/dev/null || true")
	Fake(t, dir, "readlink", "[ -f \"$FAKE/resolv-link\" ] && cat \"$FAKE/resolv-link\" || exit 1")
	Fake(t, dir, "lsinitramfs", "exit $(cat \"$FAKE/lsinitramfs.rc\" 2>/dev/null || echo 0)")
	Fake(t, dir, "update-alternatives", `case "$*" in
  *"--query iptables"*) if [ -f "$FAKE/iptables-now" ]; then printf 'Name: iptables\nLink: /usr/sbin/iptables\nStatus: auto\nValue: %s\n' "$(cat "$FAKE/iptables-now")"; else echo "update-alternatives: error: no alternatives for iptables" >&2; exit 2; fi ;;
esac`)
	Fake(t, dir, "dpkg-query", `for last; do :; done
case "$last" in
  linux-image-*) grep " linux-image-[0-9]" "$FAKE/kernels.txt" 2>/dev/null || true ;;
  -f*) cat "$FAKE/packages.tsv" 2>/dev/null || true ;;
  *) case "$*" in
       *'${Essential}'*) echo " optional" ;;
       *) if [ -f "$FAKE/pkg-$last" ]; then cat "$FAKE/pkg-$last"; else echo "dpkg-query: no packages found matching $last" >&2; exit 1; fi ;;
     esac ;;
esac`)
	Fake(t, dir, "docker", `echo "docker $*" >> "$FAKE/calls.log"
case "$1" in
  ps) if [ "$2" = -aq ]; then cat "$FAKE/docker-ids.txt" 2>/dev/null || true; else cat "$FAKE/docker-ps.txt" 2>/dev/null || true; fi ;;
  inspect) if [ "$#" -lt 4 ]; then echo "docker inspect requires at least 1 argument."; exit 1; fi; cat "$FAKE/docker-inspect.txt" ;;
esac
exit 0`)
	// curl says the status that it was asked to write (-w) as FAKE_CURL_CODE, and ends with FAKE_CURL_EXIT
	Fake(t, dir, "curl", "echo \"curl $*\" >> \"$FAKE/calls.log\"; case \"$*\" in *http_code*) echo \"${FAKE_CURL_CODE:-200}\" ;; esac; exit ${FAKE_CURL_EXIT:-0}")
	Fake(t, dir, "dkms", "cat \"$FAKE/dkms.txt\" 2>/dev/null || true")
	Fake(t, dir, "debconf-communicate", `read -r cmd
case "$cmd" in
  "get grub-pc/install_devices") echo "0 $(cat "$FAKE/grub-devices" 2>/dev/null)" ;;
  "get grub-pc/install_devices_empty") echo "0 $(cat "$FAKE/grub-empty" 2>/dev/null || echo false)" ;;
  *) echo "10 $cmd doesn't exist" ;;
esac`)
	// sshd -t and sshd -T need /run/sshd, which only a running ssh.service makes: without it -t fails (255) and -T says so and prints nothing
	Fake(t, dir, "sshd", "echo \"sshd $*\" >> \"$FAKE/calls.log\"\nif [ ! -d ./run-sshd ]; then echo 'Missing privilege separation directory: /run/sshd' >&2; [ \"$1\" = -t ] && exit 255; exit 0; fi\ncase \"$1\" in -t) exit $(cat \"$FAKE/sshd-t.rc\" 2>/dev/null || echo 0);; -T) cat \"$FAKE/sshd-T.txt\" 2>/dev/null || true;; esac")
	Fake(t, dir, "dpkg", `echo "dpkg $*" >> "$FAKE/calls.log"
case "$1" in
  --audit) if [ -f "$FAKE/audit-error.txt" ]; then cat "$FAKE/audit-error.txt" >&2; exit 2; fi; cat "$FAKE/audit.txt" 2>/dev/null || true ;;
  --get-selections) ls -ld ./bk | awk '{ print $1 }' > "$FAKE/bkmode"; echo "bash install" ;;
  --configure) [ ! -f "$FAKE/dpkg-fails" ] || exit 1 ;;
esac
exit 0`)
	Fake(t, dir, "systemctl", `echo "systemctl $*" >> "$FAKE/calls.log"
case "$1" in
  is-active) if [ -f "$FAKE/active-$2" ]; then echo active; else echo inactive; exit 3; fi ;;
  stop) rm -f "$FAKE/active-$2" ;;
  start) : > "$FAKE/active-$2" ;;
  --failed) cat "$FAKE/failed.txt" 2>/dev/null || true ;;
  list-units) cat "$FAKE/casaos-units.txt" 2>/dev/null || true ;;
esac
exit 0`)
	Fake(t, dir, "apt-get", `echo "apt-get $*" >> "$FAKE/calls.log"
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
	Write(t, dir, "fake/avail", "99999999\n")
	Write(t, dir, "dpkg-status", "Package: bash\nStatus: install ok installed\n")
	// the box is Debian 11 until a test says otherwise: every block after the first stops on a machine that is not, unless its plan has begun the upgrade
	Write(t, dir, "os-release", "VERSION_CODENAME=bullseye\n")
	Write(t, dir, "bootid", "boot-0\n")
	Write(t, dir, "fake/sim.txt", Converged)
	Write(t, dir, "fake/now", strconv.FormatInt(now, 10)+"\n")
}

// PathVars say where a block's own words point in the box.
type PathVars struct {
	// PlanID is the plan whose backup folder the blocks name (BK=/var/backups/casaos-os-upgrade-<PlanID>); for a refresh, the folder is named by BKFrom.
	PlanID string
	// BKFrom is the line that the block has for its backup folder, if it is not the plan's: "BK=/var/backups/casaos-os-upgrade-refresh-<run id>".
	BKFrom string
	// BK is the folder it points at: "./bk" in the box, or an absolute path where the core reads the same folder.
	BK string
	// TimersState is the file that holds the state of the timers, as the text names it.
	TimersState string
	// Cgroup is the file that the gate of an auto block reads in place of /proc/self/cgroup, when the text was written with the core's own words for it (the core gives no file); a test
	// that writes the text with a file of its own leaves it empty.
	Cgroup string
}

// Paths points the paths of the machine that a block names at the box. The text is the block's own but for those, and nothing else of it is changed.
func Paths(code string, v PathVars) string {
	bk := v.BK
	if bk == "" {
		bk = "./bk"
	}
	from := v.BKFrom
	if from == "" {
		from = "BK=/var/backups/casaos-os-upgrade-" + v.PlanID
	}
	pairs := []string{
		"PATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH", "PATH=\"$PWD/bin:$PATH\"; export PATH",
		from, "BK=" + bk,
		"TS=" + v.TimersState, "TS=./timers.state",
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
	}
	if v.Cgroup != "" {
		pairs = append(pairs, "/proc/self/cgroup", v.Cgroup)
	}
	r := strings.NewReplacer(pairs...)
	// what a quoted heredoc holds is written as it is (the restore script, whose hash the block checks, and the files of the sources): only the lines outside are pointed at the box
	var out strings.Builder
	inside := false
	for _, line := range strings.SplitAfter(code, "\n") {
		switch {
		case inside:
			out.WriteString(line)
			inside = strings.TrimRight(line, "\n") != "CASAOS_EOF"
		default:
			out.WriteString(r.Replace(line))
			inside = strings.Contains(line, "<<'CASAOS_EOF'")
		}
	}
	return out.String()
}

// DriverVars say where the driver's own words point in the box.
type DriverVars struct {
	// Root is the folder whose bin comes first in the PATH of the driver and whose bootid is the boot id that it reads, and where the state of the timers is (timers.state).
	Root string
	// BKCase and RefreshCase are the case patterns that stand where the driver has /var/backups/casaos-os-upgrade-* and /var/backups/casaos-os-upgrade-refresh-*.
	BKCase, RefreshCase string
}

// replaceOnce is the change of the driver's text that must apply exactly once: a driver that was edited has to be looked at, not run with a change that did nothing.
func replaceOnce(t testing.TB, text, from, to string) string {
	t.Helper()
	if n := strings.Count(text, from); n != 1 {
		t.Fatalf("the driver has %d places that read %q, and a change of it for the box needs exactly one", n, from)
	}
	return strings.Replace(text, from, to, 1)
}

// Driver is the text of the driver with the paths of the machine that it names pointed at the box.
func Driver(t testing.TB, driver string, v DriverVars) string {
	t.Helper()
	root := filepath.ToSlash(v.Root)
	driver = replaceOnce(t, driver, "PATH=/usr/sbin:/usr/bin:/sbin:/bin; export PATH\n", "PATH=\"$(cd '"+root+"/bin' && pwd):/usr/sbin:/usr/bin:/sbin:/bin\"; export PATH\n")
	driver = replaceOnce(t, driver, "/proc/sys/kernel/random/boot_id", root+"/bootid")
	driver = replaceOnce(t, driver, "case \"$BK\" in /var/backups/casaos-os-upgrade-*) ;;", "case \"$BK\" in "+v.BKCase+") ;;")
	driver = replaceOnce(t, driver, "case \"$BK\" in /var/backups/casaos-os-upgrade-refresh-*) return 0 ;;", "case \"$BK\" in "+v.RefreshCase+") return 0 ;;")
	driver = replaceOnce(t, driver, "/var/backups/casaos-os-upgrade-timers.state", root+"/timers.state")
	return driver
}

// Flow makes the machine that the nine blocks of a hop go through one after the other, on top of Install: two of the three timers running, the files that blocks 2 to 8 read, a
// kernel to start with, room, and a boot (bootID). The sources of apt and the simulations of the full upgrade are the plan's, and the test gives them.
func Flow(t testing.TB, dir, bootID string) {
	t.Helper()
	Write(t, dir, "bootid", bootID+"\n")
	// block 1: two of the timers are running, and the stub of systemctl says so
	Write(t, dir, "fake/active-apt-daily.timer", "")
	Write(t, dir, "fake/active-unattended-upgrades.service", "")
	// block 2
	Write(t, dir, "root/etc/hosts", "127.0.0.1 localhost\n")
	Write(t, dir, "fake/packages.tsv", "ii \tbash\t5.1-2\nii \tlinux-image-5.10.0-46-cloud-amd64\t5.10.209-2\n")
	Write(t, dir, "fake/ss.txt", "LISTEN 0 128 0.0.0.0:22 0.0.0.0:*\n")
	Write(t, dir, "fake/sshd-T.txt", "port 22\nlistenaddress 0.0.0.0:22\npermitrootlogin without-password\npasswordauthentication no\n")
	Write(t, dir, "fake/sim.txt", Converged)
	// block 4
	Write(t, dir, "archives/a.deb", "x")
	// block 7: the new kernel is there, with its initrd and a boot loader that names it, and the old one is kept
	Write(t, dir, "fake/kernels.txt", "ii  linux-image-5.10.0-46-cloud-amd64\nii  linux-image-6.1.0-53-cloud-amd64\n")
	Write(t, dir, "fake/pkg-systemd-timesyncd", "ii ")
	Write(t, dir, "boot/vmlinuz-6.1.0-53-cloud-amd64", "k")
	sized(t, dir, "boot/initrd.img-6.1.0-53-cloud-amd64", 5<<20+1)
	Write(t, dir, "boot/vmlinuz-5.10.0-46-cloud-amd64", "k")
	Write(t, dir, "boot/grub/grub.cfg", "menuentry { linux /boot/vmlinuz-6.1.0-53-cloud-amd64 }\n")
}

// Restarted makes the machine the one after its restart: another boot, Debian 12, the kernel that block 7 looked at.
func Restarted(t testing.TB, dir, bootID string) {
	t.Helper()
	Write(t, dir, "bootid", bootID+"\n")
	Write(t, dir, "os-release", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_CODENAME=bookworm\n")
	Write(t, dir, "fake/uname-r", "6.1.0-53-cloud-amd64\n")
}

// sized makes a file of n bytes (zeros, and no time spent on them).
func sized(t testing.TB, dir, rel string, n int64) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := f.Truncate(n); err != nil {
		t.Fatal(err)
	}
}
