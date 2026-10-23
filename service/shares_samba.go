package service

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ReCasaOS/CasaOS/pkg/config"
	model2 "github.com/ReCasaOS/CasaOS/service/model"
)

const (
	sambaConfigDir       = "/etc/samba"
	sambaConfigFile      = "/etc/samba/smb.conf"
	sambaShareConfigName = "smb.casa.conf"
	sambaShareConfigFile = "/etc/samba/smb.casa.conf"
)

// sambaSection renders one share as an smb.conf section.
//
// A share with no Username is written exactly as CasaOS has always written it,
// field for field and line for line. Every row predating authenticated shares
// carries an empty Username, so an upgrade regenerates their sections unchanged.
//
// An authenticated share drops the guest access, the world-writable masks and
// the root ownership together. Leaving any single one behind would make the
// authentication decorative: "guest ok" alone re-opens the share, and
// "force user = root" alone hands every file back to root no matter who
// connected.
func sambaSection(share model2.SharesDBModel) string {
	name := filepath.Base(share.Path)

	// "vfs objects" is a per-share setting, which is why the global template
	// leaves its module line commented out: a share without the flag loads no
	// module at all, and putting one in [global] instead is what takes every
	// share down on a host missing vfs_fruit. "fruit:time machine" already
	// implies durable handles and no kernel oplocks, kernel share modes or posix
	// locking for this share, and smbd advertises it as _adisk._tcp over mDNS by
	// itself, so those lines are not repeated here.
	timeMachine := ""
	if share.TimeMachine {
		timeMachine = "vfs objects = catia fruit streams_xattr\n" +
			"fruit:time machine = yes\n"
	}

	if share.Username == "" {
		return "\n[" + name + "]\n" +
			"comment = CasaOS share " + name + "\n" +
			"public = Yes\n" +
			"path = " + share.Path + "\n" +
			"browseable = Yes\n" +
			"read only = No\n" +
			"guest ok = Yes\n" +
			"create mask = 0777\n" +
			"directory mask = 0777\n" +
			"force user = root\n" + timeMachine + "\n"
	}

	return "\n[" + name + "]\n" +
		"comment = CasaOS share " + name + "\n" +
		"public = No\n" +
		"path = " + share.Path + "\n" +
		"browseable = Yes\n" +
		"read only = No\n" +
		"guest ok = No\n" +
		"valid users = " + share.Username + "\n" +
		"create mask = 0660\n" +
		"directory mask = 0770\n" +
		"force user = " + share.Username + "\n" + timeMachine + "\n"
}

// validateSambaConfig asks Samba's own parser whether the generated file is
// usable.
//
// smb.casa.conf is included by smb.conf, and casaos-wsdd.service declares
// PartOf=smbd.service: a configuration smbd refuses does not merely break
// sharing, it takes network discovery down with it, and neither failure is
// reported anywhere. Checking first is what stops one bad share from silently
// disabling both.
//
// A host without Samba's tooling installed has nothing to validate against,
// which is not an error.
func validateSambaConfig() error {
	if _, err := exec.LookPath("testparm"); err != nil {
		return nil
	}

	output, err := exec.Command("testparm", "--suppress-prompt", sambaConfigFile).CombinedOutput()
	if err != nil {
		return fmt.Errorf("samba rejected the generated share configuration: %s", strings.TrimSpace(string(output)))
	}

	return nil
}

// SambaMainConfigExternal: the host owns smb.conf ([server] SambaMainConfig =
// external), e.g. an image-built system where it is read-only. CasaOS then
// never writes it (no first-share template, no guest-mapping flip) and manages
// only smb.casa.conf, which the host's smb.conf has to include.
func SambaMainConfigExternal() bool {
	return strings.EqualFold(strings.TrimSpace(config.ServerInfo.SambaMainConfig), "external")
}

var ErrSambaIncludeMissing = errors.New("smb.conf is owned by the host ([server] SambaMainConfig = external) but does not include " + sambaShareConfigFile)

// includesShareConfig: smb.conf has an `include = /etc/samba/smb.casa.conf` line.
func includesShareConfig(mainConfig []byte) bool {
	for _, line := range strings.Split(string(mainConfig), "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.EqualFold(strings.TrimSpace(key), "include") && strings.TrimSpace(value) == sambaShareConfigFile {
			return true
		}
	}
	return false
}

// checkExternalMainConfig: in external mode, the host's smb.conf must include
// smb.casa.conf, or every share CasaOS writes is silently ignored.
func checkExternalMainConfig() error {
	if !SambaMainConfigExternal() {
		return nil
	}
	content, err := os.ReadFile(sambaConfigFile)
	if err != nil {
		return fmt.Errorf("read the host's smb.conf: %w", err)
	}
	if !includesShareConfig(content) {
		return ErrSambaIncludeMissing
	}
	return nil
}

var ErrTimeMachineUnsupported = errors.New("Time Machine needs Samba's vfs_fruit module (package samba-vfs-modules on Debian and Ubuntu)")

// ValidateTimeMachineSupport checks that smbd can load the module a Time
// Machine share names.
//
// testparm parses the configuration without loading VFS modules, so a missing
// one sails through validateSambaConfig and only surfaces as that single share
// refusing every connection. A host without smbd has nothing to check against,
// which is not an error, and neither is a build that lays its modules out
// somewhere unexpected: the guard never turns into a false refusal.
func ValidateTimeMachineSupport() error {
	output, err := exec.Command("smbd", "-b").Output()
	if err != nil {
		return nil
	}

	for _, line := range strings.Split(string(output), "\n") {
		key, dir, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || key != "MODULESDIR" {
			continue
		}

		if _, err := os.Stat(filepath.Join(strings.TrimSpace(dir), "vfs", "fruit.so")); err != nil {
			return ErrTimeMachineUnsupported
		}

		return nil
	}

	return nil
}

// syncGuestMapping keeps the global guest mapping in step with whether any share
// is actually protected.
//
// With a protected share present the setting has to be "never": under
// "bad user" a client whose credentials are refused is silently remapped to the
// guest account and receives ACCESS_DENIED instead of being asked for a
// password, so a protected share looks broken rather than protected.
//
// With no protected share it has to go back to "bad user". Windows sends the
// logged-in account name whether or not the share wants one, and under "never"
// that unknown name is refused outright instead of falling back to guest, which
// would break the plain guest shares people have always used.
//
// Only that one line is rewritten, on an smb.conf CasaOS has already written.
// InitSambaConfig returns early on such hosts, so this is the only route by
// which the setting can reach an existing installation, and regenerating the
// whole file would discard the hand edits the upstream issue thread is full of.
func syncGuestMapping(hasProtectedShare bool) (bool, error) {
	// external: the host's smb.conf, the host's guest mapping
	if SambaMainConfigExternal() {
		return false, nil
	}

	content, err := os.ReadFile(sambaConfigFile)
	if err != nil {
		return false, err
	}

	from, to := "map to guest = never", "map to guest = bad user"
	if hasProtectedShare {
		from, to = to, from
	}

	updated := strings.ReplaceAll(string(content), from, to)
	if updated == string(content) {
		return false, nil
	}

	return true, os.WriteFile(sambaConfigFile, []byte(updated), 0o644)
}

// shareRoots are the directories a share may live under.
//
// Sharing a folder changes its ownership and permissions as root, on a path the
// caller supplies. Without a boundary that is a primitive for handing any
// directory on the host to an arbitrary account, so the API is confined to the
// places a network share plausibly belongs: CasaOS storage and the usual mount
// points for external media.
var shareRoots = []string{
	"/DATA",
	"/var/lib/casaos/files",
	"/mnt",
	"/media",
	"/srv",
}

var (
	ErrSharePathNotDirectory = errors.New("only a directory can be shared")
	ErrSharePathOutsideRoots = errors.New("a share must live under /DATA, /mnt, /media or /srv")
)

// ValidateSharePath resolves path and reports whether it is a real directory
// inside one of the permitted roots.
//
// Symlinks are resolved before the check rather than after, so a link planted
// inside a share cannot be used to reach a target outside it.
func ValidateSharePath(path string) (string, error) {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", err
	}

	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", err
	}

	info, err := os.Lstat(resolved)
	if err != nil {
		return "", err
	}

	if !info.IsDir() {
		return "", ErrSharePathNotDirectory
	}

	for _, root := range shareRoots {
		if resolved == root || strings.HasPrefix(resolved, root+string(os.PathSeparator)) {
			return resolved, nil
		}
	}

	return "", ErrSharePathOutsideRoots
}
