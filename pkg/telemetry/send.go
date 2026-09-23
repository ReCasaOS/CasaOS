package telemetry

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/utils/logger"
	"go.uber.org/zap"
)

// send posts one event to PostHog's capture endpoint. A transport error or an
// answer outside 2xx is a failure: the caller logs it and waits for the next check.
// A success is logged too, so that the journal shows what left the box.
func (t *Telemetry) send(ctx context.Context, event, id string, properties map[string]any) error {
	body, err := json.Marshal(map[string]any{
		"api_key":     apiKey,
		"event":       event,
		"distinct_id": id,
		"timestamp":   t.Now().UTC().Format(time.RFC3339),
		"properties":  properties,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.Endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("%s answered %s", t.Endpoint, resp.Status)
	}
	logger.Info("telemetry: sent", zap.String("event", event))
	return nil
}
