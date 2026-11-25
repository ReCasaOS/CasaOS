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
