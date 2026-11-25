package osmodule

import "sort"

// Merge gives, for each module name in either catalogue, the entry to keep: the one with the highest tag that the system accepts; if the system accepts neither, the one with the highest tag;
// the same tag in both: the shipped one. The merged catalogue is sorted by name and has the serial, the date and the versions withdrawn of the online one (none when it is empty; a shipped
// catalogue has none). online says, by name, whether the kept entry is the online one.
func Merge(shipped, online Catalogue, accepts func(Entry) bool) (Catalogue, map[string]bool) {
	merged := Catalogue{Schema: shipped.Schema, Serial: online.Serial, Generated: online.Generated, Withdrawn: online.Withdrawn}
	fromOnline := map[string]bool{}
	for _, s := range shipped.Modules {
		kept := s
		if o, ok := online.Find(s.Name); ok && onlineWins(s, o, accepts) {
			kept, fromOnline[s.Name] = o, true
		} else {
			fromOnline[s.Name] = false
		}
		merged.Modules = append(merged.Modules, kept)
	}
	for _, o := range online.Modules {
		if _, ok := shipped.Find(o.Name); !ok {
			merged.Modules = append(merged.Modules, o)
			fromOnline[o.Name] = true
		}
	}
	sort.Slice(merged.Modules, func(i, j int) bool { return merged.Modules[i].Name < merged.Modules[j].Name })
	return merged, fromOnline
}

// onlineWins says whether the online entry beats the shipped one of the same module.
func onlineWins(s, o Entry, accepts func(Entry) bool) bool {
	if sa, oa := accepts(s), accepts(o); sa != oa {
		return oa
	}
	c, ok := CompareTags(o.Tag, s.Tag)
	return ok && c > 0
}
