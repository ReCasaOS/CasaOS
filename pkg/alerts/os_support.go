package alerts

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/ReCasaOS/CasaOS/pkg/ossupport"
	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
	"go.uber.org/zap"
)

// OSSupportNotifiedFile holds the last state of the operating system's support that was told to the
// owner, "<id>-<version_id>-<state>". It is the core's only memory of it: a restart changes nothing.
const OSSupportNotifiedFile = "/var/lib/casaos/os-support.notified"

var (
	// osSupportFirst is how long after the start the first look comes: the same five minutes as the
	// disks', so that the network is up when a message that is told once is sent. osSupportEvery
	// is how often it looks again, so that a box that crosses its date is told. Vars, so tests can
	// shorten them.
	osSupportFirst = 5 * time.Minute
	osSupportEvery = 24 * time.Hour
)

// watchOSSupport tells the owner when the operating system's security support is ending or has
// ended, once for each state of each release: it looks five minutes after the start, then every day.
func (h *Hub) watchOSSupport(ctx context.Context) {
	timer := time.NewTimer(osSupportFirst)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
		h.checkOSSupport()
		timer.Reset(osSupportEvery)
	}
}

// checkOSSupport sends the one message of a state of support that is not told yet, and nothing for
// "supported" or "unknown". The state is told once: the last one told is in memory and in the marker
// file. Unlike a Docker run, which is over, a state of support lasts for months, and a warning that
// was lost is worse than one that comes twice: so the marker is written only after a message went
// out. With no channel, with the Updates category off, or when every channel failed, nothing is
// written and the next look (a day later) tries again. A crash between the send and the marker
// may tell the state once more. Any number of looks may run at once: one of them tells it.
func (h *Hub) checkOSSupport() {
	report := h.OSSupport()
	state := report.Support.State
	if state != ossupport.EndingSoon && state != ossupport.SecurityEnded {
		return
	}
	key := report.ID + "-" + report.VersionID + "-" + state

	h.osMu.Lock()
	defer h.osMu.Unlock()
	if key == h.osNotified {
		return
	}
	path := filepath.Join(h.Root, OSSupportNotifiedFile)
	if data, err := os.ReadFile(path); err == nil && strings.TrimSpace(string(data)) == key {
		h.osNotified = key
		return
	}
	// one key per state: the six hours an alert stays quiet are for repeats, not for the next state
	if !h.raiseAs(alert{key: "os-support:" + key, category: Updates, sentence: osSupportSentence(report)}, true) {
		return
	}
	// ponytail: without the file (an unwritable state directory) a restart tells the state again.
	h.osNotified = key
	if err := file.WriteFileAtomic(path, []byte(key+"\n")); err != nil {
		logger.Error("alerts: cannot remember that the support of the operating system was told", zap.Error(err))
	}
}

// osSupportSentence is what the owner reads: what ends or ended and when, then what the product
// does not do yet.
func osSupportSentence(report ossupport.Report) string {
	name, kind := "", ""
	switch report.ID {
	case "debian":
		name, kind = "Debian", "Debian"
	case "ubuntu":
		name, kind = "Ubuntu", "standard"
	}
	date := osSupportDate(report.Support)
	subject := name + " " + report.VersionID
	var sentence string
	switch {
	case report.Support.State == ossupport.SecurityEnded:
		sentence = subject + " no longer receives " + kind + " security updates since " + date + "."
	case report.Support.Precision == ossupport.PrecisionMonth:
		sentence = subject + " stops receiving " + kind + " security updates " + date + "."
	default:
		sentence = subject + " stops receiving " + kind + " security updates on " + date + "."
	}
	return sentence + " ReCasaOS cannot upgrade the operating system for you yet: see the dashboard."
}

// osSupportDate is the day in words, "31 August 2026", or the month with "around" for a date the
// sources do not agree on the day of: "around June 2027".
func osSupportDate(support ossupport.Support) string {
	end, err := time.Parse("2006-01-02", support.Ends)
	if err != nil {
		return support.Ends
	}
	if support.Precision == ossupport.PrecisionMonth {
		return "around " + end.Format("January 2006")
	}
	return end.Format("2 January 2006")
}
