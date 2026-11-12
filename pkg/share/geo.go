package share

import (
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ReCasaOS/CasaOS/common"
)

const (
	geoBaseURL  = "https://download.db-ip.com/free"
	geoMaxAge   = 32 * 24 * time.Hour
	geoCheck    = 6 * time.Hour
	geoRetry    = time.Hour
	geoTimeout  = 2 * time.Minute // per file
	geoMaxBytes = 1 << 30         // a gunzipped file past this is not a DB-IP Lite
)

// geoFiles maps the DB-IP edition to the file the Gateway reads.
var geoFiles = []struct{ edition, name string }{
	{"country", "dbip-country-lite.mmdb"},
	{"asn", "dbip-asn-lite.mmdb"},
}

// GeoStatus is what the files on disk are, and why the last update failed ("" when it did not).
type GeoStatus struct {
	Country, ASN bool
	UpdatedAt    time.Time // the older of the two files; zero when none
	Error        string
}

// GeoFetcher keeps DB-IP Lite's country and network files current. The Gateway memory-maps them, so a file is
// only ever replaced by renaming a finished one over it.
type GeoFetcher struct {
	Dir     string
	BaseURL string
	Client  *http.Client
	Now     func() time.Time

	mu  sync.Mutex
	err string
}

func (f *GeoFetcher) now() time.Time {
	if f.Now != nil {
		return f.Now()
	}
	return time.Now()
}

// Status reads the files' state; a missing file is false.
func (f *GeoFetcher) Status() GeoStatus {
	var st GeoStatus
	ok := [2]*bool{&st.Country, &st.ASN}
	for i, g := range geoFiles {
		fi, err := os.Stat(filepath.Join(f.Dir, g.name))
		if err != nil {
			continue
		}
		*ok[i] = true
		if t := fi.ModTime(); st.UpdatedAt.IsZero() || t.Before(st.UpdatedAt) {
			st.UpdatedAt = t
		}
	}
	f.mu.Lock()
	st.Error = f.err
	f.mu.Unlock()
	return st
}

// Stale is whether a file is missing or older than 32 days.
func (f *GeoFetcher) Stale() bool {
	for _, g := range geoFiles {
		fi, err := os.Stat(filepath.Join(f.Dir, g.name))
		if err != nil || f.now().Sub(fi.ModTime()) > geoMaxAge {
			return true
		}
	}
	return false
}

// Update downloads both files. A file that fails keeps its old version; the first error is returned and kept.
func (f *GeoFetcher) Update(ctx context.Context) error {
	var first error
	if err := os.MkdirAll(f.Dir, 0o755); err != nil {
		first = err
	} else {
		for _, g := range geoFiles {
			if err := f.fetch(ctx, g.edition, g.name); err != nil && first == nil {
				first = fmt.Errorf("%s: %w", g.name, err)
			}
		}
	}
	f.mu.Lock()
	f.err = ""
	if first != nil {
		f.err = first.Error()
	}
	f.mu.Unlock()
	return first
}

var errGeoNotFound = errors.New("not published")

// fetch tries this month's file, then last month's when this month's is not out yet.
func (f *GeoFetcher) fetch(ctx context.Context, edition, name string) error {
	now := f.now().UTC()
	prev := time.Date(now.Year(), now.Month()-1, 1, 0, 0, 0, 0, time.UTC)
	err := f.download(ctx, edition, name, now)
	if errors.Is(err, errGeoNotFound) {
		err = f.download(ctx, edition, name, prev)
	}
	return err
}

func (f *GeoFetcher) download(ctx context.Context, edition, name string, month time.Time) (err error) {
	ctx, cancel := context.WithTimeout(ctx, geoTimeout)
	defer cancel()
	base := f.BaseURL
	if base == "" {
		base = geoBaseURL
	}
	url := fmt.Sprintf("%s/dbip-%s-lite-%s.mmdb.gz", base, edition, month.Format("2006-01"))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "ReCasaOS/"+common.VERSION)
	c := f.Client
	if c == nil {
		c = &http.Client{}
	}
	resp, err := c.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return errGeoNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download answered %d", resp.StatusCode)
	}
	zr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	final := filepath.Join(f.Dir, name)
	tmp := final + ".tmp"
	out, err := os.OpenFile(tmp, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			out.Close()
			os.Remove(tmp)
		}
	}()
	n, err := io.Copy(out, io.LimitReader(zr, geoMaxBytes+1))
	if err != nil {
		return err
	}
	if n > geoMaxBytes {
		return errors.New("file too large")
	}
	if err = mmdbCheck(out, n); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, final)
}

// mmdbCheck looks for the MMDB metadata marker in the last 128 KiB.
func mmdbCheck(r io.ReaderAt, size int64) error {
	n := min(size, 128<<10)
	buf := make([]byte, n)
	if _, err := r.ReadAt(buf, size-n); err != nil && err != io.EOF {
		return err
	}
	if !bytes.Contains(buf, []byte("\xab\xcd\xefMaxMind.com")) {
		return errors.New("not an MMDB file")
	}
	return nil
}

// geoStep updates when protection is on and a file is stale, and says how long to wait before the next look.
func (s *Service) geoStep(ctx context.Context) time.Duration {
	s.mu.Lock()
	g, err := s.guardLocked()
	on := err == nil && g.Protection
	s.mu.Unlock()
	if !on || !s.Geo.Stale() {
		return geoCheck
	}
	if err := s.Geo.Update(ctx); err != nil {
		if ctx.Err() == nil {
			s.log("share: cannot update the DB-IP Lite files", err)
		}
		return geoRetry
	}
	return geoCheck
}

func (s *Service) geoLoop(ctx context.Context) {
	for {
		t := time.NewTimer(s.geoStep(ctx))
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
	}
}
