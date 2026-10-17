package alerts

import (
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// osUpgradeRetries are the waits before a notice that no channel took is sent again: the news of an upgrade is told once by the core, which keeps its own record of that, so a
// network that is down when a run stops, or when the machine comes back from its restart before its DNS does, must not make the news lost. The last wait is the last try.
var osUpgradeRetries = []time.Duration{time.Minute, 5 * time.Minute, 15 * time.Minute, time.Hour, 4 * time.Hour}

// OSUpgradeNotice tells the owner of what became of an upgrade of the operating system, or that it waits for them: the core says each thing once (it keeps its own record
// of what it told, which survives a restart of the core), and this is only the way out to the channels. The key is the core's own, one for each thing there is to say, so that a
// run that stopped, was resumed and stopped again is told twice, and the six hours that an alert stays quiet are for the repeats of one thing and not for the next. It returns at
// once, and says whether a channel was there to take the message (the category of updates is on and there is one): the sending is made on its own, and when no channel takes the
// message it is sent again after the waits of osUpgradeRetries, for as long as the core lives and a channel is there.
func (h *Hub) OSUpgradeNotice(key, sentence string) bool {
	if !h.updatesHaveAChannel() {
		return false
	}
	a := alert{key: "os-upgrade:" + key, category: Updates, sentence: sentence}
	retries := h.OSUpgradeRetries
	h.sending.Add(1)
	go func() {
		defer h.sending.Done()
		for i := 0; ; i++ {
			if h.raiseAs(a, true) {
				return
			}
			if i >= len(retries) {
				logger.Info("alerts: an upgrade notice reached no channel and is not sent again", zap.String("key", key))
				return
			}
			time.Sleep(retries[i])
			if !h.updatesHaveAChannel() {
				return
			}
		}
	}()
	return true
}

// updatesHaveAChannel says whether an alert of the category of updates has anywhere to go.
func (h *Hub) updatesHaveAChannel() bool {
	c := h.load()
	return len(c.Channels) > 0 && c.Categories[Updates]
}
