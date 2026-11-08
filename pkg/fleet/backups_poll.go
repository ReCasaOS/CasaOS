package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// pollTimeout bounds one whole pass over one box.
const pollTimeout = 20 * time.Second

// boxCaller sends one request to a box and returns what it answered: Relay in
// production, a fake in tests.
type boxCaller func(ctx context.Context, boxID, method, path string) (Response, error)

const (
	errUnsupported = "this box does not support backup monitoring yet: update it"
	errInterrupted = "interrupted"
	amPath         = "/v2/app_management"
)

var errBadAnswer = errors.New("the box's answer could not be read")

// statusError is a box answering something else than 200. It carries the
// status only: the text a box sends back is never kept.
type statusError int

func (e statusError) Error() string { return fmt.Sprintf("the box answered with status %d", int(e)) }

// fetch asks one route of the box and decodes the 200 answer into dst. Only
// the fields dst lists are read.
func (f *Fleet) fetch(ctx context.Context, boxID, path string, dst any) error {
	res, err := f.call(ctx, boxID, http.MethodGet, path)
	if err != nil {
		return err
	}
	if res.Status != http.StatusOK {
		return statusError(res.Status)
	}
	if json.Unmarshal(res.Body, dst) != nil {
		return errBadAnswer
	}
	return nil
}

// pollBox reads a box's backups through its tunnel. The runs, schedules and
// operations routes are required: without them the result carries only an
// Error (storeBackups then keeps the last good answer). The rest is optional,
// and an old box that lacks it simply has less to show.
func (f *Fleet) pollBox(ctx context.Context, box Box) BoxBackups {
	ctx, cancel := context.WithTimeout(ctx, pollTimeout)
	defer cancel()

	var in backupInput
	var r struct {
		Data []runRow `json:"data"`
	}
	var s struct {
		Data []scheduleRow `json:"data"`
	}
	var ops struct {
		Operations    []opRow `json:"operations"`
		OSUpgradeOpen bool    `json:"os_upgrade_open"`
	}
	for _, step := range []struct {
		path string
		dst  any
	}{{"/backup/runs", &r}, {"/backup/schedules", &s}, {"/operations", &ops}} {
		if err := f.fetch(ctx, box.ID, amPath+step.path, step.dst); err != nil {
			return failedPoll(box, err)
		}
	}
	in.Runs, in.Schedules, in.Operations, in.OSUpgradeOpen = r.Data, s.Data, ops.Operations, ops.OSUpgradeOpen

	var v struct {
		Data []verifyRow `json:"data"`
	}
	if f.fetch(ctx, box.ID, amPath+"/backup/verifications", &v) == nil {
		in.Verifications = v.Data
	}
	var vs struct {
		Data *VerificationSchedule `json:"data"`
	}
	if f.fetch(ctx, box.ID, amPath+"/backup/verification-schedule", &vs) == nil {
		in.VerifySchedule = vs.Data
	}

	// The restic state of each distinct destination. A name the relay table
	// refuses is not asked, and one whose answer is missing stays Known=false.
	in.Restic = map[string]resticRow{}
	for _, row := range in.Schedules {
		d := row.Destination
		path := amPath + "/backup/destinations/" + d + "/restic"
		if _, done := in.Restic[d]; done {
			continue
		}
		in.Restic[d] = resticRow{}
		if _, ok := Allowed(http.MethodGet, path); !ok {
			continue
		}
		var rs struct {
			Data struct {
				Kept      bool         `json:"kept"`
				LastCheck *ResticCheck `json:"last_check"`
			} `json:"data"`
		}
		if f.fetch(ctx, box.ID, path, &rs) == nil {
			in.Restic[d] = resticRow{Known: true, Kept: rs.Data.Kept, Check: rs.Data.LastCheck}
		}
	}

	out := BoxBackups{BoxID: box.ID, Name: box.Name, Online: true, PolledAt: f.Now(), VerificationSchedule: in.VerifySchedule}
	out.Couples, out.State, out.Green, out.Problems = assess(in, out.PolledAt)
	return out
}

// failedPoll is what a poll that could not read a required route returns: no
// content, only why.
func failedPoll(box Box, err error) BoxBackups {
	out := BoxBackups{BoxID: box.ID, Name: box.Name, Online: !errors.Is(err, ErrOffline), State: "unknown", Couples: []Couple{}, Error: err.Error()}
	if errors.Is(err, context.Canceled) {
		out.Error = errInterrupted // the caller is shutting down: not a fact about the box
	}
	var se statusError
	if errors.As(err, &se) && (se == http.StatusForbidden || se == http.StatusNotFound) {
		out.Error = errUnsupported
	}
	return out
}
