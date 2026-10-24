package service

import (
	"bytes"
	"errors"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
	"time"
)

// uploadChunk is a chunk as the route hands it over.
func uploadChunk(t *testing.T, content string) *multipart.FileHeader {
	t.Helper()
	var body bytes.Buffer
	w := multipart.NewWriter(&body)
	part, err := w.CreateFormFile("file", "blob")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(content)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	form, err := multipart.NewReader(&body, w.Boundary()).ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { form.RemoveAll() })
	return form.File["file"][0]
}

func openFileCount(t *testing.T) int {
	t.Helper()
	entries, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		t.Fatal(err)
	}
	return len(entries)
}

func TestAnUploadClosesTheFileAfterEveryChunk(t *testing.T) {
	defer debug.SetGCPercent(debug.SetGCPercent(-1)) // a forgotten *os.File is closed by the GC: keep it from hiding one
	dir := t.TempDir()
	uploads := NewFileUploadService()
	before := openFileCount(t)

	for chunk, content := range []string{"aaa", "bbb"} {
		if err := uploads.UploadFile(nil, dir, int64(chunk+1), 3, 3, 3, 9, "a-9", "a.txt", "a.txt", uploadChunk(t, content)); err != nil {
			t.Fatal(err)
		}
	}

	if after := openFileCount(t); after != before {
		t.Errorf("%d files open after two chunks of three, want %d", after, before)
	}
}

// The .tmp of an upload that did not end is still there when the same file is sent again, shorter.
func TestAnUploadDoesNotKeepTheTailOfAnOlderTmp(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.txt.tmp"), []byte("an older and longer upload"), 0o644); err != nil {
		t.Fatal(err)
	}
	uploads := NewFileUploadService()

	if err := uploads.UploadFile(nil, dir, 1, 3, 3, 2, 5, "a-5", "a.txt", "a.txt", uploadChunk(t, "new")); err != nil {
		t.Fatal(err)
	}
	if err := uploads.UploadFile(nil, dir, 2, 3, 2, 2, 5, "a-5", "a.txt", "a.txt", uploadChunk(t, "er")); err != nil {
		t.Fatal(err)
	}

	got, err := os.ReadFile(filepath.Join(dir, "a.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "newer" {
		t.Errorf("the file holds %q, want %q", got, "newer")
	}
}

// A last chunk whose file cannot take its name must not leave the uploads locked, or unlock them twice.
func TestAnUploadThatCannotBeRenamedLeavesTheOthersWorking(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "a.txt", "taken"), 0o755); err != nil {
		t.Fatal(err)
	}
	uploads := NewFileUploadService()

	uploads.UploadFile(nil, dir, 1, 3, 3, 1, 3, "a-3", "a.txt", "a.txt", uploadChunk(t, "abc"))

	if err := uploads.UploadFile(nil, dir, 1, 3, 3, 1, 3, "b-3", "b.txt", "b.txt", uploadChunk(t, "abc")); err != nil {
		t.Fatal(err)
	}
	if got, err := os.ReadFile(filepath.Join(dir, "b.txt")); err != nil || string(got) != "abc" {
		t.Errorf("b.txt holds %q (%v), want abc", got, err)
	}
}

// A chunk outside 1..totalChunks, or a chunk count unlike the first chunk's, is refused and leaves the other uploads working.
func TestAChunkOutsideItsUploadIsRefused(t *testing.T) {
	dir := t.TempDir()
	uploads := NewFileUploadService()

	if err := uploads.UploadFile(nil, dir, 1, 3, 3, 2, 6, "a-6", "a.txt", "a.txt", uploadChunk(t, "abc")); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct{ number, size, total, totalSize int64 }{
		{3, 3, 2, 6}, {0, 3, 2, 6}, {-1, 3, 2, 6}, {1, 3, 0, 6}, {1, 0, 2, 6}, {1, 3, 2, -1},
		{1, 1, 1 << 50, 1 << 60}, // more than the table may hold
		{1, 3, 4, 6},             // more chunks than the size has
	} {
		if err := uploads.UploadFile(nil, dir, c.number, c.size, 3, c.total, c.totalSize, "b-6", "b.txt", "b.txt", uploadChunk(t, "xyz")); !errors.Is(err, ErrInvalidChunk) {
			t.Errorf("chunk %d of %d (size %d, total %d): %v, want ErrInvalidChunk", c.number, c.total, c.size, c.totalSize, err)
		}
	}
	if err := uploads.UploadFile(nil, dir, 3, 3, 3, 3, 6, "a-6", "a.txt", "a.txt", uploadChunk(t, "xyz")); !errors.Is(err, ErrInvalidChunk) {
		t.Errorf("a chunk count unlike the first chunk's: %v, want ErrInvalidChunk", err)
	}
	if err := uploads.UploadFile(nil, dir, 1, 1<<20, 0, 1, 0, "e-0", "empty.txt", "empty.txt", uploadChunk(t, "")); err != nil {
		t.Errorf("an empty file, as the dashboard sends it: %v", err)
	}
	if err := uploads.TestChunk(nil, "a-6", 5); !errors.Is(err, ErrInvalidChunk) {
		t.Errorf("testing chunk 5 of 2: %v, want ErrInvalidChunk", err)
	}

	done := make(chan error, 1)
	go func() {
		done <- uploads.UploadFile(nil, dir, 2, 3, 3, 2, 6, "a-6", "a.txt", "a.txt", uploadChunk(t, "def"))
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the uploads stayed locked after a refused chunk")
	}
	if got, err := os.ReadFile(filepath.Join(dir, "a.txt")); err != nil || string(got) != "abcdef" {
		t.Errorf("a.txt holds %q (%v), want abcdef", got, err)
	}
}
