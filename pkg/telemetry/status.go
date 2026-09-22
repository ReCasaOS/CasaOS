package telemetry

// Status is what GET and PUT /v1/sys/telemetry answer in data.
type Status struct {
	Enabled    bool    `json:"enabled"`
	NoticeSeen bool    `json:"notice_seen"`
	Preview    Preview `json:"preview"`
}

// Preview is the heartbeat as it would be sent now. It is built even when the
// statistics are off: it is local and sends nothing.
type Preview struct {
	Event      string         `json:"event"`
	Properties map[string]any `json:"properties"`
}

func (t *Telemetry) Status() Status {
	s := t.load()
	return Status{
		Enabled:    s.Enabled,
		NoticeSeen: s.NoticeSeen,
		Preview:    Preview{Event: "heartbeat", Properties: t.Properties()},
	}
}

// Update sets whichever of enabled and notice_seen is given, and nothing else;
// it rewrites a malformed telemetry.json. Turning the statistics off holds from
// the next check.
func (t *Telemetry) Update(enabled, noticeSeen *bool) (Status, error) {
	if _, err := t.modify(func(s *State) {
		if enabled != nil {
			s.Enabled = *enabled
		}
		if noticeSeen != nil {
			s.NoticeSeen = *noticeSeen
		}
	}); err != nil {
		return Status{}, err
	}
	return t.Status(), nil
}
