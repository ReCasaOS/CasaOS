package osreadiness

import (
	"context"
	"io/fs"
	"path"
	"slices"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/osmodule"
)

func init() {
	register(
		Check{Code: "modules", Group: GroupPackages, Title: "No module of ReCasaOS needs care around the upgrade", Run: checkModules},
	)
}

// modulesDir is where the installer puts the manifest of each module it installed: <name>.json.
const modulesDir = "/etc/casaos/modules"

// maxModules is how many manifests are read; a folder with more is a folder that cannot be told.
const maxModules = 32

// engineRunsModuleCalls says whether the upgrade engine makes the calls that a module declares for an upgrade (prepare, then status until ready, the stop of its units, and resume after the
// restart). It does not yet, so a module that declares them is as much a reason to refuse as one that does not: a machine whose virtual machines are running is not upgraded under them.
// The change that makes the calls sets it, and this check stops refusing a module that has declared them and has answered.
const engineRunsModuleCalls = false

// Why a module stands in the way, as the report says it.
const (
	moduleInvalid        = "invalid"
	moduleNoOSUpgrade    = "no_os_upgrade"
	moduleCallsNotRunYet = "calls_not_run_yet"
)

// checkModules refuses an upgrade of a machine that has a module of ReCasaOS installed, and names the module, until the module has said what it needs around the upgrade and the engine does
// it. The manifest is read through the seam, checked against fixed patterns, and never run: a file that cannot be read or does not pass is a refusal that names the file, and not a module
// that is left out.
func checkModules(ctx context.Context, p Probe) Result {
	entries, err := p.ReadDir(ctx, modulesDir)
	if isNotExist(err) {
		return pass()
	}
	if err != nil {
		return unknown(err)
	}
	var files []string
	for _, entry := range entries {
		if strings.HasSuffix(entry, ".json") {
			files = append(files, entry)
		}
	}
	if len(files) == 0 {
		return pass()
	}
	if len(files) > maxModules {
		return unknownWith(ReasonTooLarge)
	}
	var modules, broken, reasons []string
	say := func(reason string) {
		if !slices.Contains(reasons, reason) {
			reasons = append(reasons, reason)
		}
	}
	for _, file := range files {
		manifest, err := readModule(ctx, p, file)
		if err != nil {
			// the name is the file's stem when it is one, and "unnamed" when it is not a name that the report may repeat
			stem := strings.TrimSuffix(file, ".json")
			if token(stem, 32) == "" {
				stem = "unnamed"
			}
			if !slices.Contains(broken, stem) {
				broken = append(broken, stem)
			}
			say(moduleInvalid)
			continue
		}
		modules = append(modules, manifest.Name)
		switch {
		case manifest.OSUpgrade == nil:
			say(moduleNoOSUpgrade)
		case !engineRunsModuleCalls:
			say(moduleCallsNotRunYet)
		}
	}
	if len(reasons) == 0 {
		return pass()
	}
	slices.Sort(modules)
	slices.Sort(broken)
	slices.Sort(reasons)
	data := D{"modules": names(modules), "reasons": reasons}
	if len(broken) > 0 {
		data["invalid"] = names(broken)
	}
	return Result{
		Severity: Refuse, Title: "A module of ReCasaOS is installed",
		Detail: "A module runs services and keeps data of its own that the upgrade does not manage: it must say what it needs around an operating system upgrade, and the upgrade must do it. " +
			"Until then, a machine with a module installed is not upgraded; remove the module, or upgrade by hand.",
		Data: data, Action: ActionNone,
	}
}

// readModule reads and checks the manifest of a file of the folder of modules: a regular file, no link, no larger than the bound.
func readModule(ctx context.Context, p Probe, file string) (osmodule.Manifest, error) {
	full := path.Join(modulesDir, file)
	info, err := p.Lstat(ctx, full)
	if err != nil {
		return osmodule.Manifest{}, err
	}
	if info.Mode&fs.ModeType != 0 || info.Size > osmodule.MaxBytes {
		return osmodule.Manifest{}, &osmodule.Invalid{Field: "file", Reason: osmodule.ReasonUnreadable}
	}
	raw, err := p.ReadFile(ctx, full)
	if err != nil {
		return osmodule.Manifest{}, err
	}
	return osmodule.Parse(file, raw)
}
