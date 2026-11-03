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
