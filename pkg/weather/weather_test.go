package weather

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

const forecast = `{"current":{"temperature_2m":24.3,"relative_humidity_2m":61,"weather_code":2,"wind_speed_10m":11.5,"is_day":1},
"daily":{"time":["2026-11-25","2026-11-26"],"weather_code":[2,61],"temperature_2m_max":[27.1,22],"temperature_2m_min":[15.2,14],"precipitation_probability_max":[10,null]}}`

const places = `{"results":[{"name":"Vinhedo","admin1":"São Paulo","country":"Brazil","latitude":-23.03,"longitude":-46.97},
{"name":"","latitude":1,"longitude":1},{"name":"Nowhere","latitude":95,"longitude":0}]}`

var vinhedo = &Place{Name: "Vinhedo", Country: "Brazil", Latitude: -23.03, Longitude: -46.97}

// open is a Service under a temporary root whose Open-Meteo is a test server.
func open(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) (*Service, *atomic.Int32) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); answer(w, r) }))
	t.Cleanup(server.Close)
	s := New(t.TempDir())
	s.ForecastURL = server.URL + "/v1/forecast"
	s.GeocodeURL = server.URL + "/v1/search"
	return s, &hits
}

func TestOffAsksNothing(t *testing.T) {
	s, hits := open(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(forecast)) })
	if st := s.Status(); st.Enabled || st.Place != nil || st.Units != "metric" || st.Forecast != nil {
		t.Fatalf("a box that never chose: %+v", st)
	}
	if _, err := s.Update(Config{Enabled: false, Place: vinhedo}); err != nil {
		t.Fatal(err)
	}
	s.Status()
	if hits.Load() != 0 {
		t.Fatalf("off, Open-Meteo was asked %d times", hits.Load())
	}
	if _, err := s.Update(Config{Enabled: true}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("on with no place: %v", err)
	}
	for _, p := range []*Place{{Name: " ", Latitude: 1}, {Name: "x", Latitude: 91}, {Name: "x", Longitude: -181}} {
		if _, err := s.Update(Config{Enabled: true, Place: p}); !errors.Is(err, ErrInvalid) {
			t.Fatalf("place %+v: %v", p, err)
		}
	}
	// a file that does not parse is off
	if err := os.WriteFile(filepath.Join(s.Root, ConfigFile), []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if st := s.Status(); st.Enabled {
		t.Fatalf("unreadable file: %+v", st)
	}
}

func TestTheForecastIsReadOnceInHalfAnHourAndKeptWhenOpenMeteoFails(t *testing.T) {
	var fail atomic.Bool
	var query atomic.Value
	s, hits := open(t, func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.Query())
		if fail.Load() {
			http.Error(w, "down", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(forecast))
	})
	now := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	s.Now = func() time.Time { return now }

	st, err := s.Update(Config{Enabled: true, Place: vinhedo, Units: "kelvin"})
	if err != nil {
		t.Fatal(err)
	}
	if st.Units != "metric" || st.Forecast == nil {
		t.Fatalf("the first read waits for the forecast: %+v", st)
	}
	f := st.Forecast
	if f.Current != (Current{24.3, 61, 11.5, 2, true}) || len(f.Days) != 2 || f.Days[1] != (Day{"2026-11-26", 61, 14, 22, 0}) || !f.Updated.Equal(now) {
		t.Fatalf("forecast = %+v", f)
	}
	q := query.Load().(url.Values)
	if q["latitude"][0] != "-23.0300" || q["timezone"][0] != "auto" || q["temperature_unit"] != nil {
		t.Fatalf("asked %v", q)
	}

	now = now.Add(29 * time.Minute)
	s.Status()
	if hits.Load() != 1 {
		t.Fatalf("asked %d times within half an hour", hits.Load())
	}

	fail.Store(true)
	now = now.Add(time.Minute)
	s.Status() // due: refreshes in the background
	s.refreshed.Wait()
	st = s.Status()
	if hits.Load() != 2 || st.Forecast == nil || st.Forecast.Current.Temperature != 24.3 || st.Error == "" {
		t.Fatalf("after a failed read: hits %d, %+v", hits.Load(), st)
	}

	// another place, or other units, is another forecast
	fail.Store(false)
	st, _ = s.Update(Config{Enabled: true, Place: vinhedo, Units: "imperial"})
	q = query.Load().(url.Values)
	if hits.Load() != 3 || st.Error != "" || q["temperature_unit"][0] != "fahrenheit" || q["wind_speed_unit"][0] != "mph" {
		t.Fatalf("imperial: hits %d, %+v, asked %v", hits.Load(), st, q)
	}
}

func TestSearch(t *testing.T) {
	var query atomic.Value
	s, hits := open(t, func(w http.ResponseWriter, r *http.Request) {
		query.Store(r.URL.Query())
		_, _ = w.Write([]byte(places))
	})
	got, err := s.Search(" Vinhedo ", "PT")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != (Place{"Vinhedo", "São Paulo", "Brazil", -23.03, -46.97}) {
		t.Fatalf("places = %+v (the nameless and the out-of-range left out)", got)
	}
	q := query.Load().(url.Values)
	if q["name"][0] != "Vinhedo" || q["language"][0] != "pt" || q["count"][0] != "5" {
		t.Fatalf("asked %v", q)
	}
	if s.Search("Paris", "x&y=1"); query.Load().(url.Values)["language"][0] != "en" {
		t.Fatal("a language that is not one was passed on")
	}
	if _, err := s.Search("a", "en"); !errors.Is(err, ErrInvalid) || hits.Load() != 2 {
		t.Fatalf("one letter: %v, hits %d", err, hits.Load())
	}
}
