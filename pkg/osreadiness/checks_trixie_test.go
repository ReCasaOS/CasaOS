package osreadiness

import (
	"io/fs"
	"testing"
)

func sshServer(f *fakeProbe) { f.installed(pkg("openssh-server", "1:9.2p1-2+deb12u7"), "openssh-server") }

func TestSSHDSAHostKey(t *testing.T) {
	conf := func(text string) func(*fakeProbe) { return func(f *fakeProbe) { f.set(sshdConfig, text) } }
	key := func(name, pub string) func(*fakeProbe) { return func(f *fakeProbe) { f.set(name+".pub", pub) } }
	runScenarios(t, "ssh_dsa_hostkey", []scenario{
		{name: "Debian's own sshd_config, which names no key", setup: after(debian12, sshServer, conf("#HostKey /etc/ssh/ssh_host_rsa_key\nInclude /etc/ssh/sshd_config.d/*.conf\n")), severity: OK},
		{name: "no sshd_config at all", setup: after(debian12, sshServer), severity: OK},
		{name: "no SSH server", setup: after(debian12, func(f *fakeProbe) { f.installed("", "openssh-server") }, conf("HostKey /etc/ssh/ssh_host_dsa_key\n")), severity: OK},
		{name: "Ed25519 and RSA", setup: after(debian12, sshServer, conf("HostKey /etc/ssh/ssh_host_ed25519_key\nHostKey /etc/ssh/ssh_host_rsa_key\n"),
			key("/etc/ssh/ssh_host_ed25519_key", "ssh-ed25519 AAAA root@box\n"), key("/etc/ssh/ssh_host_rsa_key", "ssh-rsa AAAA root@box\n")), severity: OK},
		{name: "an ECDSA key whose public half is not there", setup: after(debian12, sshServer, conf("HostKey /etc/ssh/ssh_host_ecdsa_key\n")), severity: OK},
		{name: "only a DSA key", setup: after(debian12, sshServer, conf("HostKey /etc/ssh/ssh_host_dsa_key\n"), key("/etc/ssh/ssh_host_dsa_key", "ssh-dss AAAA root@box\n")),
			severity: Refuse, data: D{"keys": []string{"/etc/ssh/ssh_host_dsa_key"}}, action: ActionNone},
		{name: "a DSA key by its name, in sshd_config.d, written with an equals sign", setup: after(debian12, sshServer, func(f *fakeProbe) {
			f.set(sshdConfigDir+"/old.conf", "hostkey=/etc/ssh/ssh_host_dsa_key\n")
		}), severity: Refuse},
		{name: "a DSA key beside an Ed25519 one", setup: after(debian12, sshServer, conf("HostKey /etc/ssh/ssh_host_ed25519_key\nHostKey /etc/ssh/ssh_host_dsa_key\n"),
			key("/etc/ssh/ssh_host_ed25519_key", "ssh-ed25519 AAAA\n"), key("/etc/ssh/ssh_host_dsa_key", "ssh-dss AAAA\n")),
			severity: Warn, data: D{"keys": []string{"/etc/ssh/ssh_host_dsa_key"}}},
		{name: "an sshd_config that cannot be read", setup: after(debian12, sshServer, func(f *fakeProbe) {
			f.fail(sshdConfig, &fs.PathError{Op: "open", Path: sshdConfig, Err: fs.ErrPermission})
		}), severity: Refuse, unknown: ReasonUnreadable},
	})
}

func TestCryptsetupMissing(t *testing.T) {
	runScenarios(t, "cryptsetup_missing", []scenario{
		{name: "no crypttab", setup: debian12, severity: OK},
		{name: "a crypttab of comments", setup: after(debian12, func(f *fakeProbe) { f.set(crypttabPath, "# <target name> <source device> <key file> <options>\n") }), severity: OK},
		{name: "an encrypted volume", setup: after(debian12, func(f *fakeProbe) {
			f.set(crypttabPath, "# comment\nsda5_crypt UUID=1234 none luks,discard\n")
		}), severity: Warn, data: D{"count": 1}, action: ActionNone},
	})
}

func TestSysctlConf(t *testing.T) {
	runScenarios(t, "sysctl_conf", []scenario{
		{name: "Debian's own, all comments", setup: after(debian12, func(f *fakeProbe) { f.set(sysctlConf, "#\n# /etc/sysctl.conf\n#net.ipv4.ip_forward=1\n; old style\n") }), severity: OK},
		{name: "no file", setup: debian12, severity: OK},
		{name: "two settings", setup: after(debian12, func(f *fakeProbe) { f.set(sysctlConf, "net.ipv4.ip_forward=1\n\nvm.swappiness = 10\n") }), severity: Warn, data: D{"count": 2}},
	})
}

func TestTmpTmpfs(t *testing.T) {
	runScenarios(t, "tmp_tmpfs", []scenario{
		{name: "a /tmp of its own in fstab", setup: after(debian12, func(f *fakeProbe) {
			f.set(fstabPath, "UUID=1 / ext4 errors=remount-ro 0 1\nUUID=2 /tmp ext4 defaults 0 2\n")
		}), severity: OK},
		{name: "/tmp on the root filesystem", setup: after(debian12, func(f *fakeProbe) { f.set(fstabPath, "UUID=1 / ext4 errors=remount-ro 0 1\n") }), severity: Warn},
		{name: "a /tmp that is commented out", setup: after(debian12, func(f *fakeProbe) {
			f.set(fstabPath, "UUID=1 / ext4 errors=remount-ro 0 1\n#UUID=2 /tmp ext4 defaults 0 2\n")
		}), severity: Warn},
	})
}
