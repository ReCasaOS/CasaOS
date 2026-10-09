package dockerpkg

import "time"

// FailedMarker is the terminal line of a run that failed, as the unit writes it: the core writes
// it itself for a unit that could not be started, so that the log is a whole run. reason is one
// of the Fail constants.
func FailedMarker(nonce string, at time.Time, reason string) string {
	return markerPrefix + "FAILED " + nonce + " " + at.UTC().Format(time.RFC3339) + " " + reason + "\n"
}
