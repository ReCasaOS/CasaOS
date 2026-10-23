package modulecmd

import (
	"os"
	"path/filepath"
	"testing"
)

// The stand-ins for systemctl and apt-get of the tests that run for real: scripts of POSIX sh (they run under busybox and under dash) that keep what they are told in <root>/stub, the
// way the real ones would keep it in systemd and dpkg. They find their directory from the path they are run by.
//
//	enabled-<unit>        the unit is enabled and active (systemctl enable --now makes it, disable --now removes it)
//	stopped-<unit>        the unit is enabled and not running (systemctl stop makes it, start removes it)
//	fail-<verb>           the verb ends with status 1
//	block-<verb>          the next such verb removes it, makes reached-<verb> and waits for half a minute (or for the signal that ends it)
//	systemctl.log, apt-get.log   the commands, one to a line
//	apt-installed         the arguments of the installs (a download, --download-only, ends before fail-apt and block-apt: they are about dpkg)
//	sim.txt, est.txt      what apt-get says of the simulation and of the estimate
const systemctlStub = `#!/bin/sh
root="${0%/usr/bin/systemctl}"
state="$root/stub"
mkdir -p "$state"
printf '%s\n' "$*" >> "$state/systemctl.log"
verb="$1"
if [ -e "$state/fail-$verb" ]; then exit 1; fi
if [ -e "$state/block-$verb" ]; then rm -f "$state/block-$verb"; : > "$state/reached-$verb"; sleep 30 & echo $! >> "$state/sleepers"; wait; fi
case "$verb" in
show) printf '%s\n' inactive ;;
enable) : > "$state/enabled-$3"; rm -f "$state/stopped-$3" ;;
disable) rm -f "$state/enabled-$3" "$state/stopped-$3" ;;
stop) : > "$state/stopped-$2" ;;
start) rm -f "$state/stopped-$2" ;;
is-active) if [ -e "$state/enabled-$2" ] && [ ! -e "$state/stopped-$2" ]; then printf '%s\n' active; exit 0; fi; printf '%s\n' inactive; exit 3 ;;
is-enabled) if [ -e "$state/enabled-$2" ]; then printf '%s\n' enabled; exit 0; fi; printf '%s\n' disabled; exit 1 ;;
esac
exit 0
`

const aptStub = `#!/bin/sh
root="${0%/usr/bin/apt-get}"
state="$root/stub"
mkdir -p "$state"
printf '%s\n' "$*" >> "$state/apt-get.log"
printf '%s\n' "pwd=$(pwd) LC_ALL=$LC_ALL LANG=$LANG FRONTEND=$DEBIAN_FRONTEND SECRET=$SECRET_OF_THE_TEST NONCE=$CASAOS_MODULE_NONCE APT_CONFIG=$APT_CONFIG TMPDIR=$TMPDIR PATH=$PATH" >> "$state/apt-get.env"
if [ -n "$APT_CONFIG" ] && [ -r "$APT_CONFIG" ]; then cat "$APT_CONFIG" > "$state/apt-config.seen"; fi
grep '^SigIgn' /proc/$$/status >> "$state/apt-get.sigign"
for a in "$@"; do
  case "$a" in
  -s) if [ -e "$state/block-sim" ]; then : > "$state/reached-sim"; sleep 30; fi; cat "$state/sim.txt"; exit 0 ;;
  --print-uris) cat "$state/est.txt"; exit 0 ;;
  --download-only) exit 0 ;;
  esac
done
if [ -e "$state/fail-apt" ]; then printf '%s\n' "E: Sub-process /usr/bin/dpkg returned an error code (1)"; exit 100; fi
if [ -e "$state/block-apt" ]; then rm -f "$state/block-apt"; : > "$state/reached-apt"; sleep 30 & echo $! >> "$state/sleepers"; wait; fi
printf '%s\n' "$*" >> "$state/apt-installed"
printf '%s\n' "Setting up the packages ..." "CASAOS_MODULE_RESULT - virt ok done - - 2000-01-01T00:00:00Z" "CASAOS_MODULE_STEP - virt route done 2000-01-01T00:00:00Z"
exit 0
`

func writeStubs(t *testing.T, root string) {
	t.Helper()
	for name, text := range map[string]string{"systemctl": systemctlStub, "apt-get": aptStub} {
		p := filepath.Join(root, "usr/bin", name)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(text), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	state := filepath.Join(root, "stub")
	if err := os.MkdirAll(state, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, text := range map[string]string{"sim.txt": aptSim, "est.txt": aptEst} {
		if err := os.WriteFile(filepath.Join(state, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func stubFile(root, name string) string { return filepath.Join(root, "stub", name) }

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}
