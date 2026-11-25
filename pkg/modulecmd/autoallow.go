package modulecmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

// The modules written outside ReCasaOS that the owner of the box lets the night update, each with the repository it came from when the owner said yes: {"modules": {"<name>":
// "<owner>/<repo>", …}}. A module of ReCasaOS is updated by the general switch of the automatic updates, and the file says nothing of it; a foreign module is updated at night only
// when its name is here with the repository the catalogue lists it from now, and the default is no. A yes is for one module from one repository: a module of the same name from
// another repository, or one installed again after a remove (which drops the yes), waits for a yes of its own.
const pathAutoAllow = pathState + "/auto-update.json"

// autoAllowMax bounds the file: 64 KiB is far more names than a box has modules.
const autoAllowMax = 64 << 10

type autoAllowFile struct {
	Modules map[string]string `json:"modules"`
}

// AutoAllowed is the entries of the file, name to repository, whose name is a module's and whose repository is "<owner>/<repo>". It never fails: a file that is missing, empty, broken,
// of the first format (a list of names, which never shipped), too large, or not a regular file (a link, a directory) allows nothing, and an entry that is not valid is not there.
func AutoAllowed(roots Roots) map[string]string {
	allowed := map[string]string{}
	raw, err := readRegular(roots.Path(pathAutoAllow), autoAllowMax)
	if err != nil {
		return allowed
	}
	var f autoAllowFile
	if json.Unmarshal(raw, &f) != nil {
		return allowed
	}
	for name, repo := range f.Modules {
		if nameRe.MatchString(name) && osmodule.ValidRepository(repo) {
			allowed[name] = repo
		}
	}
	return allowed
}

// SetAutoAllowed puts the module called name in the file with the repository it comes from (on), or takes it out (repository is then not read), and writes the file whole or not at
// all, root's, 0644. A change that changes nothing writes nothing. The read, the change and the rename are done under a lock of a neighbour file, so two changes at once both count. A
// link or anything but a regular file at the place of the file is left as it is, and refused.
func SetAutoAllowed(roots Roots, name, repository string, on bool) error {
	return setAutoAllowed(roots, name, repository, on, false)
}

// ErrNotInstalled is AllowAutoUpdate's answer for a module that is not installed.
var ErrNotInstalled = errors.New("the module is not installed")

// AllowAutoUpdate is SetAutoAllowed's yes for an installed module: under the lock it checks that the module's manifest is still there, and answers ErrNotInstalled, writing
// nothing, when it is not. A remove takes the yes out after the module's files are gone, so a yes given while it runs is never left behind.
func AllowAutoUpdate(roots Roots, name, repository string) error {
	return setAutoAllowed(roots, name, repository, true, true)
}

func setAutoAllowed(roots Roots, name, repository string, on, installedOnly bool) error {
	if !nameRe.MatchString(name) {
		return fmt.Errorf("not the name of a module: %q", name)
	}
	if on && !osmodule.ValidRepository(repository) {
		return fmt.Errorf("not the repository of a module: %q", repository)
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
	if installedOnly {
		installed, _, err := (&box{env: &Env{Roots: roots}}).InstalledManifests()
		if err != nil {
			return err
		}
		if _, there := installed[name]; !there {
			return ErrNotInstalled
		}
	}
	allowed := AutoAllowed(roots)
	if repo, there := allowed[name]; on && there && repo == repository || !on && !there {
		return nil
	}
	if on {
		allowed[name] = repository
	} else {
		delete(allowed, name)
	}
	// (json sorts the keys of a map)
	raw, err := json.Marshal(autoAllowFile{Modules: allowed})
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
