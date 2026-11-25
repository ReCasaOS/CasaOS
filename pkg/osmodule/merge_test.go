package osmodule

import (
	"reflect"
	"testing"
)

func TestMerge(t *testing.T) {
	e := func(name, tag, commit string) Entry { return Entry{Name: name, Tag: tag, Commit: commit} }
	// the system refuses what has "no" for a commit
	accepts := func(x Entry) bool { return x.Commit != "no" }
	for _, c := range []struct {
		name            string
		shipped, online []Entry
		want            []Entry
		wantOnline      map[string]bool
	}{
		{"online higher and accepted", []Entry{e("a", "v0.1.0", "s")}, []Entry{e("a", "v0.2.0", "o")}, []Entry{e("a", "v0.2.0", "o")}, map[string]bool{"a": true}},
		{"online higher and refused, shipped accepted", []Entry{e("a", "v0.1.0", "s")}, []Entry{e("a", "v0.2.0", "no")}, []Entry{e("a", "v0.1.0", "s")}, map[string]bool{"a": false}},
		{"online lower and accepted, shipped refused", []Entry{e("a", "v0.3.0", "no")}, []Entry{e("a", "v0.2.0", "o")}, []Entry{e("a", "v0.2.0", "o")}, map[string]bool{"a": true}},
		{"neither accepted: the highest, online", []Entry{e("a", "v0.1.0", "no")}, []Entry{e("a", "v0.2.0", "no")}, []Entry{e("a", "v0.2.0", "no")}, map[string]bool{"a": true}},
		{"neither accepted: the highest, shipped", []Entry{e("a", "v0.3.0", "no")}, []Entry{e("a", "v0.2.0", "no")}, []Entry{e("a", "v0.3.0", "no")}, map[string]bool{"a": false}},
		{"same tag, other commit: shipped", []Entry{e("a", "v0.1.0", "s")}, []Entry{e("a", "v0.1.0", "o")}, []Entry{e("a", "v0.1.0", "s")}, map[string]bool{"a": false}},
		{"online lower: shipped", []Entry{e("a", "v0.10.0", "s")}, []Entry{e("a", "v0.9.0", "o")}, []Entry{e("a", "v0.10.0", "s")}, map[string]bool{"a": false}},
		{"only online", nil, []Entry{e("a", "v0.1.0", "o")}, []Entry{e("a", "v0.1.0", "o")}, map[string]bool{"a": true}},
		{"only shipped", []Entry{e("a", "v0.1.0", "s")}, nil, []Entry{e("a", "v0.1.0", "s")}, map[string]bool{"a": false}},
		{"sorted by name", []Entry{e("c", "v0.1.0", "s"), e("a", "v0.1.0", "s")}, []Entry{e("b", "v0.1.0", "o")},
			[]Entry{e("a", "v0.1.0", "s"), e("b", "v0.1.0", "o"), e("c", "v0.1.0", "s")}, map[string]bool{"a": false, "b": true, "c": false}},
	} {
		got, fromOnline := Merge(Catalogue{Schema: 1, Modules: c.shipped}, Catalogue{Schema: 2, Serial: 4, Generated: "g", Modules: c.online}, accepts)
		if !reflect.DeepEqual(got.Modules, c.want) || !reflect.DeepEqual(fromOnline, c.wantOnline) {
			t.Errorf("%s: %+v %v", c.name, got.Modules, fromOnline)
		}
		if got.Serial != 4 || got.Generated != "g" {
			t.Errorf("%s: serial %d, generated %q", c.name, got.Serial, got.Generated)
		}
	}
}

func TestMergeWithNoOnlineCatalogueIsTheShippedOne(t *testing.T) {
	shipped := Catalogue{Schema: 1, Modules: []Entry{{Name: "a", Tag: "v0.1.0"}, {Name: "b", Tag: "v0.2.0"}}}
	got, fromOnline := Merge(shipped, Catalogue{}, func(Entry) bool { return true })
	if !reflect.DeepEqual(got, shipped) || got.Serial != 0 || got.Generated != "" || fromOnline["a"] || fromOnline["b"] {
		t.Fatalf("%+v %v", got, fromOnline)
	}
}

func TestMergeKeepsTheVersionsWithdrawnOfTheOnlineCatalogue(t *testing.T) {
	withdrawn := []Withdrawn{{Name: "a", Tag: "v0.1.0", Reason: "Broken."}}
	got, _ := Merge(Catalogue{Schema: 1, Modules: []Entry{{Name: "a", Tag: "v0.1.0"}}}, Catalogue{Schema: 3, Serial: 2, Withdrawn: withdrawn}, func(Entry) bool { return true })
	if !reflect.DeepEqual(got.Withdrawn, withdrawn) {
		t.Fatalf("%+v", got.Withdrawn)
	}
}

// A version the online catalogue withdrew is not what the box takes when it has a choice: it counts as one the system does not accept, in either file.
func TestMergeDoesNotPreferAWithdrawnVersion(t *testing.T) {
	e := func(name, tag string) Entry { return Entry{Name: name, Tag: tag} }
	flag := func(name, tag string) Withdrawn { return Withdrawn{Name: name, Tag: tag, Reason: "Broken."} }
	accepts := func(Entry) bool { return true }
	for _, c := range []struct {
		name            string
		shipped, online []Entry
		withdrawn       []Withdrawn
		want            Entry
		fromOnline      bool
	}{
		{"online flagged, shipped accepted", []Entry{e("a", "v0.2.0")}, []Entry{e("a", "v0.2.1")}, []Withdrawn{flag("a", "v0.2.1")}, e("a", "v0.2.0"), false},
		{"online flagged, no shipped entry", nil, []Entry{e("a", "v0.2.1")}, []Withdrawn{flag("a", "v0.2.1")}, e("a", "v0.2.1"), true},
		{"shipped flagged, online higher", []Entry{e("a", "v0.2.0")}, []Entry{e("a", "v0.2.1")}, []Withdrawn{flag("a", "v0.2.0")}, e("a", "v0.2.1"), true},
		{"shipped flagged, online lower", []Entry{e("a", "v0.2.0")}, []Entry{e("a", "v0.1.9")}, []Withdrawn{flag("a", "v0.2.0")}, e("a", "v0.1.9"), true},
		{"shipped flagged and nothing else", []Entry{e("a", "v0.2.0")}, nil, []Withdrawn{flag("a", "v0.2.0")}, e("a", "v0.2.0"), false},
		{"both flagged: the highest", []Entry{e("a", "v0.2.0")}, []Entry{e("a", "v0.2.1")}, []Withdrawn{flag("a", "v0.2.0"), flag("a", "v0.2.1")}, e("a", "v0.2.1"), true},
		{"another pair flagged", []Entry{e("a", "v0.2.0")}, []Entry{e("a", "v0.2.1")}, []Withdrawn{flag("b", "v0.2.1"), flag("a", "v0.1.0")}, e("a", "v0.2.1"), true},
	} {
		got, fromOnline := Merge(Catalogue{Schema: 1, Modules: c.shipped}, Catalogue{Schema: 3, Serial: 4, Modules: c.online, Withdrawn: c.withdrawn}, accepts)
		if len(got.Modules) != 1 || got.Modules[0].Tag != c.want.Tag || fromOnline["a"] != c.fromOnline {
			t.Errorf("%s: %+v %v", c.name, got.Modules, fromOnline)
		}
	}
}
