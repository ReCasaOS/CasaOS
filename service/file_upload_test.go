package service

import (
	"bytes"
	"mime/multipart"
	"os"
	"path/filepath"
	"runtime/debug"
	"testing"
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
