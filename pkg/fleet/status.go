package fleet

// Status is GET /v1/fleet: the role, and the half that is on.
type Status struct {
	Role   string        `json:"role"` // none | master | member
	Master *MasterStatus `json:"master,omitempty"`
	Member *MemberStatus `json:"member,omitempty"`
}

// Status says which role this box plays and its state. The role is master when
// the master is on (a self-enrolled master also has a member half, its own
// tunnel), member when only member.json exists, else none.
func (f *Fleet) Status() Status {
	s := Status{Role: "none"}
	if st, ok := f.MasterStatus(); ok {
		s.Role, s.Master = "master", &st
	}
	if st, ok := f.MemberStatus(); ok {
		if s.Role == "none" {
			s.Role = "member"
		}
		s.Member = &st
	}
	return s
}
