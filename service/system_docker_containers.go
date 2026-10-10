package service

import (
	"context"
	"regexp"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
)

// SystemDockerContainers is the running containers, as the confirmation of an update of Docker
// lists them: the update stops every one of them, and only those with a restart policy start
// again by themselves.
type SystemDockerContainers struct {
	// Running is whether the Docker daemon answered. When it did not, there is no list.
	Running bool `json:"running"`
	// Containers are the containers that run now, never null. Names and images are text the
	// owner's apps chose, and are for display only.
	Containers []dockerpkg.Container `json:"containers"`
}

// containerIDPattern is the short or the whole id `docker ps -q` prints. Only what matches is
// handed back to docker inspect, so that nothing a command printed can pass for an option.
var containerIDPattern = regexp.MustCompile(`^[0-9a-f]{12,64}$`)

// GetSystemDockerContainers lists the running containers. It is quick, asks no one but the
// docker command and does not wait for a package operation: the page asks it when the owner
// opens the confirmation.
func (s *systemService) GetSystemDockerContainers() SystemDockerContainers {
	ctx, cancel := context.WithTimeout(context.Background(), systemDockerInfoTimeout)
	defer cancel()
	return s.systemPackageUpdater().dockerContainers(ctx)
}

func (u *systemPackageUpdater) dockerContainers(ctx context.Context) SystemDockerContainers {
	result := SystemDockerContainers{Containers: []dockerpkg.Container{}}
	dockerPath, _, err := u.dockerDaemon(ctx)
	if err != nil {
		return result
	}
	output, err := u.command(ctx, dockerPath, "ps", "-q")
	if err != nil {
		return result
	}
	var ids []string
	for _, line := range strings.Split(string(output), "\n") {
		if id := strings.TrimSpace(line); containerIDPattern.MatchString(id) {
			ids = append(ids, id)
		}
	}
	result.Running = true
	if len(ids) == 0 {
		return result
	}
	// A container that stops between the two commands makes inspect fail for it and print the
	// others; with nothing at all printed the daemon is not answering.
	output, err = u.command(ctx, dockerPath, append([]string{"inspect", "--format", dockerpkg.ContainerInspectFormat}, ids...)...)
	result.Containers = dockerpkg.ParseContainers(string(output))
	if err != nil && len(result.Containers) == 0 {
		result.Running = false
	}
	return result
}
