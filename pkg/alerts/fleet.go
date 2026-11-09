package alerts

// FleetOffline is pkg/fleet's hook: a box of the master's fleet has had no
// tunnel for ten minutes (offline), or has one again.
func (h *Hub) FleetOffline(boxID, name string, offline bool) {
	key := "fleet:" + boxID + ":offline"
	if offline {
		h.raise(alert{key: key, category: Fleet, sentence: "The box " + name + " has not reached the master for 10 minutes."})
		return
	}
	h.resolve(alert{key: key, category: Fleet, sentence: "Resolved: the box " + name + " reached the master again."})
}

// FleetBackupProblem is pkg/fleet's hook: a backup problem of a box, raised at
// every pass of the master's poller while it lasts (said once in six hours),
// or resolved. The sentence is pkg/fleet's, with no text from the box.
func (h *Hub) FleetBackupProblem(key, sentence string, problem bool) {
	if problem {
		h.raise(alert{key: key, category: Fleet, sentence: sentence})
		return
	}
	h.resolve(alert{key: key, category: Fleet, sentence: sentence})
}

// Delivers reports whether an alert of category would be sent: a channel is
// set and the category is on.
func (h *Hub) Delivers(category string) bool {
	c := h.load()
	return len(c.Channels) > 0 && c.Categories[category]
}
