// Package weather keeps the place the owner chose for the dashboard's weather
// (CasaOS#188) and its forecast, read from Open-Meteo (no key). It is off until the
// owner turns it on. The box, not the browser, asks Open-Meteo: the place's
// coordinates leave the box only toward api.open-meteo.com, and a place name typed
// in the search only toward geocoding-api.open-meteo.com. Reading the forecast
// waits on the network only when nothing is cached yet; otherwise it answers what
// is cached and, when that is older than refreshEvery, refreshes in the background.
package weather

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/utils/file"
)

// ConfigFile holds the switch, the place and the units.
const ConfigFile = "/var/lib/casaos/weather.json"

const (
	refreshEvery = 30 * time.Minute
	days         = 5
	maxBody      = 256 << 10
	maxName      = 100
)

// ErrInvalid is a PUT body or a search that is not one. Nothing is written.
var ErrInvalid = errors.New("invalid weather settings")

// Place is where the forecast is for.
type Place struct {
	Name      string  `json:"name"`
	Region    string  `json:"region,omitempty"`
	Country   string  `json:"country,omitempty"`
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
}

// Config is weather.json. Units is "metric" (°C, km/h) or "imperial" (°F, mph).
type Config struct {
	Enabled bool   `json:"enabled"`
	Place   *Place `json:"place"`
	Units   string `json:"units"`
}

// Current is the weather now. Code is the WMO weather code Open-Meteo answers.
type Current struct {
	Temperature float64 `json:"temperature"`
	Humidity    float64 `json:"humidity"`
	Wind        float64 `json:"wind"`
	Code        int     `json:"code"`
	Day         bool    `json:"day"`
}

// Day is one day of the forecast, its date as the place's own (YYYY-MM-DD).
type Day struct {
	Date string  `json:"date"`
	Code int     `json:"code"`
	Min  float64 `json:"min"`
	Max  float64 `json:"max"`
	Rain float64 `json:"rain"` // the day's highest chance of rain, in %
}

// Forecast is what Open-Meteo answered, and when.
type Forecast struct {
	Current Current   `json:"current"`
	Days    []Day     `json:"days"`
	Updated time.Time `json:"updated"`
}

// Status is what GET and PUT /v1/sys/weather answer.
type Status struct {
	Config
	Forecast *Forecast `json:"forecast"`
	// why the last read failed; the forecast shown, if any, is the one before
	Error string `json:"error,omitempty"`
}

type cached struct {
	key      string // the place and units the forecast is for
	forecast *Forecast
	at       time.Time // the last try, good or not
	err      string
}

// Service is the weather of one box.
type Service struct {
	Root        string
	Now         func() time.Time
	Client      *http.Client
	ForecastURL string
	GeocodeURL  string

	mu         sync.Mutex
	cache      cached
	refreshing bool
	refreshed  sync.WaitGroup // the refresh in flight, which tests wait for
}

// Default is the box's own.
var Default = New("/")

// New keeps weather.json under root.
func New(root string) *Service {
	return &Service{
		Root:        root,
		Now:         time.Now,
		Client:      &http.Client{Timeout: 10 * time.Second},
		ForecastURL: "https://api.open-meteo.com/v1/forecast",
		GeocodeURL:  "https://geocoding-api.open-meteo.com/v1/search",
	}
}

func (s *Service) path() string { return filepath.Join(s.Root, ConfigFile) }

// load reads weather.json: no file, or one that cannot be read, is off.
func (s *Service) load() Config {
	var c Config
	data, err := os.ReadFile(s.path())
	if err != nil || json.Unmarshal(data, &c) != nil || validPlace(c.Place) != nil {
		return Config{Units: "metric"}
	}
	if c.Units != "imperial" {
		c.Units = "metric"
	}
	return c
}

func key(c Config) string {
	return fmt.Sprintf("%.4f,%.4f,%s", c.Place.Latitude, c.Place.Longitude, c.Units)
}

// Status answers the settings and the forecast. On with a place and nothing cached
// for it, it waits for Open-Meteo; with a forecast older than refreshEvery, it
// refreshes in the background. Off, it asks nothing.
func (s *Service) Status() Status {
	c := s.load()
	st := Status{Config: c}
	if !c.Enabled || c.Place == nil {
		return st
	}
	k := key(c)

	s.mu.Lock()
	if s.cache.key != k {
		s.cache = cached{key: k}
	}
	due := s.cache.at.IsZero() || s.Now().Sub(s.cache.at) >= refreshEvery
	first := s.cache.forecast == nil && s.cache.at.IsZero()
	start := due && !s.refreshing
	if start {
		s.refreshing = true
		s.refreshed.Add(1)
		go s.refresh(c, k)
	}
	s.mu.Unlock()
	if first {
		s.refreshed.Wait()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cache.key == k {
		st.Forecast, st.Error = s.cache.forecast, s.cache.err
	}
	return st
}

// Update replaces the settings. On needs a place; units other than "imperial" are
// "metric".
func (s *Service) Update(c Config) (Status, error) {
	if err := validPlace(c.Place); err != nil {
		return Status{}, err
	}
	if c.Enabled && c.Place == nil {
		return Status{}, fmt.Errorf("%w: choose a place first", ErrInvalid)
	}
	if c.Units != "imperial" {
		c.Units = "metric"
	}
	data, err := json.Marshal(c)
	if err != nil {
		return Status{}, err
	}
	if err := file.WriteFileAtomic(s.path(), data); err != nil {
		return Status{}, err
	}
	return s.Status(), nil
}

func validPlace(p *Place) error {
	if p == nil {
		return nil
	}
	if p.Name = strings.TrimSpace(p.Name); p.Name == "" || len(p.Name) > maxName || len(p.Region) > maxName || len(p.Country) > maxName {
		return fmt.Errorf("%w: a place needs a name", ErrInvalid)
	}
	if math.IsNaN(p.Latitude) || math.IsNaN(p.Longitude) || math.Abs(p.Latitude) > 90 || math.Abs(p.Longitude) > 180 {
		return fmt.Errorf("%w: the place's coordinates are out of range", ErrInvalid)
	}
	return nil
}

func (s *Service) refresh(c Config, k string) {
	defer s.refreshed.Done()
	f, err := s.fetch(c)

	s.mu.Lock()
	defer s.mu.Unlock()
	s.refreshing = false
	if s.cache.key != k {
		return // the place changed meanwhile: the next read asks again
	}
	s.cache.at = s.Now()
	s.cache.err = ""
	if err != nil {
		s.cache.err = reason(err)
		return
	}
	f.Updated = s.cache.at
	s.cache.forecast = f
}

func (s *Service) fetch(c Config) (*Forecast, error) {
	q := url.Values{}
	q.Set("latitude", strconv.FormatFloat(c.Place.Latitude, 'f', 4, 64))
	q.Set("longitude", strconv.FormatFloat(c.Place.Longitude, 'f', 4, 64))
	q.Set("current", "temperature_2m,relative_humidity_2m,weather_code,wind_speed_10m,is_day")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max")
	q.Set("timezone", "auto")
	q.Set("forecast_days", strconv.Itoa(days))
	if c.Units == "imperial" {
		q.Set("temperature_unit", "fahrenheit")
		q.Set("wind_speed_unit", "mph")
	}
	var answer struct {
		Current struct {
			Temperature float64 `json:"temperature_2m"`
			Humidity    float64 `json:"relative_humidity_2m"`
			Code        int     `json:"weather_code"`
			Wind        float64 `json:"wind_speed_10m"`
			IsDay       int     `json:"is_day"`
		} `json:"current"`
		Daily struct {
			Time []string   `json:"time"`
			Code []int      `json:"weather_code"`
			Max  []*float64 `json:"temperature_2m_max"`
			Min  []*float64 `json:"temperature_2m_min"`
			Rain []*float64 `json:"precipitation_probability_max"`
		} `json:"daily"`
	}
	if err := s.get(s.ForecastURL, q, &answer); err != nil {
		return nil, err
	}
	d := answer.Daily
	if len(d.Code) != len(d.Time) || len(d.Max) != len(d.Time) || len(d.Min) != len(d.Time) || len(d.Rain) != len(d.Time) {
		return nil, errors.New("the forecast's days do not line up")
	}
	f := &Forecast{
		Current: Current{answer.Current.Temperature, answer.Current.Humidity, answer.Current.Wind, answer.Current.Code, answer.Current.IsDay == 1},
		Days:    []Day{},
	}
	for i, date := range d.Time {
		f.Days = append(f.Days, Day{Date: date, Code: d.Code[i], Min: value(d.Min[i]), Max: value(d.Max[i]), Rain: value(d.Rain[i])})
	}
	return f, nil
}

// value is a number Open-Meteo may leave null (a chance of rain past its models).
func value(v *float64) float64 {
	if v == nil {
		return 0
	}
	return *v
}

// Search finds up to five places by name, their names in language (an ISO code,
// "en" when not one).
func (s *Service) Search(name, language string) ([]Place, error) {
	name = strings.TrimSpace(name)
	if len(name) < 2 || len(name) > maxName {
		return nil, fmt.Errorf("%w: type two letters at least", ErrInvalid)
	}
	language = strings.ToLower(language)
	if len(language) < 2 || len(language) > 3 || strings.Trim(language, "abcdefghijklmnopqrstuvwxyz") != "" {
		language = "en"
	}
	q := url.Values{}
	q.Set("name", name)
	q.Set("count", "5")
	q.Set("language", language)
	q.Set("format", "json")
	var answer struct {
		Results []struct {
			Name      string  `json:"name"`
			Admin1    string  `json:"admin1"`
			Country   string  `json:"country"`
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
		} `json:"results"`
	}
	if err := s.get(s.GeocodeURL, q, &answer); err != nil {
		return nil, err
	}
	places := []Place{}
	for _, r := range answer.Results {
		p := Place{Name: r.Name, Region: r.Admin1, Country: r.Country, Latitude: r.Latitude, Longitude: r.Longitude}
		if validPlace(&p) == nil {
			places = append(places, p)
		}
	}
	return places, nil
}

func (s *Service) get(base string, q url.Values, into any) error {
	resp, err := s.Client.Get(base + "?" + q.Encode())
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Open-Meteo answered %s", resp.Status)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(into)
}

// reason is what the dashboard shows of a failed read, the URL left out.
func reason(err error) string {
	var failed *url.Error
	if errors.As(err, &failed) {
		err = failed.Err
	}
	text := err.Error()
	if len(text) > 200 {
		text = text[:200]
	}
	return text
}
