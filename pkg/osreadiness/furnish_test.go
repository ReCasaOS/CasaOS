package osreadiness

import (
	"fmt"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

const dockerVersion = "5:28.0.4-1~debian.11~bullseye"

var (
	argvCgroup     = []string{"stat", "-fc", "%T", "/sys/fs/cgroup"}
	argvDockerInfo = []string{"docker", "info", "--format", dockerpkg.DaemonInfoFormat}
	argvDockerPs   = []string{"docker", "ps", "-q"}
	argvVerify     = []string{"findmnt", "--verify"}
)

func argvLsblk(device string) []string {
	return []string{"lsblk", "-s", "-n", "-P", "-p", "-o", "NAME,TYPE", device}
}

func argvSize(path string) []string {
	return []string{"findmnt", "-b", "-n", "-o", "SIZE", "-T", path}
}

func argvSmart(disk string) []string { return []string{"smartctl", "-H", disk} }

// lsblkOf is what "lsblk -P" prints for a stack of devices, each given as "name:type".
func lsblkOf(devices ...string) string {
	var out strings.Builder
	for _, d := range devices {
		name, kind, _ := strings.Cut(d, ":")
		fmt.Fprintf(&out, "NAME=%q TYPE=%q\n", name, kind)
	}
	return out.String()
}

// daemonInfo is what "docker info --format" prints of a daemon whose swarm is in a state.
func daemonInfo(swarm string) string {
	return `{"ServerVersion":"28.0.4","SwarmState":"` + swarm + `"}` + "\n"
}

// dockerPolicy is what "apt-cache policy docker-ce" says: the version installed, the one offered,
// and Docker's repository as where they are from.
func dockerPolicy(installed, candidate string) string {
	return "docker-ce:\n  Installed: " + installed + "\n  Candidate: " + candidate + "\n  Version table:\n *** " + installed + " 500\n        500 https://download.docker.com/linux/debian bullseye/stable amd64 Packages\n        100 /var/lib/dpkg/status\n"
}

func smartOf(result string) string {
	return "smartctl 7.2 2020-12-30 r5155\n\n=== START OF READ SMART DATA SECTION ===\nSMART overall-health self-assessment test result: " + result + "\n"
}

// inspected is a line of "docker inspect" for a container: its name, its restart policy, whether it
// shares the machine's network and the ports it publishes ("80/tcp" on host port 8080 is "80/tcp:8080").
func inspected(name, restart string, hostNetwork bool, ports ...string) string {
	network := "bridge"
	if hostNetwork {
		network = "host"
	}
	bindings := []string{}
	for _, port := range ports {
		container, host, _ := strings.Cut(port, ":")
		bindings = append(bindings, fmt.Sprintf(`%q:[{"HostIp":"0.0.0.0","HostPort":%q}]`, container, host))
	}
	return fmt.Sprintf(`{"name":"/%s","image":"img","restart":%q,"network":%q,"mounts":[],"ports":{%s}}`+"\n", name, restart, network, strings.Join(bindings, ","))
}

const containerID = "0123456789ab"

// furnish gives a healthy box what the checks of the disks, the network and the apps ask of it: a
// Debian cloud image's boot (GRUB through grub-cloud-amd64, a BIOS start), one disk that has a root
// partition and answers SMART, enough room and memory, a cable, Docker from Docker's repository
// that runs with no container, and a backup of yesterday.
func furnish(f *fakeProbe) {
	for path, content := range map[string]string{
		"/proc/meminfo":                      "MemTotal:        4044764 kB\nMemFree:         2101232 kB\nSwapTotal:             0 kB\nSwapFree:              0 kB\n",
		"/proc/mdstat":                       "Personalities : \nunused devices: <none>\n",
		"/etc/fstab":                         "# /etc/fstab\nLABEL=cloudimg-rootfs\t/\text4\tdiscard,errors=remount-ro\t0 1\n",
		"/boot/grub/grub.cfg":                "# grub",
		"/dev/disk/by-label/cloudimg-rootfs": "",
		"/var/lib/casaos/backups/runs.json":  `[{"app":"jellyfin","destination":"/DATA","stamp":"2026-10-10T01-00-00","started_at":"2026-10-10T01:00:00Z","finished_at":"2026-10-10T01:05:00Z","scheduled":true,"containers_stopped":true,"copied":3,"skipped_count":0}]`,
	} {
		f.files[path] = content
	}
	f.links["/bin"], f.links["/sbin"], f.links["/lib"] = "usr/bin", "usr/sbin", "usr/lib"
	for path, free := range map[string]uint64{
		"/": 20 << 30, "/usr": 20 << 30, "/boot": 5 << 30, "/boot/efi": 100 << 20, "/var/cache/apt/archives": 20 << 30,
	} {
		f.free[path] = free
	}
	f.installed(pkg("grub-cloud-amd64", "0.0.5")+pkg("grub-efi-amd64-signed", "1+2.06+3~deb11u7"), grubPackages...)
	f.on("cgroup2fs\n", 0, argvCgroup...)
	f.on(lsblkOf("/dev/sda1:part", "/dev/sda:disk"), 0, argvLsblk("/dev/sda1")...)
	f.on(smartOf("PASSED"), 0, argvSmart("/dev/sda")...)
	f.on("Success, no errors or warnings detected\n", 0, argvVerify...)
	f.on("32212254720\n", 0, argvSize("/boot")...)
	f.installed(pkg("docker-ce", dockerVersion), "docker-ce", "docker.io")
	f.on(dockerPolicy(dockerVersion, dockerVersion), 0, aptPolicyArgs("docker-ce")...)
	f.on(daemonInfo("inactive"), 0, argvDockerInfo...)
	f.on("", 0, argvDockerPs...)
}
