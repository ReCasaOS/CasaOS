package telemetry

import (
	"context"
	"errors"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"github.com/google/uuid"
	"go.uber.org/zap"
)

// Run checks once after a random delay of up to an hour, which spreads the
// boxes an update restarted together, then every hour until ctx is done.
func (t *Telemetry) Run(ctx context.Context) {
	delay := time.NewTimer(startDelay())
	defer delay.Stop()
	select {
	case <-delay.C:
	case <-ctx.Done():
		return
	}
	hourly := time.NewTicker(time.Hour)
	defer hourly.Stop()
	for {
		t.Check(ctx)
		select {
		case <-hourly.C:
		case <-ctx.Done():
			return
		}
	}
}

// startDelay is random, from 0 to 60 minutes, unless the install check sets
// CASAOS_TELEMETRY_START_DELAY (a Go duration).
func startDelay() time.Duration {
	if delay, err := time.ParseDuration(os.Getenv(startDelayEnv)); err == nil && delay >= 0 {
		return delay
	}
	return rand.N(time.Hour)
}

// Check is one hourly pass:
//  1. statistics off: delete upgraded-from, build nothing, send nothing;
//  2. upgraded-from present: send version_changed, delete the file only once sent;
//  3. last_sent absent or a day old: send heartbeat, record last_sent only once sent.
//
// A failure is logged and waits for the next check: nothing is retried sooner
// or queued.
func (t *Telemetry) Check(ctx context.Context) {
	upgradedFrom := t.path(upgradedFromFile)
	state := t.load()
	if !state.Enabled {
		if err := os.Remove(upgradedFrom); err != nil && !errors.Is(err, fs.ErrNotExist) {
			logger.Info("telemetry: cannot delete upgraded-from", zap.Error(err))
		}
		return
	}

	_, err := os.Stat(upgradedFrom)
	changed := err == nil
	// As the spec says: absent or at least 24 hours old. A last_sent in the
	// future (a clock that ran ahead) waits for the clock to catch up.
	due := state.LastSent.IsZero() || t.Now().Sub(state.LastSent) >= 24*time.Hour
	if !changed && !due {
		return
	}

	if state.ID == "" {
		// Made on first use and kept before it is sent: an id that could not be
		// kept would count the box again at every check.
		if state, err = t.modify(func(s *State) {
			if s.ID == "" {
				s.ID = uuid.NewString()
			}
		}); err != nil {
			logger.Info("telemetry: cannot save telemetry.json", zap.Error(err))
			return
		}
		if !state.Enabled {
			return // turned off in the meantime
		}
	}

	properties := t.Properties()
	if changed {
		event := maps.Clone(properties)
		event["previous_distribution"] = t.fileValue(upgradedFromFile)
		if err := t.send(ctx, "version_changed", state.ID, event); err != nil {
			logger.Info("telemetry: send failed", zap.String("event", "version_changed"), zap.Error(err))
		} else if err := os.Remove(upgradedFrom); err != nil {
			logger.Info("telemetry: cannot delete upgraded-from", zap.Error(err))
		}
	}
	if due {
		if err := t.send(ctx, "heartbeat", state.ID, properties); err != nil {
			logger.Info("telemetry: send failed", zap.String("event", "heartbeat"), zap.Error(err))
		} else if _, err := t.modify(func(s *State) { s.LastSent = t.Now().UTC().Truncate(time.Second) }); err != nil {
			logger.Info("telemetry: cannot save telemetry.json", zap.Error(err))
		}
	}
}
