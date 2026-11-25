package modulecmd

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
	"github.com/go-ini/ini"
)

// The online catalogue: the core fetches it (RefreshOnline) and the commands read what it kept (loadCatalogue). What is fetched decides what is later installed as root, so a fetch is
// kept only when it is a whole catalogue that passes, of a schema not below the one in place and whose serial is not below its; anything else leaves the file in place as it was.

// A RefreshResult is what became of a fetch: Kept when the fetched file is now in place, the serial it carries when it was read, and otherwise why not: "fetch" (the address did
// not give a whole answer within its bounds), "invalid" (not a catalogue that passes), "schema" (a schema below the one in place, which would drop what only the later one says:
// the versions withdrawn, the modules written outside ReCasaOS), "older" (a serial below the one in place) or "write" (it could not be put in place).
type RefreshResult struct {
	Kept   bool
	Serial int64
	Reason string
}

// RefreshOnline fetches the catalogue at url with get, checks it, compares its serial with the file in place, and replaces that file when it is not older. The read of the file in
// place, the compare and the rename are done under a lock of a neighbour file, so two fetches at once leave the higher serial. It never reads more than osmodule.MaxCatalogueBytes,
// whatever get is.
func RefreshOnline(ctx context.Context, roots Roots, get Downloader, url string) RefreshResult {
	var buf cappedBuffer
	if _, err := get.Get(ctx, url, &buf); err != nil {
		return RefreshResult{Reason: "fetch"}
	}
	fetched := checkCatalogue(buf.Bytes(), osmodule.ParseOnlineCatalogue)
	if !fetched.ok {
		return RefreshResult{Reason: "invalid"}
	}
	serial := fetched.cat.Serial
	failed := RefreshResult{Serial: serial, Reason: "write"}

	path := roots.OnlineCatalogue()
	dir := filepath.Dir(path)
	if err := makeDirs(dir, 0o755); err != nil {
		return failed
	}
	// a link where the directory is is not written through
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return failed
	}
	release, err := lockWait(path + ".lock")
	if err != nil {
		return failed
	}
	defer release()
	// a file in place that does not pass counts for nothing
	if cur := readCatalogueFile(path, osmodule.ParseOnlineCatalogue); cur.ok && fetched.cat.Schema < cur.cat.Schema {
		return RefreshResult{Serial: serial, Reason: "schema"}
	} else if cur.ok && serial < cur.cat.Serial {
		return RefreshResult{Serial: serial, Reason: "older"}
	}
	// a link or a directory where the file goes stays as it is
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, fs.ErrNotExist) {
		return failed
	}
	// under the lock, so the fixed temporary name of writeFileAtomic is this fetch's alone
	if writeFileAtomic(path, buf.Bytes(), 0o644) != nil {
		return failed
	}
	syncDir(dir)
	return RefreshResult{Kept: true, Serial: serial}
}

// cappedBuffer takes at most osmodule.MaxCatalogueBytes; a write past that is ErrTooLarge.
type cappedBuffer struct{ bytes.Buffer }

func (b *cappedBuffer) Write(p []byte) (int, error) {
	if b.Len()+len(p) > osmodule.MaxCatalogueBytes {
		return 0, ErrTooLarge
	}
	return b.Buffer.Write(p)
}

// onlineCatalogueOff reads [modules] OnlineCatalogue in the core's configuration, as the core reads it: false turns the online catalogue off, and anything else (no file, no
// section, no key, a value that is not a boolean) leaves it on, the core's default.
func onlineCatalogueOff(roots Roots) bool {
	raw, err := readRegular(roots.Path(pathCoreConfig), 1<<20)
	if err != nil {
		return false
	}
	f, err := ini.Load(raw)
	if err != nil {
		return false
	}
	on, err := f.Section("modules").Key("OnlineCatalogue").Bool()
	return err == nil && !on
}
