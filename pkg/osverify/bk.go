package osverify

import (
	"context"
	"errors"
	"io/fs"
	"regexp"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// bkPattern is a backup folder of a hop: where the blocks write, and only there.
var bkPattern = regexp.MustCompile(`^/var/backups/casaos-os-upgrade-[A-Za-z0-9._-]{1,120}$`)

// UnreadablePrefix begins the name, in Facts.Unknown of the facts of after, of a file of the backup folder that is there and
// could not be read: it is not a file that is not there, and the judge says that what it was to be compared with was not.
const UnreadablePrefix = "bk:"

// ReadBK reads the files of a backup folder that the judge needs, through the Probe. A file that is not there, or cannot be
// read, is not in what it gives back; a folder that is not one of the hop's is none.
func ReadBK(ctx context.Context, p osreadiness.Probe, dir string) BKFiles {
	bk, _ := ReadBKUnreadable(ctx, p, dir)
	return bk
}

// ReadBKUnreadable is ReadBK and the names of the files that are there and could not be read (not the ones that are not there:
// a block that did not run wrote nothing, and that is no fault of the machine's).
func ReadBKUnreadable(ctx context.Context, p osreadiness.Probe, dir string) (BKFiles, []string) {
	bk := BKFiles{}
	var unreadable []string
	if !bkPattern.MatchString(dir) || strings.Contains(dir, "..") {
		return bk, nil
	}
	for _, name := range BKNames {
		data, err := p.ReadFile(ctx, dir+"/"+name)
		switch {
		case err == nil:
			bk[name] = string(data)
		case !errors.Is(err, fs.ErrNotExist):
			unreadable = append(unreadable, name)
		}
	}
	return bk, unreadable
}
