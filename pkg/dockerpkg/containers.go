package dockerpkg

import (
	"encoding/json"
	"errors"
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Port is a port a container publishes on the box: container port 80 reached on host port 8080.
type Port struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
	HostPort int    `json:"host_port"`
}

// Container is a running container, as the confirmation of an update lists it.
type Container struct {
	Name  string `json:"name"`
	Image string `json:"image"`
	// RestartPolicy is Docker's: "no" or "" (it will not come back by itself), "always",
	// "unless-stopped" or "on-failure".
	RestartPolicy string `json:"restart_policy"`
	// HostNetwork is a container that shares the box's network: its ports are the box's ports.
	HostNetwork bool `json:"host_network"`
	// Ports are the ports it publishes, never nil.
	Ports []Port `json:"ports"`
}

// ContainerInspectFormat is the `docker inspect --format` template that prints one JSON
// object per container, on one line: `docker inspect --format <this> $(docker ps -q)`.
// Every free-text field goes through {{json}}, so that no name or image, whatever it holds,
// can break the line or the object. ParseContainers reads it.
const ContainerInspectFormat = `{"name":{{json .Name}},"image":{{json .Config.Image}},"restart":{{json .HostConfig.RestartPolicy.Name}},"network":{{json .HostConfig.NetworkMode}},"ports":{{json .NetworkSettings.Ports}}}`

var (
	containerNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	restartPolicyPattern = regexp.MustCompile(`^[a-z-]*$`)
	daemonVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+~_-]*$`)
)

const (
	maxContainerNameLen = 128
	maxRestartPolicyLen = 32
	maxImageLen         = 256
	maxDaemonVersionLen = 100
)

// validContainerName is Docker's own rule for the name of a container.
func validContainerName(name string) bool {
	return len(name) <= maxContainerNameLen && containerNamePattern.MatchString(name)
}

// validRestartPolicy is the name of a restart policy: "", "no", "always", "unless-stopped",
// "on-failure".
func validRestartPolicy(policy string) bool {
	return len(policy) <= maxRestartPolicyLen && restartPolicyPattern.MatchString(policy)
}

type inspectedContainer struct {
	Name    string `json:"name"`
	Image   string `json:"image"`
	Restart string `json:"restart"`
	Network string `json:"network"`
	Ports   map[string][]struct {
		HostPort string `json:"HostPort"`
	} `json:"ports"`
}

// ParseContainers reads the output of `docker inspect --format ContainerInspectFormat`. It
// is tolerant and strict at once: a line that is not a JSON object of that shape is skipped
// (so is a container whose name does not follow Docker's rule or whose restart policy is not
// a policy's name), and what is kept has been through validation: the name without its
// leading "/", the image cleaned of control characters and cut to a bound, the published
// ports only. The result is sorted by name and never nil.
func ParseContainers(output string) []Container {
	containers := []Container{}
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var raw inspectedContainer
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		name := strings.TrimPrefix(raw.Name, "/")
		if !validContainerName(name) || !validRestartPolicy(raw.Restart) {
			continue
		}
		containers = append(containers, Container{
			Name:          name,
			Image:         boundedText(raw.Image, maxImageLen),
			RestartPolicy: raw.Restart,
			HostNetwork:   raw.Network == "host",
			Ports:         publishedPorts(raw.Ports),
		})
	}
	sort.SliceStable(containers, func(i, j int) bool { return containers[i].Name < containers[j].Name })
	return containers
}

// boundedText drops the control and line-separator characters of text and cuts it to at most
// max bytes, on a character boundary.
func boundedText(text string, max int) string {
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) || r == '\u2028' || r == '\u2029' {
			return -1
		}
		return r
	}, text)
	if len(text) > max {
		text = text[:max]
		for !utf8.ValidString(text) {
			text = text[:len(text)-1]
		}
	}
	return text
}

// portNumber is a TCP or UDP port in decimal, or 0 when s is not one.
func portNumber(s string) int {
	if s == "" || len(s) > 5 {
		return 0
	}
	n := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			return 0
		}
		n = n*10 + int(r-'0')
	}
	if n > 65535 {
		return 0
	}
	return n
}

// publishedPorts reads Docker's port map ("80/tcp": [{"HostPort": "8080"}]), keeping what is
// published on a host port, once each, in order.
func publishedPorts(bindings map[string][]struct {
	HostPort string `json:"HostPort"`
}) []Port {
	ports := []Port{}
	seen := map[Port]struct{}{}
	for key, list := range bindings {
		number, protocol, hasProtocol := strings.Cut(key, "/")
		if !hasProtocol {
			protocol = "tcp"
		}
		containerPort := portNumber(number)
		if containerPort == 0 || (protocol != "tcp" && protocol != "udp" && protocol != "sctp") {
			continue
		}
		for _, binding := range list {
			hostPort := portNumber(binding.HostPort)
			if hostPort == 0 {
				continue
			}
			port := Port{Port: containerPort, Protocol: protocol, HostPort: hostPort}
			if _, dup := seen[port]; !dup {
				seen[port] = struct{}{}
				ports = append(ports, port)
			}
		}
	}
	sort.Slice(ports, func(i, j int) bool {
		a, b := ports[i], ports[j]
		if a.Port != b.Port {
			return a.Port < b.Port
		}
		if a.Protocol != b.Protocol {
			return a.Protocol < b.Protocol
		}
		return a.HostPort < b.HostPort
	})
	return ports
}

// DaemonInfo is what the update needs to know of the Docker daemon.
type DaemonInfo struct {
	// ServerVersion is the version the running daemon reports.
	ServerVersion string
	// SwarmActive is a node that is part of a swarm.
	SwarmActive bool
}

// DaemonInfoFormat is the `docker info --format` template ParseDaemonInfo reads: one JSON
// object on one line.
const DaemonInfoFormat = `{"ServerVersion":{{json .ServerVersion}},"SwarmState":{{json .Swarm.LocalNodeState}}}`

// ParseDaemonInfo reads the output of `docker info --format DaemonInfoFormat`. It is an
// error when no daemon answered: the CLI then prints nothing, or what it knows of itself
// without a ServerVersion, or an error text. The first line that is a JSON object is the
// answer, so a warning before it does no harm.
func ParseDaemonInfo(output string) (DaemonInfo, error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "{") {
			continue
		}
		var raw struct {
			ServerVersion string `json:"ServerVersion"`
			SwarmState    string `json:"SwarmState"`
		}
		if err := json.Unmarshal([]byte(line), &raw); err != nil {
			continue
		}
		if !validDaemonVersion(raw.ServerVersion) {
			return DaemonInfo{}, errors.New("the Docker daemon did not report a usable version")
		}
		return DaemonInfo{ServerVersion: raw.ServerVersion, SwarmActive: raw.SwarmState == "active"}, nil
	}
	return DaemonInfo{}, errors.New("the Docker daemon did not answer")
}

// validDaemonVersion is the version a daemon reports: "29.8.0", "25.0.0-rc.1", "20.10.24+dfsg1".
func validDaemonVersion(v string) bool {
	return len(v) <= maxDaemonVersionLen && daemonVersionPattern.MatchString(v)
}
