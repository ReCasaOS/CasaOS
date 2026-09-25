package alerts

import (
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/version"
)

// AutoUpdate is pkg/autoupdate's hook: an automatic update of release that
// succeeded, failed, or failed a second time and paused.
func (h *Hub) AutoUpdate(result, release string) {
	sentences := map[string]string{
		"succeeded": "ReCasaOS updated itself to " + release + ".",
		"failed":    "The automatic update to " + release + " failed: it is tried again next night.",
		"paused":    "The automatic update to " + release + " failed twice: it is paused until you resume it in the settings, or a newer release comes out.",
	}
	if sentence, known := sentences[result]; known {
		h.raise(alert{key: "update:" + release, category: Updates, sentence: sentence})
	}
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
