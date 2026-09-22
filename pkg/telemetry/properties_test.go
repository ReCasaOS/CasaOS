package telemetry

import (
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"runtime/debug"
	"strconv"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/common"
)

// virt stands for systemd-detect-virt.
func virt(out string, err error) func(string, ...string) ([]byte, error) {
	return func(name string, _ ...string) ([]byte, error) {
		if name != "systemd-detect-virt" {
			return nil, errors.New("unexpected command " + name)
		}
		return []byte(out), err
	}
}

// disk lays a block device out as sysfs does: the device under /sys/devices
// with its size in 512-byte sectors, and /sys/block/<name> a relative link to it.
func disk(t *testing.T, root, name, device string, sectors uint64) {
	t.Helper()
	write(t, root, filepath.Join("/sys", device, "size"), strconv.FormatUint(sectors, 10)+"\n")
	link := filepath.Join(root, "/sys/block", name)
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join("..", device), link); err != nil {
		t.Fatal(err)
	}
}

const (
	mdstatActive   = "Personalities : [raid1]\nmd0 : active raid1 sdb1[1] sda1[0]\n      976630464 blocks super 1.2 [2/2] [UU]\n\nunused devices: <none>\n"
	mdstatInactive = "Personalities : \nmd127 : inactive sdb[0](S)\n      976631512 blocks super 1.2\n\nunused devices: <none>\n"
	mdstatNone     = "Personalities : \nunused devices: <none>\n"
)

func TestPropertiesOfABox(t *testing.T) {
	root := t.TempDir()
	write(t, root, common.FORK_RELEASE_FILE, "v0.5.0\n")
	write(t, root, "/etc/os-release", "PRETTY_NAME=\"Ubuntu 24.04.1 LTS\"\nID=ubuntu\nVERSION_ID=\"24.04\"\n")
	write(t, root, "/proc/sys/kernel/osrelease", "6.8.0-45-generic\n")
	write(t, root, "/proc/device-tree/model", "Raspberry Pi 5 Model B Rev 1.0\x00")
	write(t, root, "/proc/meminfo", "MemTotal:        8010052 kB\nMemFree:         1234567 kB\n")
	write(t, root, "/proc/mdstat", mdstatActive)
	disk(t, root, "sda", "devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", 3907029168) // 2 TB
	tel := New(root)
	tel.Command = virt("none\n", errors.New("exit status 1"))

	want := map[string]any{
		"distribution":            "v0.5.0",
		"core":                    "v" + common.VERSION,
		"arch":                    arch(runtime.GOARCH, buildSettings()),
		"os":                      "ubuntu 24.04",
		"kernel":                  "6.8",
		"virtualization":          "none",
		"model":                   "Raspberry Pi 5 Model B Rev 1.0",
		"docker":                  "unknown",
		"cpu_cores":               runtime.NumCPU(),
		"ram_gb":                  "8",
		"disks":                   1,
		"storage_tb":              "2-4",
		"raid":                    true,
		"$process_person_profile": false,
		"$lib":                    "recasaos-core",
	}
	if got := tel.Properties(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Properties() =\n%v\nwant\n%v", got, want)
	}
}

func TestAnEmptyBoxIsUnknownEverywhere(t *testing.T) {
	tel := New(t.TempDir())
	tel.Command = virt("", errors.New(`exec: "systemd-detect-virt": executable file not found in $PATH`))
	got := tel.Properties()
	for _, key := range []string{"distribution", "os", "kernel", "virtualization", "model", "docker", "ram_gb"} {
		if got[key] != "unknown" {
			t.Errorf("%s = %v, want unknown", key, got[key])
		}
	}
	if got["disks"] != 0 || got["storage_tb"] != "<0.5" || got["raid"] != false {
		t.Errorf("disks, storage_tb, raid = %v, %v, %v; want 0, <0.5, false", got["disks"], got["storage_tb"], got["raid"])
	}
}

func TestArch(t *testing.T) {
	armv7 := []debug.BuildSetting{{Key: "GOARCH", Value: "arm"}, {Key: "GOARM", Value: "7"}}
	cases := []struct {
		goarch   string
		settings []debug.BuildSetting
		want     string
	}{
		{"amd64", []debug.BuildSetting{{Key: "GOARCH", Value: "amd64"}, {Key: "GOAMD64", Value: "v1"}}, "amd64"},
		{"arm64", nil, "arm64"},
		{"arm", armv7, "arm-7"},
		{"arm", nil, "arm"},
	}
	for _, tc := range cases {
		if got := arch(tc.goarch, tc.settings); got != tc.want {
			t.Errorf("arch(%q, %v) = %q, want %q", tc.goarch, tc.settings, got, tc.want)
		}
	}
}

func TestOSRelease(t *testing.T) {
	cases := []struct {
		name, content, want string
	}{
		{"ubuntu", "NAME=\"Ubuntu\"\nID=ubuntu\nID_LIKE=debian\nVERSION_ID=\"24.04\"\n", "ubuntu 24.04"},
		{"debian", "PRETTY_NAME=\"Debian GNU/Linux 12 (bookworm)\"\nVERSION_ID=\"12\"\nID=debian\n", "debian 12"},
		{"arch has no VERSION_ID", "NAME=\"Arch Linux\"\nID=arch\nBUILD_ID=rolling\n", "arch"},
		{"single quotes", "ID='fedora'\nVERSION_ID='40'\n", "fedora 40"},
		{"no ID", "NAME=\"Something\"\n", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "/etc/os-release", tc.content)
			if got := New(root).osRelease(); got != tc.want {
				t.Fatalf("osRelease() = %q, want %q", got, tc.want)
			}
		})
	}
	if got := New(t.TempDir()).osRelease(); got != "unknown" {
		t.Errorf("osRelease() with no /etc/os-release = %q, want unknown", got)
	}
}

func TestKernelIsMajorMinor(t *testing.T) {
	cases := []struct {
		release, want string
	}{
		{"6.8.0-45-generic\n", "6.8"},
		{"5.15.167.4-microsoft-standard-WSL2\n", "5.15"},
		{"6.12.20+rpt-rpi-2712\n", "6.12"},
		{"6.10\n", "6.10"},
		{"", "unknown"},
		{"not a version\n", "unknown"},
	}
	for _, tc := range cases {
		root := t.TempDir()
		write(t, root, "/proc/sys/kernel/osrelease", tc.release)
		if got := New(root).kernel(); got != tc.want {
			t.Errorf("kernel() of %q = %q, want %q", tc.release, got, tc.want)
		}
	}
	if got := New(t.TempDir()).kernel(); got != "unknown" {
		t.Errorf("kernel() with no osrelease = %q, want unknown", got)
	}
}

func TestVirtualization(t *testing.T) {
	cases := []struct {
		name, out string
		err       error
		want      string
	}{
		{"a virtual machine", "kvm\n", nil, "kvm"},
		{"a container", "lxc\n", nil, "lxc"},
		{"WSL", "wsl\n", nil, "wsl"},
		// It exits 1 when it prints none: the output counts, not the status.
		{"bare metal", "none\n", errors.New("exit status 1"), "none"},
		{"not installed", "", errors.New(`exec: "systemd-detect-virt": executable file not found in $PATH`), "unknown"},
	}
	for _, tc := range cases {
		tel := New(t.TempDir())
		tel.Command = virt(tc.out, tc.err)
		if got := tel.virtualization(); got != tc.want {
			t.Errorf("%s: virtualization() = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestModel(t *testing.T) {
	long := strings.Repeat("x", 80)
	cases := []struct {
		name, deviceTree, dmi, want string
	}{
		{"a device-tree board", "Raspberry Pi 5 Model B Rev 1.0\x00", "", "Raspberry Pi 5 Model B Rev 1.0"},
		{"a DMI machine", "", "ZimaBoard\n", "ZimaBoard"},
		{"the device tree first", "Raspberry Pi 4 Model B Rev 1.5\x00", "Other\n", "Raspberry Pi 4 Model B Rev 1.5"},
		{"an empty device tree falls back to DMI", "\x00", "ZimaBoard\n", "ZimaBoard"},
		{"placeholder OEM", "", "To Be Filled By O.E.M.\n", "unknown"},
		{"placeholder System Product Name", "", "System Product Name\n", "unknown"},
		{"placeholder Default string", "", "Default string\n", "unknown"},
		{"placeholder Not Specified", "", "Not Specified\n", "unknown"},
		{"a placeholder in another case", "", "not specified\n", "unknown"},
		{"64 characters at most", "", long + "\n", long[:64]},
		{"nothing", "", "", "unknown"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.deviceTree != "" {
				write(t, root, "/proc/device-tree/model", tc.deviceTree)
			}
			if tc.dmi != "" {
				write(t, root, "/sys/class/dmi/id/product_name", tc.dmi)
			}
			if got := New(root).model(); got != tc.want {
				t.Fatalf("model() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDocker(t *testing.T) {
	root := t.TempDir()
	socket := filepath.Join(root, "/var/run/docker.sock")
	if err := os.MkdirAll(filepath.Dir(socket), 0o755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { listener.Close() })
	go http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/version" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"Version":"28.3.1","ApiVersion":"1.51","Os":"linux"}`))
	}))

	if got := New(root).docker(); got != "28.3.1" {
		t.Fatalf("docker() = %q, want 28.3.1", got)
	}
	if got := New(t.TempDir()).docker(); got != "unknown" {
		t.Fatalf("docker() with no socket = %q, want unknown", got)
	}
}

func TestRAMIsRoundedToTheNearestSize(t *testing.T) {
	const gib = 1 << 20 // in kB, as /proc/meminfo counts
	cases := []struct {
		kib  uint64
		want string
	}{
		{gib / 2, "1"},
		{gib*3/2 - 1, "1"},
		{gib * 3 / 2, "2"},
		{3*gib - 1, "2"},
		{3 * gib, "4"},
		{6*gib - 1, "4"},
		{6 * gib, "8"},
		{8010052, "8"}, // an 8 GB box
		{12*gib - 1, "8"},
		{12 * gib, "16"},
		{24 * gib, "32"},
		{48 * gib, "64"},
		{96*gib - 1, "64"},
		{96 * gib, "128+"},
		{512 * gib, "128+"},
	}
	for _, tc := range cases {
		if got := ramBucket(tc.kib); got != tc.want {
			t.Errorf("ramBucket(%d kB) = %q, want %q", tc.kib, got, tc.want)
		}
	}
}

func TestRAMFromMeminfo(t *testing.T) {
	root := t.TempDir()
	write(t, root, "/proc/meminfo", "MemTotal:        3884096 kB\nMemFree:          123456 kB\n")
	if got := New(root).ramGB(); got != "4" {
		t.Fatalf("ramGB() = %q, want 4", got)
	}
	if got := New(t.TempDir()).ramGB(); got != "unknown" {
		t.Fatalf("ramGB() with no meminfo = %q, want unknown", got)
	}
}

func TestDisksAreThePhysicalBlockDevices(t *testing.T) {
	root := t.TempDir()
	disk(t, root, "sda", "devices/pci0000:00/0000:00:17.0/ata1/host0/target0:0:0/0:0:0:0/block/sda", 3907029168)       // 2 TB
	disk(t, root, "nvme0n1", "devices/pci0000:00/0000:00:1d.0/0000:3d:00.0/nvme/nvme0/nvme0n1", 1953525168)            // 1 TB
	disk(t, root, "mmcblk0", "devices/platform/emmc2bus/fe340000.mmc/mmc_host/mmc0/mmc0:0001/block/mmcblk0", 61071360) // 31 GB
	// Left out: an eMMC boot partition, an optical drive, and the virtual devices.
	disk(t, root, "mmcblk0boot0", "devices/platform/emmc2bus/fe340000.mmc/mmc_host/mmc0/mmc0:0001/block/mmcblk0/mmcblk0boot0", 8192)
	disk(t, root, "sr0", "devices/pci0000:00/0000:00:17.0/ata2/host1/target1:0:0/1:0:0:0/block/sr0", 2097151)
	disk(t, root, "loop0", "devices/virtual/block/loop0", 1000000)
	disk(t, root, "zram0", "devices/virtual/block/zram0", 1000000)
	disk(t, root, "md0", "devices/virtual/block/md0", 1000000)

	count, size := New(root).disks()
	if count != 3 {
		t.Fatalf("disks = %d, want 3", count)
	}
	if want := uint64(3907029168+1953525168+61071360) * 512; size != want {
		t.Fatalf("size = %d bytes, want %d", size, want)
	}
	if got := storageTB(size); got != "2-4" {
		t.Fatalf("storageTB(%d) = %q, want 2-4", size, got)
	}
	if count, size := New(t.TempDir()).disks(); count != 0 || size != 0 {
		t.Fatalf("disks() with no /sys/block = %d, %d; want 0, 0", count, size)
	}
}

func TestStorageBuckets(t *testing.T) {
	const tb = 1_000_000_000_000
	cases := []struct {
		size uint64
		want string
	}{
		{0, "<0.5"},
		{tb/2 - 1, "<0.5"},
		{tb / 2, "0.5-1"},
		{tb - 1, "0.5-1"},
		{tb, "1-2"},
		{2*tb - 1, "1-2"},
		{2 * tb, "2-4"},
		{4*tb - 1, "2-4"},
		{4 * tb, "4-8"},
		{8 * tb, "8-16"},
		{16 * tb, "16-32"},
		{32*tb - 1, "16-32"},
		{32 * tb, "32+"},
		{100 * tb, "32+"},
	}
	for _, tc := range cases {
		if got := storageTB(tc.size); got != tc.want {
			t.Errorf("storageTB(%d) = %q, want %q", tc.size, got, tc.want)
		}
	}
}

func TestRAID(t *testing.T) {
	cases := []struct {
		name, mdstat string
		want         bool
	}{
		{"an active array", mdstatActive, true},
		{"an inactive array", mdstatInactive, false},
		{"no array", mdstatNone, false},
	}
	for _, tc := range cases {
		root := t.TempDir()
		write(t, root, "/proc/mdstat", tc.mdstat)
		if got := New(root).raid(); got != tc.want {
			t.Errorf("%s: raid() = %v, want %v", tc.name, got, tc.want)
		}
	}
	if New(t.TempDir()).raid() {
		t.Error("raid() with no /proc/mdstat = true, want false")
	}
}
