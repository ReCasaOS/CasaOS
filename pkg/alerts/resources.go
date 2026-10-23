package alerts

import (
	"regexp"
	"slices"
	"strconv"
)

// resourceEvents are the events of the machines' statistics: a host, a VM or a
// container whose CPU or memory stays high, or a VM that crashed. AppManagement,
// the virt module and the core publish them, the same way.
var resourceEvents = []string{"resource:alert-firing", "resource:alert-resolved"}

var (
	resourceKinds      = []string{"app-container", "container", "vm", "host"}
	resourceSituations = []string{"cpu_high", "mem_high", "vm_crashed"}
	// number is a value fit to print as it came: no exponent, no NaN, no hex.
	number = regexp.MustCompile(`^\d{1,9}(\.\d{1,3})?$`)
)

// Handle is the entry of a source that runs in this process: it takes an event
// as the message bus would hand it.
func (h *Hub) Handle(name string, properties map[string]string) {
	h.onEvent(name, properties)
}

// isMutedResource says whether an event of resourceEvents is one a busy box
// (a Docker update, an OS upgrade) makes up: a high CPU or memory firing, not a
// crash. A resolution is never one: resolve sends only for an alert that was
// sent, and dropping it would leave that alert open on the phone for good.
func isMutedResource(name string, p map[string]string) bool {
	return name == "resource:alert-firing" && (p["situation"] == "cpu_high" || p["situation"] == "mem_high")
}

// fromResource is the alert of one resource event, and whether it resolves the
// alert rather than raises it. It is not one when the item, the kind or the
// situation is missing or unknown.
func fromResource(name string, p map[string]string) (a alert, resolved, ok bool) {
	if !slices.Contains(resourceEvents, name) || p["item"] == "" ||
		!slices.Contains(resourceKinds, p["kind"]) || !slices.Contains(resourceSituations, p["situation"]) {
		return alert{}, false, false
	}
	resolved = name == "resource:alert-resolved"
	who := truncate(plain(p["name"]), 100)
	if who == "" {
		who = "A resource"
	}
	var sentence string
	switch p["situation"] {
	case "cpu_high", "mem_high":
		what, normal := "CPU", "CPU is back to normal"
		if p["situation"] == "mem_high" {
			what, normal = "memory", "memory is back to normal"
		}
		if resolved {
			sentence = "Resolved: " + who + ": " + normal + "."
			break
		}
		sentence = who + ": " + what + " high"
		if number.MatchString(p["value"]) {
			sentence = who + ": " + what + " at " + p["value"] + " %"
		}
		if minutes, err := strconv.Atoi(p["minutes"]); err == nil && minutes >= 1 && minutes <= 120 {
			sentence += " for " + strconv.Itoa(minutes) + " min"
		}
		sentence += "."
	default: // vm_crashed
		sentence = who + " crashed."
		if resolved {
			sentence = "Resolved: " + who + " runs again."
		}
	}
	key := "resource:" + p["kind"] + ":" + p["item"] + ":" + p["situation"]
	return alert{key: key, category: Resources, sentence: sentence}, resolved, true
}
