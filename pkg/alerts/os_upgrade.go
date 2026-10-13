package alerts

// OSUpgradeNotice tells the owner of what became of an upgrade of the operating system, or that it waits for them: the core says each thing once (it keeps its own record
// of what it told, which survives a restart of the core), and this is only the way out to the channels. The key is the core's own, one for each thing there is to say, so that a
// run that stopped, was resumed and stopped again is told twice, and the six hours that an alert stays quiet are for the repeats of one thing and not for the next. It returns at
// once, and says whether a channel was there to take the message (the category of updates is on and there is one).
func (h *Hub) OSUpgradeNotice(key, sentence string) bool {
	return h.raise(alert{key: "os-upgrade:" + key, category: Updates, sentence: sentence})
}
