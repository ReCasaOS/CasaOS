package osreadiness

import (
	"context"
	"path"
	"slices"
	"strings"
)

// The checks of the hop from Debian 12 to 13 that the release notes of Debian 13 name (chapter 5.1), beyond the ones that other
// hops share (OpenSSH, cgroup v1, /boot). They read files only. i386, which Debian 13 no longer supports as a system, is refused by
// unproven_arch already, as everything that is not amd64.
func init() {
	register(
		Check{Code: "ssh_dsa_hostkey", Group: GroupPackages, Title: "The SSH server has a host key it can still load", Hops: []HopID{HopDebian12to13}, Run: checkSSHDSAHostKey},
		Check{Code: "cryptsetup_missing", Group: GroupFilesystem, Title: "The encrypted volumes will open after the upgrade", Hops: []HopID{HopDebian12to13}, Run: checkCrypttab},
		Check{Code: "sysctl_conf", Group: GroupFilesystem, Title: "No setting is left in /etc/sysctl.conf", Hops: []HopID{HopDebian12to13}, Run: checkSysctlConf},
		Check{Code: "tmp_tmpfs", Group: GroupFilesystem, Title: "/tmp stays where it is", Hops: []HopID{HopDebian12to13}, Run: checkTmpTmpfs},
	)
}

const (
	sshdConfig    = "/etc/ssh/sshd_config"
	sshdConfigDir = "/etc/ssh/sshd_config.d"
	crypttabPath  = "/etc/crypttab"
	sysctlConf    = "/etc/sysctl.conf"
)

// activeLines are the lines of a file that say something: not blank, and not a comment (# or ;).
func activeLines(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line[0] == '#' || line[0] == ';' {
			continue
		}
		out = append(out, line)
	}
	return out
}

// readIfThere is a file's text, "" with no error when there is no such file.
func readIfThere(ctx context.Context, p Probe, file string) (string, error) {
	data, err := p.ReadFile(ctx, file)
	if isNotExist(err) {
		return "", nil
	}
	return string(data), err
}

// hostKeys are the files that the HostKey lines of sshd's configuration name: sshd_config, and the files of sshd_config.d that
// Debian's own sshd_config includes. A machine with none uses sshd's defaults, of which none is DSA.
func hostKeys(ctx context.Context, p Probe) ([]string, error) {
	texts := []string{}
	main, err := readIfThere(ctx, p, sshdConfig)
	if err != nil {
		return nil, err
	}
	texts = append(texts, main)
	names, err := p.ReadDir(ctx, sshdConfigDir)
	if err != nil && !isNotExist(err) {
		return nil, err
	}
	for _, name := range names {
		if !strings.HasSuffix(name, ".conf") {
			continue
		}
		text, err := readIfThere(ctx, p, path.Join(sshdConfigDir, name))
		if err != nil {
			return nil, err
		}
		texts = append(texts, text)
	}
	var keys []string
	for _, text := range texts {
		for _, line := range activeLines(text) {
			fields := strings.Fields(strings.Replace(line, "=", " ", 1))
			if len(fields) >= 2 && strings.EqualFold(fields[0], "HostKey") {
				key := strings.Trim(fields[1], `"`)
				if !slices.Contains(keys, key) {
					keys = append(keys, key)
				}
			}
		}
	}
	return keys, nil
}

// isDSAKey says whether a host key is a DSA one: its public half says ssh-dss, or, when there is none to read, its name says dsa
// (ssh_host_dsa_key) and not ecdsa.
func isDSAKey(ctx context.Context, p Probe, key string) (bool, error) {
	pub, err := p.ReadFile(ctx, key+".pub")
	switch {
	case err == nil:
		return strings.HasPrefix(strings.TrimSpace(string(pub)), "ssh-dss"), nil
	case !isNotExist(err):
		return false, err
	}
	base := strings.ToLower(path.Base(key))
	return strings.Contains(base, "dsa") && !strings.Contains(base, "ecdsa"), nil
}

func checkSSHDSAHostKey(ctx context.Context, p Probe) Result {
	_, installed, err := versionOf(ctx, p, "openssh-server")
	if err != nil {
		return unknown(err)
	}
	if !installed {
		return pass()
	}
	keys, err := hostKeys(ctx, p)
	if err != nil {
		return unknown(err)
	}
	var dsa []string
	for _, key := range keys {
		is, err := isDSAKey(ctx, p, key)
		if err != nil {
			return unknown(err)
		}
		if is {
			dsa = append(dsa, key)
		}
	}
	switch {
	case len(dsa) == 0:
		return pass()
	case len(dsa) == len(keys):
		return Result{
			Severity: Refuse, Title: "The SSH server has only DSA host keys",
			Detail: "The OpenSSH of Debian 13 no longer loads DSA keys, and every host key that sshd_config names is one: the SSH server would have no key after the restart, and a machine that is reached over SSH would be lost. Make an Ed25519 key (ssh-keygen -t ed25519 -f /etc/ssh/ssh_host_ed25519_key -N ''), name it with HostKey, restart ssh, and check again.",
			Data:   D{"files": dsa}, Action: ActionNone,
		}
	}
	return Result{
		Severity: Warn, Title: "The SSH server has a DSA host key",
		Detail: "The OpenSSH of Debian 13 no longer loads DSA keys: the SSH server will start with its other host keys, and log an error for this one. Remove its HostKey line.",
		Data:   D{"files": dsa}, Action: ActionNone,
	}
}

func checkCrypttab(ctx context.Context, p Probe) Result {
	text, err := readIfThere(ctx, p, crypttabPath)
	if err != nil {
		return unknown(err)
	}
	volumes := activeLines(text)
	if len(volumes) == 0 {
		return pass()
	}
	return Result{
		Severity: Warn, Title: "Encrypted volumes need a new package after the upgrade",
		Detail: "Debian 13 opens the volumes of /etc/crypttab with the package systemd-cryptsetup, which is new in it: systemd recommends it, so the upgrade normally installs it. Before the restart, check that it is there (dpkg -s systemd-cryptsetup), or install it (apt install systemd-cryptsetup): without it, these volumes do not open at the start.",
		Data:   D{"count": len(volumes)}, Action: ActionNone,
	}
}

func checkSysctlConf(ctx context.Context, p Probe) Result {
	text, err := readIfThere(ctx, p, sysctlConf)
	if err != nil {
		return unknown(err)
	}
	settings := activeLines(text)
	if len(settings) == 0 {
		return pass()
	}
	return Result{
		Severity: Warn, Title: "Settings in /etc/sysctl.conf will no longer be applied",
		Detail: "In Debian 13, systemd-sysctl no longer reads /etc/sysctl.conf: its settings would be gone after the restart, without a word. Move them to a file of /etc/sysctl.d, such as /etc/sysctl.d/local.conf.",
		Data:   D{"count": len(settings)}, Action: ActionNone,
	}
}

func checkTmpTmpfs(ctx context.Context, p Probe) Result {
	text, err := readIfThere(ctx, p, fstabPath)
	if err != nil {
		return unknown(err)
	}
	for _, e := range parseFstab(text) {
		if e.File == "/tmp" {
			// a /tmp of its own in fstab overrides the new default
			return pass()
		}
	}
	return Result{
		Severity: Warn, Title: "/tmp will be in memory after the restart",
		Detail: "In Debian 13, /tmp is a tmpfs: it is in memory, takes up to half of it, and is emptied at each start. Anything an application keeps in /tmp is lost at the restart. To keep /tmp on the disk, run systemctl mask tmp.mount before the restart.",
		Data:   D{}, Action: ActionNone,
	}
}
