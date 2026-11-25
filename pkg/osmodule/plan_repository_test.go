package osmodule

import "testing"

// from repo is the catalogue with the entry's repository set (schema 3 has one per entry).
func from(c Catalogue, repo string) Catalogue {
	c.Modules = append([]Entry(nil), c.Modules...)
	c.Modules[0].Repository = repo
	return c
}

// The plan's id binds the repository an entry comes from: the same archive from another repository is another plan, for an install and for an update.
func TestThePlanIdBindsTheRepository(t *testing.T) {
	bare := Plan(virtCatalogue(t), "virt", newBox()).PlanID
	a := Plan(from(virtCatalogue(t), "someone/virt"), "virt", newBox()).PlanID
	b := Plan(from(virtCatalogue(t), "other/virt"), "virt", newBox()).PlanID
	if a == "" || a == b || a == bare || b == bare {
		t.Errorf("install ids: none %s, someone %s, other %s", bare, a, b)
	}
	if len(a) != 3+32 || a[:3] != "m1-" {
		t.Errorf("shape %q", a)
	}
	ua := PlanUpdate(from(newer(t), "someone/virt"), "virt", newUpdBox(t)).PlanID
	ub := PlanUpdate(from(newer(t), "other/virt"), "virt", newUpdBox(t)).PlanID
	if ua == "" || ua == ub {
		t.Errorf("update ids: someone %s, other %s", ua, ub)
	}
}
