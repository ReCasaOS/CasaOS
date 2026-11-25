package modulecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
)

// The modules written outside ReCasaOS that the owner of the box lets the night update, by name: {"modules": ["<name>", …]}. A module of ReCasaOS is updated by the general switch of
// the automatic updates, and the file says nothing of it; a foreign module is updated at night only when its name is here, and the default is no.
const pathAutoAllow = pathState + "/auto-update.json"

// autoAllowMax bounds the file: 64 KiB is far more names than a box has modules.
const autoAllowMax = 64 << 10

type autoAllowFile struct {
	Modules []string `json:"modules"`
}

// AutoAllowed is the names in the file that are names of modules. It never fails: a file that is missing, empty, broken, too large, or not a regular file (a link, a directory) allows
// nothing, and a name that is not a module's is not allowed.
func AutoAllowed(roots Roots) map[string]bool {
	allowed := map[string]bool{}
	raw, err := readRegular(roots.Path(pathAutoAllow), autoAllowMax)
	if err != nil {
		return allowed
	}
	var f autoAllowFile
	if json.Unmarshal(raw, &f) != nil {
		return allowed
	}
	for _, name := range f.Modules {
		if nameRe.MatchString(name) {
			allowed[name] = true
		}
	}
	return allowed
}

// SetAutoAllowed puts the module called name in the file (on) or takes it out, and writes the file whole or not at all, root's, 0644, its names sorted and each once. The read, the change
// and the rename are done under a lock of a neighbour file, so two changes at once both count. A link or anything but a regular file at the place of the file is left as it is, and refused.
func SetAutoAllowed(roots Roots, name string, on bool) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("not the name of a module: %q", name)
	}
	path := roots.Path(pathAutoAllow)
	dir := filepath.Dir(path)
	if err := makeDirs(dir, 0o755); err != nil {
		return err
	}
	// a link where the directory is is not written through
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() {
		return fmt.Errorf("%s is not a directory", dir)
	}
	release, err := lockWait(path + ".lock")
	if err != nil {
		return err
	}
	defer release()
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() || err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("%s is not a regular file", path)
	}
	allowed := AutoAllowed(roots)
	if on {
		allowed[name] = true
	} else {
		delete(allowed, name)
	}
	raw, err := json.Marshal(autoAllowFile{Modules: append([]string{}, slices.Sorted(maps.Keys(allowed))...)})
	if err != nil {
		return err
	}
	// under the lock, so the fixed temporary name of writeFileAtomic is this change's alone
	if err := writeFileAtomic(path, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}
