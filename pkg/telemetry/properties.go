package telemetry

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
)

const unknown = "unknown"

// Properties is what every event carries. The sender and the preview the
// dashboard shows both build it here: what the owner sees is what is sent.
func (t *Telemetry) Properties() map[string]any {
	disks, size := t.disks()
	return map[string]any{
		"distribution":            t.fileValue(common.FORK_RELEASE_FILE),
		"core":                    "v" + common.VERSION,
		"arch":                    arch(runtime.GOARCH, buildSettings()),
		"os":                      t.osRelease(),
		"kernel":                  t.kernel(),
		"virtualization":          t.virtualization(),
		"model":                   t.model(),
		"docker":                  t.docker(),
		"cpu_cores":               runtime.NumCPU(),
		"ram_gb":                  t.ramGB(),
		"disks":                   disks,
		"storage_tb":              storageTB(size),
		"raid":                    t.raid(),
		"$process_person_profile": false,
		"$lib":                    "recasaos-core",
	}
}

// fileValue is a file's trimmed content, or "unknown" when it is absent or empty.
func (t *Telemetry) fileValue(name string) string {
	data, err := os.ReadFile(t.path(name))
	if value := strings.TrimSpace(string(data)); err == nil && value != "" {
		return value
	}
	return unknown
}

func buildSettings() []debug.BuildSetting {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Settings
	}
	return nil
}

// arch is GOARCH, with GOARM on arm: "arm-7", as the release names its armv7 build.
func arch(goarch string, settings []debug.BuildSetting) string {
	if goarch != "arm" {
		return goarch
	}
	for _, setting := range settings {
		if setting.Key == "GOARM" && setting.Value != "" {
			return "arm-" + setting.Value
		}
	}
	return goarch
}

// osRelease is ID and VERSION_ID from /etc/os-release, ID alone when there is
// no VERSION_ID.
func (t *Telemetry) osRelease() string {
	data, err := os.ReadFile(t.path("/etc/os-release"))
	if err != nil {
		return unknown
	}
	values := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			values[key] = strings.Trim(value, `"'`)
		}
	}
	switch {
	case values["ID"] == "":
		return unknown
	case values["VERSION_ID"] == "":
		return values["ID"]
	default:
		return values["ID"] + " " + values["VERSION_ID"]
	}
}

var majorMinor = regexp.MustCompile(`^\d+\.\d+`)

// kernel is the running kernel's release, major.minor: what uname -r answers,
// read where the kernel keeps it.
func (t *Telemetry) kernel() string {
	data, _ := os.ReadFile(t.path("/proc/sys/kernel/osrelease"))
	if version := majorMinor.FindString(strings.TrimSpace(string(data))); version != "" {
		return version
	}
	return unknown
}

// virtualization is what systemd-detect-virt prints. It exits 1 when it prints
// "none", so its output counts, not its exit status.
func (t *Telemetry) virtualization() string {
	out, _ := t.Command("systemd-detect-virt")
	if value := strings.TrimSpace(string(out)); value != "" {
		return value
	}
	return unknown
}

// modelPlaceholders are what firmware answers when nobody named the machine.
var modelPlaceholders = []string{"To Be Filled By O.E.M.", "System Product Name", "Default string", "Not Specified"}

// model is the board's device-tree model, else the DMI product name.
func (t *Telemetry) model() string {
	for _, name := range []string{"/proc/device-tree/model", "/sys/class/dmi/id/product_name"} {
		data, _ := os.ReadFile(t.path(name))
		value := strings.Trim(string(data), "\x00 \t\r\n")
		if value == "" {
			continue
		}
		if runes := []rune(value); len(runes) > 64 {
			value = strings.TrimSpace(string(runes[:64]))
		}
		for _, placeholder := range modelPlaceholders {
			if strings.EqualFold(value, placeholder) {
				return unknown
			}
		}
		return value
	}
	return unknown
}

// docker is the engine's version, from GET /version on its socket.
func (t *Telemetry) docker() string {
	socket := t.path("/var/run/docker.sock")
	client := &http.Client{
		Timeout: 5 * time.Second,
		// Its own transport, and so no proxy: this request never leaves the box.
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var dialer net.Dialer
			return dialer.DialContext(ctx, "unix", socket)
		}},
	}
	defer client.CloseIdleConnections()
	resp, err := client.Get("http://docker/version")
	if err != nil {
		return unknown
	}
	defer resp.Body.Close()
	var version struct{ Version string }
	if resp.StatusCode != http.StatusOK || json.NewDecoder(resp.Body).Decode(&version) != nil || version.Version == "" {
		return unknown
	}
	return version.Version
}

// ramGB is MemTotal rounded to the nearest of 1, 2, 4 ... 64 GiB, "128+" past
// that. A string, because of "128+".
func (t *Telemetry) ramGB() string {
	data, err := os.ReadFile(t.path("/proc/meminfo"))
	if err != nil {
		return unknown
	}
	for _, line := range strings.Split(string(data), "\n") {
		if fields := strings.Fields(line); len(fields) >= 2 && fields[0] == "MemTotal:" {
			if kib, err := strconv.ParseUint(fields[1], 10, 64); err == nil {
				return ramBucket(kib)
			}
		}
	}
	return unknown
}

// ramBucket takes kB as /proc/meminfo counts them (KiB). The boundary between
// two sizes is their midpoint, 1.5 times the smaller; a midpoint goes up.
func ramBucket(kib uint64) string {
	gib := float64(kib) / (1 << 20)
	for _, size := range []float64{1, 2, 4, 8, 16, 32, 64} {
		if gib < size*1.5 {
			return strconv.Itoa(int(size))
		}
	}
	return "128+"
}

// disks counts the physical block devices and sums their sizes: the entries of
// /sys/block that do not resolve under /sys/devices/virtual/ (loop, zram, dm,
// md), less optical drives (sr*) and eMMC boot partitions (mmcblk*boot*).
func (t *Telemetry) disks() (count int, size uint64) {
	dir := t.path("/sys/block")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0
	}
	for _, entry := range entries {
		name := entry.Name()
		if skip, _ := filepath.Match("sr*", name); skip {
			continue
		}
		if skip, _ := filepath.Match("mmcblk*boot*", name); skip {
			continue
		}
		resolved, err := filepath.EvalSymlinks(filepath.Join(dir, name))
		if err != nil || strings.Contains(resolved, "/sys/devices/virtual/") {
			continue
		}
		count++
		data, _ := os.ReadFile(filepath.Join(dir, name, "size"))
		sectors, _ := strconv.ParseUint(strings.TrimSpace(string(data)), 10, 64)
		size += sectors * 512
	}
	return count, size
}

// storageTB buckets a size in TB of 10^12 bytes.
func storageTB(size uint64) string {
	const tb = 1_000_000_000_000
	switch {
	case size < tb/2:
		return "<0.5"
	case size < tb:
		return "0.5-1"
	case size < 2*tb:
		return "1-2"
	case size < 4*tb:
		return "2-4"
	case size < 8*tb:
		return "4-8"
	case size < 16*tb:
		return "8-16"
	case size < 32*tb:
		return "16-32"
	default:
		return "32+"
	}
}

// raid reports whether /proc/mdstat lists an active md array: "md0 : active raid1 ...".
func (t *Telemetry) raid() bool {
	data, _ := os.ReadFile(t.path("/proc/mdstat"))
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && strings.HasPrefix(fields[0], "md") && fields[1] == ":" && fields[2] == "active" {
			return true
		}
	}
	return false
}
