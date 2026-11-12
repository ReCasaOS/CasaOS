package alerts

// ShareGuardProblem is pkg/share's hook: a ban wave on a shared app, missing country data, or a reset guard file,
// said at every resync while it lasts (once in six hours), or resolved. The sentence is pkg/share's.
func (h *Hub) ShareGuardProblem(key, sentence string, problem bool) {
	if problem {
		h.raise(alert{key: key, category: Apps, sentence: sentence})
		return
	}
	h.resolve(alert{key: key, category: Apps, sentence: sentence})
}
