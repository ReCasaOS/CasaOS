package alerts

import (
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/version"
)

// AutoUpdate is pkg/autoupdate's hook: an automatic update of release that
// succeeded, failed, failed because its installer was refused (not signed, or not that
// release), or failed a second time and paused.
func (h *Hub) AutoUpdate(result, release string) {
	sentences := map[string]string{
		"refused":   "The update to " + release + " was not installed: its installer is not signed by ReCasaOS.",
		"succeeded": "ReCasaOS updated itself to " + release + ".",
		"failed":    "The automatic update to " + release + " failed: it is tried again next night.",
		"paused":    "The automatic update to " + release + " failed twice: it is paused until you resume it in the settings, or a newer release comes out.",
	}
	if sentence, known := sentences[result]; known {
		// one key per result: a pause the next night is news, not a repeat of the failure
		h.raise(alert{key: "update:" + release + ":" + result, category: Updates, sentence: sentence})
	}
}

// ModuleUpdates tells what a night did with the modules: one sentence a round, which names its tags.
func (h *Hub) ModuleUpdates(sentence string) {
	h.raise(alert{key: "modules:" + sentence, category: Updates, sentence: sentence})
}

// checkRelease announces a release newer than the one installed, once per
// release, when automatic updates are off: they announce their own. It looks
// at most once a day, through the update button's own version check.
func (h *Hub) checkRelease() {
	now := h.Now()
	if !h.releaseChecked.IsZero() && now.Sub(h.releaseChecked) < 24*time.Hour {
		return
	}
	h.releaseChecked = now
	if h.AutoUpdates() {
		return
	}
	latest, current := h.Latest(), h.Current()
	if latest == "" || latest == h.announced || !version.IsVersionNewer(latest, current) {
		return
	}
	sentence := "ReCasaOS " + latest + " is out, v" + strings.TrimPrefix(current, "v") + " is installed: update from the dashboard's settings."
	if h.raise(alert{key: "update:" + latest, category: Updates, sentence: sentence}) {
		h.announced = latest
	}
}

// ModuleWithdrawn tells that an installed module is at a version its catalogue withdrew, with the catalogue's reason
// quoted as it is. It waits for the channels: true when one of them took the message, so that the caller can keep it
// told for good; false (no channel, the category off, every send failed) leaves nothing on record, to try again.
func (h *Hub) ModuleWithdrawn(name, tag, reason string) bool {
	// the reason is the catalogue's text: put in as it is, never read as a format
	sentence := name + " " + tag + ` was withdrawn from the catalogue: "` + reason + `"`
	return h.raiseAs(alert{key: "withdrawn:" + name + ":" + tag, category: Updates, sentence: sentence}, true, "")
}
