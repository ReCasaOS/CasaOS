package v1

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/ReCasaOS/CasaOS-Common/pkg/stats"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/hoststats"
	"github.com/labstack/echo/v4"
)

// statsFail answers an error of the stats routes: the code alone, never its cause.
func statsFail(ctx echo.Context, status int, code string) error {
	return ctx.JSON(status, model.Result{Success: status, Message: code})
}

func statsOK(ctx echo.Context, data any) error {
	return ctx.JSON(http.StatusOK, model.Result{Success: http.StatusOK, Message: "ok", Data: data})
}

// statsService is the running service, or the 503 already written. Stats
// answers are live and never cached.
func statsService(ctx echo.Context) (*hoststats.Service, error) {
	ctx.Response().Header().Set("Cache-Control", "no-store")
	if s := hoststats.Default; s != nil {
		return s, nil
	}
	return nil, statsFail(ctx, http.StatusServiceUnavailable, "stats_unavailable")
}

// latest is the newest reading of chart/name in the live window, nil when there is none.
func latest(res stats.Result, chart, name string) *float64 {
	for _, s := range res.Series {
		if s.Chart != chart || s.Name != name {
			continue
		}
		for i := len(s.Points) - 1; i >= 0; i-- {
			if v := s.Points[i].Avg; v != nil {
				return v
			}
		}
	}
	return nil
}

func intOf(v *float64) *int64 {
	if v == nil {
		return nil
	}
	n := int64(*v)
	return &n
}

type statsItem struct {
	ID          string   `json:"id"`
	Name        string   `json:"name"`
	Kind        string   `json:"kind"`
	Group       string   `json:"group"`
	State       string   `json:"state"`
	StartedAt   *string  `json:"started_at"`
	Detail      *string  `json:"detail"`
	CPUPercent  *float64 `json:"cpu_percent"`
	MemoryUsed  *int64   `json:"memory_used"`
	MemoryLimit *int64   `json:"memory_limit"`
	Alerts      int      `json:"alerts"`
}

// GetStats lists what is measured: the machine.
//
// @Summary statistics: the measured items
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Success 200 {object} model.Result
// @Router /sys/stats [get]
func GetStats(ctx echo.Context) error {
	s, err := statsService(ctx)
	if s == nil {
		return err
	}
	item := statsItem{
		ID: hoststats.Item, Name: s.Name(), Kind: "host", Group: "Machine", State: "running",
		Alerts: len(s.Eval.Active(hoststats.Item)),
	}
	if b := s.BootTime(); !b.IsZero() {
		t := b.UTC().Format(time.RFC3339)
		item.StartedAt = &t
	}
	// no data yet (before the first minute) is a null, not an error
	if res, err := s.Store.Query(hoststats.Item, stats.RangeLive); err == nil {
		item.CPUPercent = latest(res, "cpu", "total")
		item.MemoryUsed = intOf(latest(res, "memory", "rss"))
		item.MemoryLimit = intOf(latest(res, "memory", "limit"))
	}
	return statsOK(ctx, map[string]any{"items": []statsItem{item}, "notes": []string{}})
}

type statsScale struct {
	CPU           string   `json:"cpu"`
	CPUAllocation *float64 `json:"cpu_allocation"`
	MemoryLimit   *int64   `json:"memory_limit"`
}

// statsResult drops Result's MarshalJSON: embedded as is, it would be promoted
// and hide every field beside it.
type statsResult stats.Result

type statsSeriesBody struct {
	ID string `json:"id"`
	statsResult
	Scale statsScale `json:"scale"`
	Notes []string   `json:"notes"`
}

// GetStatsSeries answers the history of the machine over ?range= (live when absent).
//
// @Summary statistics: the series of one item
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param id path string true "item id"
// @Param range query string false "live, 15m, 1h, 24h or 7d"
// @Success 200 {object} model.Result
// @Router /sys/stats/{id} [get]
func GetStatsSeries(ctx echo.Context) error {
	s, err := statsService(ctx)
	if s == nil {
		return err
	}
	id := ctx.Param("id")
	if id != hoststats.Item {
		return statsFail(ctx, http.StatusNotFound, "unknown_item")
	}
	r := stats.RangeLive
	// read here: `?range=` is refused, not taken for absent
	if values, ok := ctx.QueryParams()["range"]; ok {
		var valid bool
		if r, valid = stats.ParseRange(values[0]); !valid {
			return statsFail(ctx, http.StatusBadRequest, "bad_range")
		}
	}
	res, err := s.Store.Query(id, r)
	if err != nil {
		// ErrUnknownItem here is the host before its first sample
		return statsFail(ctx, http.StatusServiceUnavailable, "stats_unavailable")
	}
	live := res
	if r != stats.RangeLive {
		if live, err = s.Store.Query(id, stats.RangeLive); err != nil {
			live = stats.Result{}
		}
	}
	return statsOK(ctx, statsSeriesBody{
		ID:          id,
		statsResult: statsResult(res),
		Scale: statsScale{
			CPU: "percent_of_one_core", CPUAllocation: latest(live, "cpu", "vcpus"), MemoryLimit: intOf(latest(live, "memory", "limit")),
		},
		Notes: []string{},
	})
}

type statsAlert struct {
	Key   string    `json:"key"`
	Since time.Time `json:"since"`
	Value float64   `json:"value"`
}

type statsAlertsBody struct {
	Rules  []stats.Rule `json:"rules"`
	Active []statsAlert `json:"active"`
}

func statsAlertsOf(ctx echo.Context, s *hoststats.Service) error {
	active := []statsAlert{}
	for _, e := range s.Eval.Active(hoststats.Item) {
		active = append(active, statsAlert{Key: e.Key, Since: e.Since, Value: e.Value})
	}
	return statsOK(ctx, statsAlertsBody{Rules: s.Eval.Rules(hoststats.Item), Active: active})
}

// GetStatsAlerts answers the alert rules of the machine and its firing alerts.
//
// @Summary statistics: alert rules and firing alerts of one item
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param id path string true "item id"
// @Success 200 {object} model.Result
// @Router /sys/stats/{id}/alerts [get]
func GetStatsAlerts(ctx echo.Context) error {
	s, err := statsService(ctx)
	if s == nil {
		return err
	}
	if ctx.Param("id") != hoststats.Item {
		return statsFail(ctx, http.StatusNotFound, "unknown_item")
	}
	return statsAlertsOf(ctx, s)
}

// maxRulesBody bounds a PUT of rules; two rules take about 150 bytes.
const maxRulesBody = 64 << 10

func badRules(ctx echo.Context, field string) error {
	return statsFail(ctx, http.StatusBadRequest, "bad_rules: "+field)
}

// PutStatsAlerts replaces the rules listed, by key; the others keep theirs. A
// malformed body, whatever its fault, is bad_rules naming the field.
//
// @Summary statistics: set alert rules of one item
// @Accept application/json
// @Produce application/json
// @Tags sys
// @Security ApiKeyAuth
// @Param id path string true "item id"
// @Success 200 {object} model.Result
// @Router /sys/stats/{id}/alerts [put]
func PutStatsAlerts(ctx echo.Context) error {
	s, err := statsService(ctx)
	if s == nil {
		return err
	}
	if ctx.Param("id") != hoststats.Item {
		return statsFail(ctx, http.StatusNotFound, "unknown_item")
	}
	raw, err := io.ReadAll(http.MaxBytesReader(ctx.Response(), ctx.Request().Body, maxRulesBody))
	var body struct {
		Rules []map[string]json.RawMessage `json:"rules"`
	}
	if err != nil || json.Unmarshal(raw, &body) != nil || body.Rules == nil {
		return badRules(ctx, "rules") // too large, not JSON, not an object, no list
	}
	rules := make([]stats.Rule, len(body.Rules))
	for i, m := range body.Rules {
		// each field present and of its type; the values are the Evaluator's to judge
		for _, f := range []struct {
			name string
			to   any
		}{{"key", &rules[i].Key}, {"enabled", &rules[i].Enabled}, {"threshold", &rules[i].Threshold}, {"minutes", &rules[i].Minutes}} {
			if v := m[f.name]; string(v) == "null" || json.Unmarshal(v, f.to) != nil {
				return badRules(ctx, fmt.Sprintf("rules[%d].%s", i, f.name))
			}
		}
	}
	var re *stats.RuleError
	switch err := s.Eval.SetRules(hoststats.Item, rules); {
	case errors.As(err, &re):
		return badRules(ctx, fmt.Sprintf("rules[%d].%s", re.Index, re.Field))
	case err != nil:
		return statsFail(ctx, http.StatusServiceUnavailable, "stats_unavailable")
	}
	return statsAlertsOf(ctx, s)
}
