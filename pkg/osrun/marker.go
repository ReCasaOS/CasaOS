package osrun

import (
	"errors"
	"io"
	"strconv"
	"strings"
	"time"
)

// The unit and the core tell a run's story in a file of their own, one line per event, which apt and dpkg never write to:
//
//	CASAOS_OSU_<KIND> <nonce> <n> <fields...>
//
// n is the number of the attempt the line belongs to, 0 for the lines of the run itself. Every field is one of a closed
// set or matches [A-Za-z0-9_.:+~-]{1,64}; times are 2006-01-02T15:04:05Z; a line is at most 256 bytes; and the nonce is
// the one the core wrote on the first line. A line that does not have exactly that shape is not a marker, whatever it looks
// like, and the reader only counts it.

const markerPrefix = "CASAOS_OSU_"

// The kinds of marker. The core writes the first nine, the unit's driver the others.
const (
	MkQueued          = "QUEUED"
	MkRun             = "RUN"
	MkAttempt         = "ATTEMPT"
	MkStartFailed     = "START_FAILED"
	MkRebootRequested = "REBOOT_REQUESTED"
	MkRebootFailed    = "REBOOT_FAILED"
	MkTakeover        = "TAKEOVER"
	MkAck             = "ACK"
	MkBootSeen        = "BOOT_SEEN"
	MkUnitStart       = "UNIT_START"
	MkBlockStart      = "BLOCK_START"
	MkBlockDone       = "BLOCK_DONE"
	MkBlockSkipped    = "BLOCK_SKIPPED"
	MkStop            = "STOP"
	MkUnwound         = "UNWOUND"
	MkRefreshDone     = "REFRESH_DONE"
	MkRehearsalReady  = "REHEARSAL_READY"
	MkRebootPending   = "REBOOT_PENDING"
	MkRebootNotNeeded = "REBOOT_NOT_NEEDED"
	MkVerifyDone      = "VERIFY_DONE"
	MkUnitEnd         = "UNIT_END"
)

// MarkerKinds are the kinds of marker.
var MarkerKinds = []string{
	MkQueued, MkRun, MkAttempt, MkStartFailed, MkRebootRequested, MkRebootFailed, MkTakeover, MkAck, MkBootSeen,
	MkUnitStart, MkBlockStart, MkBlockDone, MkBlockSkipped, MkStop, MkUnwound, MkRefreshDone, MkRehearsalReady,
	MkRebootPending, MkRebootNotNeeded, MkVerifyDone, MkUnitEnd,
}

// ErrMarkersTooLarge is what ParseMarkers answers, with what it read, when the file is longer than it reads: a run's
// story is a few kilobytes, and one that is not is not to be trusted to say how it ended.
var ErrMarkersTooLarge = errors.New("the log of the upgrade is longer than it can be")

// A Marker is one valid line of the log. Only the fields its Kind has are set; the others are empty. A field that the
// line writes as "-" (a boot id, a plan id or a block that is not known) is the empty string here.
type Marker struct {
	Kind string
	// N is the attempt the marker belongs to, 0 for the markers of the run.
	N int
	// At is the time the marker says, zero for the kinds that carry none.
	At time.Time

	// RUN
	RunID, Hop, PlanID, RunKind string
	// ATTEMPT (Phase, From: the block the attempt is meant to start at, Who: auto or owner) and UNIT_START (Phase)
	Phase, From, Who string
	// RUN, BOOT_SEEN, UNIT_START, REBOOT_PENDING and VERIFY_DONE: the id of a boot
	Boot string
	// UNIT_START: the driver's process id
	PID int
	// BLOCK_START, BLOCK_DONE, BLOCK_SKIPPED and STOP: the place of the block in the attempt's list, and which one it is
	// (one of BlockIDs, or for a STOP also StopDriver, StopList or StopUnwind)
	Index int
	ID    string
	// STOP: the status of the block, how it is told (always one of Classes: a word that is not one is unknown), and whether
	// the driver thought the point of no return had been passed
	RC    int
	Class string
	PNR   bool
	// ACK (discard, acknowledge), UNWOUND (ok, partial), REFRESH_DONE (yes, no) and UNIT_END (ok, stopped)
	Result string
	// REFRESH_DONE: why a restart is wanted, REBOOT_PENDING and REBOOT_NOT_NEEDED: the kernel
	Why, Kernel string
}

const timeLayout = "2006-01-02T15:04:05Z"

// FormatTime is how a marker writes a time.
func FormatTime(t time.Time) string { return t.UTC().Format(timeLayout) }

func parseTime(s string) (time.Time, bool) {
	if len(s) != len(timeLayout) {
		return time.Time{}, false
	}
	t, err := time.Parse(timeLayout, s)
	if err != nil || t.IsZero() {
		return time.Time{}, false
	}
	return t, true
}

// ValidNonce reports whether s is 32 lower case hex digits, what the core makes the nonce of a run of.
func ValidNonce(s string) bool {
	if len(s) != 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// ValidUUID reports whether s is a lower case UUID, the shape of a run's id and of the kernel's boot ids.
func ValidUUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case i == 8 || i == 13 || i == 18 || i == 23:
			if c != '-' {
				return false
			}
		case (c < '0' || c > '9') && (c < 'a' || c > 'f'):
			return false
		}
	}
	return true
}

// isToken reports whether s matches [A-Za-z0-9_.:+~-]{1,64}.
func isToken(s string) bool {
	if len(s) == 0 || len(s) > 64 {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9':
		case c == '_', c == '.', c == ':', c == '+', c == '~', c == '-':
		default:
			return false
		}
	}
	return true
}

// smallInt reads a number written in decimal with no sign and no leading zero, of at most max.
func smallInt(s string, max int) (int, bool) {
	if s == "" || len(s) > 9 || (len(s) > 1 && s[0] == '0') {
		return 0, false
	}
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return 0, false
		}
		n = n*10 + int(s[i]-'0')
	}
	return n, n <= max
}

// A field is one word of a marker: what it may be, how it is put in a Marker and how it is taken out.
type field struct {
	valid func(string) bool
	set   func(*Marker, string)
	get   func(*Marker) string
}

func oneOf(words ...string) func(string) bool {
	return func(s string) bool { return isOneOf(words, s) }
}

func dashOr(valid func(string) bool) func(string) bool {
	return func(s string) bool { return s == "-" || valid(s) }
}

func undash(s string) string {
	if s == "-" {
		return ""
	}
	return s
}

func dash(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func intField(max int, p func(*Marker) *int) field {
	return field{
		valid: func(s string) bool { _, ok := smallInt(s, max); return ok },
		set:   func(m *Marker, s string) { *p(m), _ = smallInt(s, max) },
		get:   func(m *Marker) string { return strconv.Itoa(*p(m)) },
	}
}

func textField(valid func(string) bool, p func(*Marker) *string) field {
	return field{
		valid: valid,
		set:   func(m *Marker, s string) { *p(m) = s },
		get:   func(m *Marker) string { return *p(m) },
	}
}

// dashField is a word that is "-" for the empty string.
func dashField(valid func(string) bool, p func(*Marker) *string) field {
	return field{
		valid: dashOr(valid),
		set:   func(m *Marker, s string) { *p(m) = undash(s) },
		get:   func(m *Marker) string { return dash(*p(m)) },
	}
}

func isClassWord(s string) bool {
	if s == "" || len(s) > 32 {
		return false
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c < 'a' || c > 'z') && c != '_' {
			return false
		}
	}
	return true
}

var (
	fTime = field{
		valid: func(s string) bool { _, ok := parseTime(s); return ok },
		set:   func(m *Marker, s string) { m.At, _ = parseTime(s) },
		get:   func(m *Marker) string { return FormatTime(m.At) },
	}
	fPhase   = textField(ValidPhase, func(m *Marker) *string { return &m.Phase })
	fFrom    = dashField(ValidBlock, func(m *Marker) *string { return &m.From })
	fWho     = textField(oneOf(WhoAuto, WhoOwner), func(m *Marker) *string { return &m.Who })
	fRunID   = textField(ValidUUID, func(m *Marker) *string { return &m.RunID })
	fHop     = textField(isToken, func(m *Marker) *string { return &m.Hop })
	fPlan    = dashField(isToken, func(m *Marker) *string { return &m.PlanID })
	fRunKind = textField(oneOf(RunKinds...), func(m *Marker) *string { return &m.RunKind })
	fBoot    = dashField(ValidUUID, func(m *Marker) *string { return &m.Boot })
	fPID     = intField(99999999, func(m *Marker) *int { return &m.PID })
	fIndex   = intField(999, func(m *Marker) *int { return &m.Index })
	fBlock   = textField(ValidBlock, func(m *Marker) *string { return &m.ID })
	fStopID  = textField(func(s string) bool { return ValidBlock(s) || isOneOf([]string{StopDriver, StopList, StopUnwind}, s) },
		func(m *Marker) *string { return &m.ID })
	fRC    = intField(255, func(m *Marker) *int { return &m.RC })
	fClass = field{
		valid: isClassWord,
		set: func(m *Marker, s string) {
			m.Class = s
			if !ValidClass(s) {
				// A word of a block that the core has no sentence for.
				m.Class = ClassUnknown
			}
		},
		get: func(m *Marker) string { return m.Class },
	}
	fPNR = field{
		valid: oneOf("0", "1"),
		set:   func(m *Marker, s string) { m.PNR = s == "1" },
		get: func(m *Marker) string {
			if m.PNR {
				return "1"
			}
			return "0"
		},
	}
	fAck     = textField(oneOf("discard", "acknowledge"), func(m *Marker) *string { return &m.Result })
	fUnwound = textField(oneOf("ok", "partial"), func(m *Marker) *string { return &m.Result })
	fYesNo   = textField(oneOf("yes", "no"), func(m *Marker) *string { return &m.Result })
	fEnd     = textField(oneOf("ok", "stopped"), func(m *Marker) *string { return &m.Result })
	fWhy     = textField(isToken, func(m *Marker) *string { return &m.Why })
	fKernel  = textField(isToken, func(m *Marker) *string { return &m.Kernel })
)

// A schema is the shape of a kind of marker: whether it belongs to the run (n is 0) or to an attempt (n is 1 or more),
// and its fields after n.
type schema struct {
	ofRun  bool
	fields []field
}

var schemas = map[string]schema{
	MkQueued:          {true, []field{fTime}},
	MkRun:             {true, []field{fRunID, fHop, fPlan, fRunKind, fBoot}},
	MkAttempt:         {false, []field{fPhase, fFrom, fWho, fTime}},
	MkStartFailed:     {false, []field{fTime}},
	MkRebootRequested: {true, []field{fTime}},
	MkRebootFailed:    {true, []field{fTime}},
	MkTakeover:        {true, []field{fTime}},
	MkAck:             {true, []field{fAck, fTime}},
	MkBootSeen:        {true, []field{fBoot, fTime}},
	MkUnitStart:       {false, []field{fPhase, fBoot, fPID, fTime}},
	MkBlockStart:      {false, []field{fIndex, fBlock, fTime}},
	MkBlockDone:       {false, []field{fIndex, fBlock, fTime}},
	MkBlockSkipped:    {false, []field{fIndex, fBlock}},
	MkStop:            {false, []field{fIndex, fStopID, fRC, fClass, fPNR, fTime}},
	MkUnwound:         {false, []field{fUnwound, fTime}},
	MkRefreshDone:     {false, []field{fYesNo, fWhy, fTime}},
	MkRehearsalReady:  {false, []field{fTime}},
	MkRebootPending:   {false, []field{fBoot, fKernel, fTime}},
	MkRebootNotNeeded: {false, []field{fKernel, fTime}},
	MkVerifyDone:      {false, []field{fBoot, fTime}},
	MkUnitEnd:         {false, []field{fEnd, fTime}},
}

// parseMarker reads a line as a marker of any run and says which nonce it carries.
func parseMarker(line string) (m Marker, nonce string, ok bool) {
	rest, found := strings.CutPrefix(line, markerPrefix)
	if !found || len(line) > markerLineMax {
		return Marker{}, "", false
	}
	words := strings.Split(rest, " ")
	if len(words) < 3 {
		return Marker{}, "", false
	}
	sch, found := schemas[words[0]]
	if !found || !ValidNonce(words[1]) {
		return Marker{}, "", false
	}
	n, found := smallInt(words[2], 999)
	if !found || sch.ofRun != (n == 0) || len(words)-3 != len(sch.fields) {
		return Marker{}, "", false
	}
	m = Marker{Kind: words[0], N: n}
	for i, f := range sch.fields {
		if !f.valid(words[3+i]) {
			return Marker{}, "", false
		}
		f.set(&m, words[3+i])
	}
	return m, words[1], true
}

// Line is the marker as the core writes it to the log, with its newline. It fails for a marker that no reader would
// take: a kind that is not one, fields that are not valid, a line that is too long.
func (m Marker) Line(nonce string) (string, error) {
	sch, found := schemas[m.Kind]
	if !found || !ValidNonce(nonce) {
		return "", errors.New("not a marker of the upgrade")
	}
	words := []string{markerPrefix + m.Kind, nonce, strconv.Itoa(m.N)}
	for _, f := range sch.fields {
		words = append(words, f.get(&m))
	}
	line := strings.Join(words, " ")
	if _, got, ok := parseMarker(line); !ok || got != nonce {
		return "", errors.New("not a valid marker of the upgrade: " + m.Kind)
	}
	return line + "\n", nil
}

// ParseMarkers reads the log of a run. The nonce is the one on the first line, which must be the core's QUEUED marker;
// with no such line there is no run, and the answer is empty. After that a line is a marker only if it has the exact
// shape of one with that nonce: anything else (apt's words, a forged nonce, a carriage return, a NUL, a line that is
// too long or not whole, an empty one) is counted and left out. The markers are in the order of the file, the QUEUED one
// first. At most 1 MiB is read; a longer file gives ErrMarkersTooLarge with what was before the limit. It never panics
// on any bytes.
func ParseMarkers(r io.Reader) (nonce string, ms []Marker, ignored int, err error) {
	data, err := io.ReadAll(io.LimitReader(r, markersMax+1))
	if err != nil {
		return "", nil, 0, err
	}
	if len(data) > markersMax {
		data, err = data[:markersMax], ErrMarkersTooLarge
	}
	lines := strings.Split(string(data), "\n")
	// What follows the last newline is a line that was not finished (or cut by the limit), and so is not one.
	if last := lines[len(lines)-1]; last != "" {
		ignored++
	}
	lines = lines[:len(lines)-1]
	if len(lines) == 0 {
		return "", nil, ignored, err
	}
	first, nonce, ok := parseMarker(lines[0])
	if !ok || first.Kind != MkQueued {
		return "", nil, ignored + len(lines), err
	}
	ms = append(ms, first)
	for _, line := range lines[1:] {
		m, got, ok := parseMarker(line)
		if !ok || got != nonce || m.Kind == MkQueued {
			ignored++
			continue
		}
		ms = append(ms, m)
	}
	return nonce, ms, ignored, err
}
