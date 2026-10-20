package common

import (
	"slices"
	"testing"
)

func TestResourceEventsAreRegistered(t *testing.T) {
	want := []string{"item", "name", "kind", "situation", "value", "since", "source", "minutes"}
	seen := map[string]bool{}
	for _, e := range EventTypes {
		if seen[e.Name] {
			t.Errorf("event %q is registered twice", e.Name)
		}
		seen[e.Name] = true
	}
	for _, name := range []string{"resource:alert-firing", "resource:alert-resolved"} {
		found := false
		for _, e := range EventTypes {
			if e.Name != name {
				continue
			}
			found = true
			if e.SourceID != SERVICENAME {
				t.Errorf("%s: SourceID = %q, want %q", name, e.SourceID, SERVICENAME)
			}
			var got []string
			for _, p := range e.PropertyTypeList {
				got = append(got, p.Name)
			}
			if !slices.Equal(got, want) {
				t.Errorf("%s: properties = %v, want %v", name, got, want)
			}
		}
		if !found {
			t.Errorf("event %q is not registered", name)
		}
	}
}
