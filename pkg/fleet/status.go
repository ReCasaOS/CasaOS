package fleet

// Status is GET /v1/fleet: the role, and the half that is on.
type Status struct {
	Role   string        `json:"role"` // none | master | member
	Master *MasterStatus `json:"master,omitempty"`
	Member *MemberStatus `json:"member,omitempty"`
	Remote *RemoteStatus `json:"remote,omitempty"`
}

// RemoteStatus is the box's remote access as last known locally; no relay call is made for it.
type RemoteStatus struct {
	Relay     string `json:"relay"`
	Addr      string `json:"addr,omitempty"`
	Connected bool   `json:"connected"`
	Error     string `json:"error,omitempty"`
	Alone     bool   `json:"alone"`
}

// Status says which role this box plays and its state. The role is master when
// the master is on (a self-enrolled master also has a member half, its own
// tunnel), member when only member.json exists, else none.
func (f *Fleet) Status() Status {
	s := Status{Role: "none"}
	if st, ok := f.MasterStatus(); ok {
		s.Role, s.Master = "master", &st
		if st.Relay != "" {
			s.Remote = &RemoteStatus{Relay: st.Relay, Addr: st.NetAddr, Connected: st.NetConnected, Error: st.NetError}
		}
	}
	if st, ok := f.MemberStatus(); ok {
		if s.Role == "none" {
			s.Role = "member"
		}
		s.Member = &st
	}
	if s.Remote == nil && s.Master == nil {
		f.mu.Lock()
		rs, ok := f.loadRelay()
		f.mu.Unlock()
		if ok && remoteAlone(rs, false) {
			r := &RemoteStatus{Relay: rs.Relay, Addr: rs.NetAddr, Alone: true}
			f.m.netMu.Lock()
			r.Connected, r.Error = f.netStatus(rs.Relay, "this box is not on its remote access network right now; after a restart, wait a minute. If this lasts, turn remote access off and on again (your devices will have to be added again)")
			f.m.netMu.Unlock()
			s.Remote = r
		}
	}
	return s
}
