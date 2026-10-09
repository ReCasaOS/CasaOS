package alerts

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/dockerpkg"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"go.uber.org/zap"
)

// DockerNotifiedFile holds the nonce of the last Docker update that was told to the owner. It is
// the core's only memory of it: a restart of the core, at any moment of a run, changes nothing.
const DockerNotifiedFile = "/var/lib/casaos/docker-update.notified"

var (
	// dockerPoll is how often the watcher looks for the end of a run, and dockerPolls how many
	// times it looks: 5 seconds for 2 hours, more than any update takes. A run that outlasts it
	// is told by the next start of the core. Vars, so tests can shorten them.
	dockerPoll  = 5 * time.Second
	dockerPolls = 1440
)

// maxNotReturned is how many container names a message lists before it says "and N more".
const maxNotReturned = 10

// DockerRun is a run of the Docker update as far as the owner's phone is concerned. The service
// reads it from the run's log and from systemd; nothing here talks to Docker.
type DockerRun struct {
	// Nonce names the run, and is "" when there is no run to tell of.
	Nonce string
	// Outcome is "success", "restart_pending" or "failed" once the run has ended, and "" while it
	// has not.
	Outcome string
	// ErrorCode is why a run failed: guard, download, install, daemon, start or no_result.
	ErrorCode string
	// To is the version of the engine that runs after a run that succeeded.
	To string
	// NotReturned are the containers that were running before the update and were not after,
	// already validated by whoever read the log.
	NotReturned []DockerNotReturned
}

// DockerNotReturned is a container that was running before the update and was not running when the
// unit stopped waiting for it.
type DockerNotReturned struct {
	Name string
	// RestartPolicy is Docker's. A container whose policy is "" or "no" does not start again by
	// itself; one that has another was still starting when the unit stopped waiting.
	RestartPolicy string
}

// WatchDockerUpdate is the update button's hook: a run was just started, tell the owner when it
// ends. It returns at once.
func (h *Hub) WatchDockerUpdate() {
	go h.watchDocker(context.Background())
}

// watchDocker waits for the last run of the Docker update to end and tells it, looking every few
// seconds for about two hours. It is started with the core, for a run that ended or is going on
// while the core was down or restarting, and by the button, for the one it starts: with no run
// to wait for it returns at once. Any number of them may run: dockerEnded tells a run once.
func (h *Hub) watchDocker(ctx context.Context) {
	for i := 0; i < dockerPolls; i++ {
		run := h.LastDockerRun()
		switch {
		case run.Nonce == "":
			return
		case run.Outcome != "":
			h.dockerEnded(run)
			return
		}
		select {
		case <-time.After(dockerPoll):
		case <-ctx.Done():
			return
		}
	}
}

// dockerEnded sends the one message of a run that ended, and does nothing for a run it already
// told: the nonce of the last one is in memory and in the marker file, and the file is written
// before the message is sent, so that no restart, however badly timed, tells a run twice. It is
// written whether or not there is a channel to tell: the owner who adds one next week is not sent
// the news of last week's run.
func (h *Hub) dockerEnded(run DockerRun) {
	h.dockerMu.Lock()
	defer h.dockerMu.Unlock()
	if run.Nonce == "" || run.Nonce == h.dockerNotified {
		return
	}
	path := filepath.Join(h.Root, DockerNotifiedFile)
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) == run.Nonce {
		h.dockerNotified = run.Nonce
		return
	}
	// ponytail: without the file (an unwritable state directory) a restart tells the run again.
	h.dockerNotified = run.Nonce
	if err := file.WriteFileAtomic(path, []byte(run.Nonce+"\n")); err != nil {
		logger.Error("alerts: cannot remember that a Docker update was told", zap.Error(err))
	}
	// one key per run: the six hours an alert stays quiet are for repeats, not for the next run
	h.raise(alert{key: "docker-update:" + run.Nonce, category: Updates, sentence: dockerSentence(run)})
}

// dockerSentence is what the owner reads of a run that ended: what became of Docker, then the
// containers that are not running again.
func dockerSentence(run DockerRun) string {
	var sentence string
	switch run.Outcome {
	case "success":
		sentence = "Docker was updated"
		if run.To != "" {
			sentence += " to " + run.To
		}
		sentence += "."
	case "restart_pending":
		sentence = "Docker's new version is installed, but the old one still runs: restart Docker to switch (sudo systemctl restart docker, which stops every container)."
	default:
		sentence = "The Docker update failed: " + dockerFailure(run.ErrorCode) + "."
	}
	// Only a container with no restart policy is left to the owner: the others were still starting
	// when the unit stopped waiting, and Docker is starting them.
	var stay, late []string
	for _, container := range run.NotReturned {
		if container.RestartPolicy == "" || container.RestartPolicy == "no" {
			stay = append(stay, container.Name)
		} else {
			late = append(late, container.Name)
		}
	}
	if len(stay) > 0 {
		sentence += " Not running again: " + nameList(stay) + " (they do not start by themselves)."
	}
	if len(late) > 0 {
		sentence += " Still starting after " + strconv.Itoa(dockerpkg.ReturnWaitSeconds) + " seconds: " + nameList(late) + " (they have a restart policy and should start by themselves)."
	}
	if run.Outcome == "failed" {
		sentence += " Details are in the dashboard."
	}
	return sentence
}

// nameList is the names, the first maxNotReturned of them, and how many more there are.
func nameList(names []string) string {
	list := strings.Join(names[:min(len(names), maxNotReturned)], ", ")
	if len(names) > maxNotReturned {
		list += " and " + strconv.Itoa(len(names)-maxNotReturned) + " more"
	}
	return list
}

// dockerFailure is why a run failed, in words: the code is the unit's and nothing else is said.
func dockerFailure(code string) string {
	switch code {
	case "guard":
		return "it was no longer what you confirmed, so nothing was installed"
	case "download":
		return "the packages could not be downloaded, nothing was changed"
	case "install":
		return "the packages could not be installed"
	case "daemon":
		return "Docker did not come back after the update"
	case "start":
		return "the update could not be started, nothing was changed"
	}
	return "it stopped before it reported a result"
}
