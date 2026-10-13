package osverify

import (
	"context"
	"regexp"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// bkPattern is a backup folder of a hop: where the blocks write, and only there.
var bkPattern = regexp.MustCompile(`^/var/backups/casaos-os-upgrade-[A-Za-z0-9._-]{1,120}$`)

// ReadBK reads the files of a backup folder that the judge needs, through the Probe. A file that is not there, or cannot be
// read, is not in what it gives back; a folder that is not one of the hop's is none.
func ReadBK(ctx context.Context, p osreadiness.Probe, dir string) BKFiles {
	bk := BKFiles{}
	if !bkPattern.MatchString(dir) || strings.Contains(dir, "..") {
		return bk
	}
	for _, name := range BKNames {
		if data, err := p.ReadFile(ctx, dir+"/"+name); err == nil {
			bk[name] = string(data)
		}
	}
	return bk
}
