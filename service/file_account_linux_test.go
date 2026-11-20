//go:build linux

package service

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ReCasaOS/CasaOS-Common/external"
	"github.com/ReCasaOS/CasaOS/model"
	"github.com/ReCasaOS/CasaOS/pkg/fileaccess"
	"github.com/ReCasaOS/CasaOS/pkg/fileroots"
)

// an account given <base>/w to write and <base>/r to read; <base>/outside is no folder of it
func accountFolders(t *testing.T) (base string) {
	t.Helper()
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"w", "r", "outside"} {
		if err := os.MkdirAll(filepath.Join(base, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	wasAccount, wasFolders, wasDefault := fileaccess.AccountOf, fileaccess.FoldersOf, fileroots.Default
	t.Cleanup(func() { fileaccess.AccountOf, fileaccess.FoldersOf, fileroots.Default = wasAccount, wasFolders, wasDefault })
	fileroots.Default = nil
	fileaccess.AccountOf = func(string, string) (string, bool, bool) { return "user", false, true }
	folders := []external.Folder{{Path: base + "/w", Write: true}, {Path: base + "/r"}}
	fileaccess.FoldersOf = func(string, string) []external.Folder { return folders }
	t.Cleanup(func() { folders = nil })
	return base
}

func accountTask(kind, from, to string) model.FileOperate {
	return model.FileOperate{Type: kind, Item: []model.FileItem{{From: from}}, To: to, Style: "overwrite", Account: "4"}
}

// The task of an account waits in the queue on paths checked when it was asked. A folder
// swapped for a link out meanwhile (renames the account may make) leads nowhere: the task
// runs inside the account's folders.
func TestAnAccountsTaskCannotBeLedOutByASwap(t *testing.T) {
	base := accountFolders(t)
	w := base + "/w"
	writeOperateFile(t, w+"/cron.d/evil", "evil")
	// a link out, already in the account's folder (an app's data, say)
	if err := os.MkdirAll(w+"/y", 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../outside", w+"/y/etc-link"); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(w+"/x/etc-link", 0o755); err != nil {
		t.Fatal(err)
	}
	// asked on w/x/etc-link, a real folder; then the account swaps x for y
	task := accountTask("copy", w+"/cron.d", w+"/x/etc-link")
	if err := os.Rename(w+"/x", w+"/x-old"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(w+"/y", w+"/x"); err != nil {
		t.Fatal(err)
	}

	runOperation(t, task)

	if exists(base + "/outside/cron.d") {
		t.Fatal("the copy was led out of the account's folders")
	}
	if _, code := taskOutcome(t.Name()); code == "" {
		t.Error("the task did not say it failed")
	}
}

func TestAnAccountCopiesAndMovesInsideItsFolders(t *testing.T) {
	base := accountFolders(t)
	writeOperateFile(t, base+"/r/photos/a.jpg", "a")
	writeOperateFile(t, base+"/outside/secret", "s")
	if err := os.Symlink("../../outside/secret", base+"/r/photos/leak"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(base+"/w/in", 0o755); err != nil {
		t.Fatal(err)
	}

	// read in r, written in w; a link out of the folders is not copied as what it points to
	runOperation(t, accountTask("copy", base+"/r/photos", base+"/w"))
	if b, err := os.ReadFile(base + "/w/photos/a.jpg"); err != nil || string(b) != "a" {
		t.Errorf("copied a.jpg: %q %v", b, err)
	}
	if exists(base + "/w/photos/leak") {
		t.Error("a file of another folder came in through a link")
	}

	// a move inside w is a rename
	runOperation(t, accountTask("move", base+"/w/photos", base+"/w/in"))
	if !exists(base+"/w/in/photos/a.jpg") || exists(base+"/w/photos") {
		t.Error("the move inside w")
	}
}

func TestAnAccountsTaskChecksItsFoldersWhenItRuns(t *testing.T) {
	base := accountFolders(t)
	writeOperateFile(t, base+"/r/a.txt", "a")

	// r is read only: nothing moves out of it, nothing goes into it
	runOperation(t, accountTask("move", base+"/r/a.txt", base+"/w"))
	if !exists(base+"/r/a.txt") || exists(base+"/w/a.txt") {
		t.Error("a move out of a read-only folder")
	}
	writeOperateFile(t, base+"/w/b.txt", "b")
	runOperation(t, accountTask("copy", base+"/w/b.txt", base+"/r"))
	if exists(base + "/r/b.txt") {
		t.Error("a copy into a read-only folder")
	}

	// the folder taken away before the task runs: nothing is done
	fileaccess.FoldersOf = func(string, string) []external.Folder { return nil }
	runOperation(t, accountTask("copy", base+"/w/b.txt", base+"/w"))
	if _, code := taskOutcome(t.Name()); code == "" {
		t.Error("a task of an account with no folder left did not fail")
	}
}
