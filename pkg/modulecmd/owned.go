package modulecmd

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"slices"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// holding says what is at a path of the box, against what the list of the files of a module says it should hold.
type holding int

const (
	// holdsNothing: nothing is there (a path that was planned and never written, or that has been taken away).
	holdsNothing holding = iota
	// holdsOurs: what is there is what the list says: a file with the bytes, a link with the target. It is the module's, and the only thing the module deletes or replaces.
	holdsOurs
	// holdsOther: anything else (other bytes, a link where there was a file, a file where there was a link, a directory). It is somebody's, and is left.
	holdsOther
)

// holds says what the path real holds, against the entry. An error is that it could not be told, which is neither of the others.
func holds(real string, e osmodule.FileEntry) (holding, error) {
	info, err := os.Lstat(real)
	switch {
	case errors.Is(err, fs.ErrNotExist) || isNotDir(err):
		return holdsNothing, nil
	case err != nil:
		return holdsOther, err
	}
	if e.Link {
		if info.Mode()&fs.ModeSymlink == 0 {
			return holdsOther, nil
		}
		target, err := os.Readlink(real)
		if err != nil {
			return holdsOther, err
		}
		return ours(slices.Contains(e.Want, target)), nil
	}
	if !info.Mode().IsRegular() {
		return holdsOther, nil
	}
	sum, err := sumOf(real)
	if err != nil {
		return holdsOther, err
	}
	return ours(slices.Contains(e.Want, sum)), nil
}

func ours(is bool) holding {
	if is {
		return holdsOurs
	}
	return holdsOther
}

// sumOf is the sha256 of a file, in lower case hex.
func sumOf(real string) (string, error) {
	f, err := os.Open(real)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
