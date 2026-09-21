package v1

import (
	"archive/zip"
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ReCasaOS/CasaOS/pkg/utils/common_err"
	"github.com/labstack/echo/v4"
)

func TestGetDownloadFile(t *testing.T) {
	dir := t.TempDir()
	a, b := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	for p, content := range map[string]string{a: "alpha", b: "bravo"} {
		if err := os.WriteFile(p, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	e := echo.New()
	download := func(ctx context.Context, format string, files ...string) *httptest.ResponseRecorder {
		q := url.Values{"files": {strings.Join(files, ",")}, "format": {format}}
		req := httptest.NewRequest(http.MethodGet, "/v1/file?"+q.Encode(), nil).WithContext(ctx)
		rec := httptest.NewRecorder()
		if err := GetDownloadFile(e.NewContext(req, rec)); err != nil {
			t.Fatal(err)
		}
		return rec
	}

	t.Run("a single file is sent as is", func(t *testing.T) {
		rec := download(context.Background(), "zip", a)
		// Without the return, an archive followed the file in the body.
		if got := rec.Body.String(); got != "alpha" {
			t.Fatalf("body = %q, want %q", got, "alpha")
		}
		if got, want := rec.Header().Get(echo.HeaderContentDisposition), "attachment; filename*=utf-8''a.txt"; got != want {
			t.Fatalf("Content-Disposition = %q, want %q", got, want)
		}
	})

	t.Run("several files are zipped under one folder", func(t *testing.T) {
		rec := download(context.Background(), "zip", a, b)
		base := filepath.Base(dir)
		if got, want := rec.Header().Get(echo.HeaderContentDisposition), "attachment; filename*=utf-8''"+url.PathEscape("_"+base+".zip"); got != want {
			t.Fatalf("Content-Disposition = %q, want %q", got, want)
		}
		if got := rec.Header().Get(echo.HeaderContentType); got != "application/octet-stream" {
			t.Fatalf("Content-Type = %q", got)
		}
		zr, err := zip.NewReader(bytes.NewReader(rec.Body.Bytes()), int64(rec.Body.Len()))
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]string{}
		for _, f := range zr.File {
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			content, err := io.ReadAll(rc)
			rc.Close()
			if err != nil {
				t.Fatal(err)
			}
			got[f.Name] = string(content)
		}
		want := map[string]string{base + "/a.txt": "alpha", base + "/b.txt": "bravo"}
		if len(got) != len(want) || got[base+"/a.txt"] != "alpha" || got[base+"/b.txt"] != "bravo" {
			t.Fatalf("entries = %v, want %v", got, want)
		}
	})

	t.Run("an error before the first byte is a JSON answer, not a download", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		rec := download(ctx, "zip", a, b)
		if rec.Code != common_err.SERVICE_ERROR {
			t.Fatalf("status = %d, want %d", rec.Code, common_err.SERVICE_ERROR)
		}
		if got := rec.Header().Get(echo.HeaderContentDisposition); got != "" {
			t.Fatalf("Content-Disposition = %q, want none", got)
		}
		if got := rec.Header().Get(echo.HeaderContentType); !strings.HasPrefix(got, echo.MIMEApplicationJSON) {
			t.Fatalf("Content-Type = %q, want JSON", got)
		}
	})
}
