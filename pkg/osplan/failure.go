package osplan

import (
	"fmt"
	"strings"
)

// The codes of a plan that could not be made, as the job says them in its error (BINDING: the page and the CI report
// use them). The four first are also what the POST answers at once, with a 409, before a job starts.
const (
	CodeNotRoot             = "not_root"
	CodeHopNotPlanned       = "hop_not_planned"
	CodeIdentityUnsupported = "identity_unsupported"
	CodeBusy                = "busy"
	CodeNoRoom              = "no_room"
	CodeSourcesFormat       = "sources_format"
	CodeSourcesAlias        = "sources_alias"
	CodeSourcesTarget       = "sources_target"
	CodeSourcesMixedFile    = "sources_mixed_file"
	CodeSourcesCDROM        = "sources_cdrom"
	CodeSandboxUpdateFailed = "sandbox_update_failed"
	CodeSandboxSolverFailed = "sandbox_solver_failed"
	CodePlanUnparsed        = "plan_unparsed"
	CodeTimeout             = "timeout"
	CodeCanceled            = "canceled"
	CodeInternal            = "internal"
)

// ErrorCodes are all the codes a plan can fail with.
var ErrorCodes = []string{
	CodeNotRoot, CodeHopNotPlanned, CodeIdentityUnsupported, CodeBusy, CodeNoRoom,
	CodeSourcesFormat, CodeSourcesAlias, CodeSourcesTarget, CodeSourcesMixedFile, CodeSourcesCDROM,
	CodeSandboxUpdateFailed, CodeSandboxSolverFailed, CodePlanUnparsed, CodeTimeout, CodeCanceled, CodeInternal,
}

// The kinds of a failed update of the sandbox (Failure.Data["kind"] of sandbox_update_failed), decided from apt's fixed
// messages.
const (
	KindNoPubkey      = "no_pubkey"
	KindNotFound      = "not_found"
	KindNetwork       = "network"
	KindReleaseChange = "release_change"
	KindOther         = "other"
)

// A Failure is why a plan could not be made: a code, the phase it was in, a few words of English for whoever reads the
// raw answer (the page says it by code), and the data that makes the code precise. It is an error.
type Failure struct {
	Code   string         `json:"code"`
	Phase  string         `json:"phase,omitempty"`
	Detail string         `json:"detail,omitempty"`
	Data   map[string]any `json:"data,omitempty"`
	// Cause is what really went wrong when the failure is internal: an error of the machine, or a recovered panic with its stack.
	// It is for the log of the core and is never said to the page (Detail is generic): it is not in the JSON.
	Cause error `json:"-"`
}

func (f *Failure) Error() string {
	var b strings.Builder
	b.WriteString(f.Code)
	if f.Phase != "" {
		b.WriteString(" (" + f.Phase + ")")
	}
	if f.Detail != "" {
		b.WriteString(": " + f.Detail)
	}
	return b.String()
}

func fail(code, detail string, data map[string]any) *Failure {
	return &Failure{Code: code, Detail: detail, Data: data}
}

func failf(code string, data map[string]any, format string, args ...any) *Failure {
	return fail(code, fmt.Sprintf(format, args...), data)
}

// because is the failure with the error that caused it, for the log.
func (f *Failure) because(err error) *Failure {
	if f != nil && err != nil {
		f.Cause = err
	}
	return f
}

// at is the failure in a phase.
func (f *Failure) at(phase string) *Failure {
	if f != nil && f.Phase == "" {
		f.Phase = phase
	}
	return f
}
