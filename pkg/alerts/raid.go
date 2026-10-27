package alerts

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/model"
)

// raidEvery is how often the software RAID arrays are looked at: a degraded
// array is one disk away from losing its data, so an hour is too long.
const raidEvery = 5 * time.Minute

// raidArray is an array of LocalStorage's GET /v1/disks/raid.
type raidArray struct {
	Name    string       `json:"name"`
	Level   string       `json:"level"`
	State   string       `json:"state"` // ok, degraded, rebuilding, resyncing, checking, reshaping or inactive
	Disks   int          `json:"disks"`
	Missing int          `json:"missing"`
	Members []raidMember `json:"members"`
}

type raidMember struct {
	Disk  string `json:"disk"`
	State string `json:"state"` // active, rebuilding, spare or faulty
}

func (a raidArray) String() string {
	return a.Name + " (" + strings.ToUpper(a.Level) + ")"
}

// watchRAID looks at the arrays five minutes after the start, then every
// raidEvery, until ctx is done.
func (h *Hub) watchRAID(ctx context.Context) {
	timer := time.NewTimer(5 * time.Minute)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
		case <-ctx.Done():
			return
		}
		h.pollRAID(ctx)
		timer.Reset(raidEvery)
	}
}

// pollRAID asks LocalStorage for the arrays and raises or resolves what they
// show. LocalStorage unreachable, or one too old to answer: nothing is said.
func (h *Hub) pollRAID(ctx context.Context) {
	runtimePath := h.RuntimePath()
	address, err := localStorage(ctx, runtimePath)
	if err != nil {
		return
	}
	var arrays []raidArray
	if get(ctx, address+"/v1/disks/raid", runtimePath, &model.Result{Data: &arrays}) != nil {
		return
	}
	raise, resolve := raidAlerts(arrays)
	for _, a := range resolve {
		h.resolve(a)
	}
	for _, a := range raise {
		h.raise(a)
	}
}

// raidAlerts is what the arrays show: an array that lacks a disk, has a failed
// one or does not run, and each of them cleared. A rebuild that ends clears the
// degraded alert: its resolution says the array is complete again.
func raidAlerts(arrays []raidArray) (raise, resolve []alert) {
	for _, a := range arrays {
		degraded := alert{key: "raid:" + a.Name + ":degraded", category: Disks}
		inactive := alert{key: "raid:" + a.Name + ":inactive", category: Disks}
		var failed []string
		for _, m := range a.Members {
			if m.State == "faulty" {
				failed = append(failed, m.Disk)
			}
		}
		switch a.State {
		case "inactive":
			inactive.sentence = "RAID " + a.String() + " is not running: its data cannot be reached."
			raise = append(raise, inactive)
		case "degraded", "rebuilding":
			switch {
			case len(failed) > 0 && a.Missing == 0:
				degraded.sentence = fmt.Sprintf("Disk %s has failed in RAID %s; a spare took its place. Replace the failed disk.", strings.Join(failed, ", "), a)
			case len(failed) > 0:
				degraded.sentence = fmt.Sprintf("RAID %s is degraded: disk %s has failed. Replace it before another disk fails.", a, strings.Join(failed, ", "))
			case a.Missing == 1:
				degraded.sentence = fmt.Sprintf("RAID %s is degraded: 1 of its %d disks is missing. Replace it before another disk fails.", a, a.Disks)
			default:
				degraded.sentence = fmt.Sprintf("RAID %s is degraded: %d of its %d disks are missing. Replace them before another disk fails.", a, a.Missing, a.Disks)
			}
			raise = append(raise, degraded)
			inactive.sentence = "Resolved: RAID " + a.String() + " is running again."
			resolve = append(resolve, inactive)
		default:
			degraded.sentence = "Resolved: RAID " + a.String() + " is complete again."
			inactive.sentence = "Resolved: RAID " + a.String() + " is running again."
			resolve = append(resolve, degraded, inactive)
		}
	}
	return raise, resolve
}
