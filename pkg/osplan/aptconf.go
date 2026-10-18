package osplan

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// The configuration of apt the sandbox runs with. "apt-get update" runs the hooks that /etc/apt/apt.conf.d has for it
// (APT::Update::Pre-Invoke, Post-Invoke, Post-Invoke-Success: Debian's 15update-stamp touches
// /var/lib/apt/periodic/update-success-stamp, and other packages run commands), which would write outside the sandbox.
// apt reads Dir::Etc::parts and Dir::Etc::main BEFORE it parses its command line, so -o cannot move them; the file the
// APT_CONFIG environment variable names is read first and can. The sandbox's apt-config therefore points both to copies in
// the sandbox directory.
//
// The copies are NOT the box's files. apt reads its configuration as a language, not as lines of text (a comment
// spliced into a name, "#include", "#clear", a brace block, a setting of Dir::Etc::main or of Dir::Cache::pkgcache inside a
// fragment: each of them got past a filter on the text in a real run of apt 2.2.4), so nothing of what the box wrote is
// copied: a file of the box is READ by the grammar below, and only if every one of its lines is understood is it written
// again, in the one form the sandbox's apt reads as it is (see canonicalAptConf). A file with a single line the grammar
// does not know is left out whole, and is named, with the reason, in the plan (apt_conf_left_out). The boxes that keep their
// proxy in a brace block ("Acquire { http { Proxy ...; }; };") therefore get no proxy in the sandbox, and a plan that needs
// it fails with sandbox_update_failed, kind network, and the list says why: that is the price of a default that denies.

const (
	maxAptFragments     = 200
	maxAptFragmentBytes = 64 << 10
	maxAptValueBytes    = 512
)

// The reasons a file of the box's apt configuration is left out of the sandbox (AptConfLeftOut.Reason).
const (
	// ConfNotSimple: a line that is not "NAME "VALUE";" or a "//" comment (a brace block, a comment between "/*" and "*/", a
	// statement that is cut, or followed by another or by a comment, a value that is not printable ASCII), or a file that is
	// not text.
	ConfNotSimple = "not_simple"
	// ConfHasDirective: a line that starts with "#" ("#include" and "#clear" change what apt reads).
	ConfHasDirective = "has_directive"
	// ConfNotAllowedSetting: a plain statement of a setting that is not on the short list that the sandbox keeps
	// (sandboxAptSettings): any other setting of apt, and in Acquire:: any that a box does not need to say, those that make
	// apt's helpers run or write something (Acquire::gpgv::Options, Acquire::<method>::Options and the like) included.
	ConfNotAllowedSetting = "not_allowed_setting"
	// ConfTooLarge: more than maxAptFragmentBytes, or one of the files beyond the first maxAptFragments.
	ConfTooLarge = "too_large"
	// ConfNotRegular: a symbolic link, a directory, a device: only a regular file is read.
	ConfNotRegular = "not_regular"
	// ConfProxyAutoDetect: Acquire::...::Proxy-Auto-Detect runs the program it names.
	ConfProxyAutoDetect = "proxy_auto_detect"
)

// AptConfLeftOut is a file of the box's apt configuration that the sandbox did not get: its name ("99proxy", or "apt.conf" for
// /etc/apt/apt.conf), and why.
type AptConfLeftOut struct {
	File   string `json:"file"`
	Reason string `json:"reason"`
}

// compareLeftOut orders the list by file, then by reason (a fragment can be called apt.conf too), so that it is the same
// whatever order the box lists its files in.
func compareLeftOut(a, b AptConfLeftOut) int {
	return cmp.Or(strings.Compare(a.File, b.File), strings.Compare(a.Reason, b.Reason))
}

// MaxLeftOut is the most files that the plan, and a failure, name as left out of the sandbox: the fragments beyond the 200th are
// left out without being read, and named, so a box with thousands of them would put some 200 KB in every answer of GET plan.
const MaxLeftOut = 200

// capLeftOut is a copy of the list in the order of compareLeftOut (a list, never nil), cut at MaxLeftOut, and whether it was cut.
func capLeftOut(list []AptConfLeftOut) ([]AptConfLeftOut, bool) {
	out := append([]AptConfLeftOut{}, list...)
	slices.SortFunc(out, compareLeftOut)
	if len(out) > MaxLeftOut {
		return out[:MaxLeftOut], true
	}
	return out, false
}

// The keys of the data of a failure that say what the sandbox did not get of the box's apt configuration.
const (
	DataConfLeftOut          = "apt_conf_left_out"
	DataConfLeftOutTruncated = "apt_conf_left_out_truncated"
)

// withAptConf puts in the data of a failure the files of apt's configuration that the sandbox left out, when it has any and the failure is of the kind that they can
// explain: an update that fails (a proxy that was left out is a network that cannot be reached), a solver that fails or an output that is not understood. The list is
// the plan's own (same entries, same reasons, same order and cut): the page says it under the sentence of the failure, which would send the owner to the network
// without it. A failure that is nil, or of another code, is given back as it is.
func (f *Failure) withAptConf(left []AptConfLeftOut) *Failure {
	if f == nil || len(left) == 0 {
		return f
	}
	switch f.Code {
	case CodeSandboxUpdateFailed, CodeSandboxSolverFailed, CodePlanUnparsed:
	default:
		return f
	}
	list, cut := capLeftOut(left)
	if f.Data == nil {
		f.Data = map[string]any{}
	}
	f.Data[DataConfLeftOut] = list
	if cut {
		f.Data[DataConfLeftOutTruncated] = true
	}
	return f
}

// AptConfig is the configuration of apt the sandbox gets: Fragments, with their path in the sandbox directory
// ("apt.conf.d/99proxy"), Main (the content of apt.conf, when the box has one and something of it is kept), and what the box
// has and the sandbox does not.
type AptConfig struct {
	Fragments []TreeFile
	Main      string
	HasMain   bool
	LeftOut   []AptConfLeftOut
}

var (
	// aptStatement is any plain statement, NAME "VALUE"; and nothing else on the line. What its name is, is decided after.
	aptStatement = regexp.MustCompile(`^[ \t]*([^ \t"\\;{}]+)[ \t]+"([^"\\]*)"[ \t]*;[ \t]*$`)
)

// httpSettings are the settings that apt's http method reads, and https is the same file (measured in the Debian 11 chroot, apt
// 2.2.4, and in apt 2.6.1: the strings of /usr/lib/apt/methods/http hold Proxy, Timeout, Pipeline-Depth, Dl-Limit, Max-Age,
// No-Cache and AllowRedirect, and https is a link to it).
var httpSettings = []string{"Proxy", "Timeout", "Pipeline-Depth", "Dl-Limit", "Max-Age", "No-Cache", "AllowRedirect"}

// sandboxAptSettings is the ONE list of the settings of a box's apt that the sandbox keeps: anything that is not in it leaves
// its whole file out (ConfNotAllowedSetting). The CI report keeps a python copy of this grammar (and the leg a canary fragment
// for it): the CI streams mirror this table, so a change is made here first and then there; a test pins it entry by entry.
//
// It is a list of what a box really sets (the proxy, the time to wait, the number of tries, the languages) and of nothing
// more, because an open "Acquire::" is not safe: Acquire::gpgv::Options is read by the gpgv that apt runs, as the user _apt,
// and "--log-file=/var/lib/apt/lists/partial/x" makes it write that file wherever it is (run in the Debian 11 chroot with
// apt 2.2.4: the file appeared in the box's own /var/lib/apt/lists/partial and in /var/tmp, owned by _apt). The same
// door is open for Acquire::<method>::Options, Acquire::<method>::Method, Cmd, Command, Sandbox (the user apt drops to),
// Changelogs, Auto-Detect, CompressionTypes::*::Comp-Type and the like: none of them is here.
//
// Each method keeps the settings that a box really sets. For http (and https, which is the same binary) they are what its
// binary reads. For ftp they are Proxy (and its per-host form) and Timeout, and NOT because the binary reads no more: the
// strings of /usr/lib/apt/methods/ftp of apt 2.2.4 also hold Acquire::FTP::Passive, Acquire::FTP::ForceExtended and
// Acquire::ftp::ProxyLogin, which a box rarely sets. A fragment that sets one of them leaves its whole file out as
// not_allowed_setting like any setting that is not in the table, and the plan names it; that
// costs a box that does use them a plan that fails with the list of what was left out, and never makes apt do more than the
// table says. What the ftp method does NOT read is what the http method does (Pipeline-Depth, Dl-Limit, Max-Age, No-Cache,
// AllowRedirect): a setting that a method does not read does nothing, and is not kept for it.
var sandboxAptSettings = struct {
	// PerMethod are the download methods that have settings kept, each with the settings that it reads: Acquire::<method>::<setting>.
	PerMethod map[string][]string
	// PerHost are the PerMethod settings that apt also reads for ONE host, Acquire::<method>::<setting>::<host>: the proxy of
	// a host is the commonest real proxy (squid-deb-proxy, apt-cacher), and apt 2.2.4 reads it as the proxy of that host
	// and of no other (the Debian 11 chroot: the request for deb.debian.org went to that proxy, the one for another host
	// went direct). A host has letters, digits, "-", "_" and "." in it, never two dots in a row, and is not only dots.
	// The ftp method reads it too (its strings hold Acquire::ftp::Proxy::).
	PerHost []string
	// Top are the settings of Acquire::<setting> itself.
	Top []string
	// Other are whole names outside Acquire: what a simulation reads of what is installed with it (the recommends and the suggests), and of the release it installs from
	// (Default-Release: a box that sets it to "stable" while it also lists testing is installed from stable, and a simulation without the setting would choose from both).
	Other []string
}{
	PerMethod: map[string][]string{
		"http":  httpSettings,
		"https": httpSettings,
		"ftp":   {"Proxy", "Timeout"},
	},
	PerHost: []string{"Proxy"},
	Top:     []string{"Retries", "Languages", "PDiffs", "By-Hash", "Check-Valid-Until", "ForceIPv4", "ForceIPv6", "Queue-Mode"},
	Other:   []string{"APT::Install-Recommends", "APT::Install-Suggests", "APT::Default-Release"},
}

// settingsOf are the settings kept of a download method (the name in any case), and whether the method is one of the table.
func settingsOf(method string) ([]string, bool) {
	for m, settings := range sandboxAptSettings.PerMethod {
		if strings.EqualFold(m, method) {
			return settings, true
		}
	}
	return nil, false
}

// inFold says whether the ASCII name is one of the list, whatever the case: apt reads names in any case ("acquire::http::proxy"
// is "Acquire::http::Proxy") and so do we, in ASCII's case only (the callers have checked that the name is ASCII).
func inFold(list []string, name string) bool {
	return slices.ContainsFunc(list, func(s string) bool { return strings.EqualFold(s, name) })
}

// keptAptName says whether a setting is one of sandboxAptSettings.
func keptAptName(name string) bool {
	for i := 0; i < len(name); i++ {
		switch c := name[i]; {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_', c == '.', c == ':':
		default:
			// (so that EqualFold below is ASCII's too: the Kelvin sign is not a "k", the long s is not an "s")
			return false
		}
	}
	if inFold(sandboxAptSettings.Other, name) {
		return true
	}
	if len(name) < len("acquire::") || !strings.EqualFold(name[:len("acquire::")], "acquire::") {
		return false
	}
	segs := strings.Split(name[len("acquire::"):], "::")
	if len(segs) == 1 {
		return inFold(sandboxAptSettings.Top, segs[0])
	}
	settings, isMethod := settingsOf(segs[0])
	switch {
	case !isMethod:
		return false
	case len(segs) == 2:
		return inFold(settings, segs[1])
	case len(segs) == 3:
		host := segs[2]
		// (a lone ":" is in no name of a host; a segment with nothing in it is only dots too: Trim of "" is "")
		return inFold(settings, segs[1]) && inFold(sandboxAptSettings.PerHost, segs[1]) && !strings.Contains(host, ":") && !strings.Contains(host, "..") && strings.Trim(host, ".") != ""
	}
	return false
}

// canonicalAptConf reads the content of a file of apt's configuration with the grammar of the sandbox, and gives it back in
// its one canonical form: a line NAME "VALUE"; for each statement, in the order of the file, and nothing else (the comments
// are dropped). Every line of the file is one of: blank; a comment from "//" to the end of the line; a statement
// NAME "VALUE"; whose NAME is in sandboxAptSettings (and is not a program to run: a name with Proxy-Auto-Detect or
// ProxyAutoDetect in it), and whose VALUE is at most maxAptValueBytes of printable ASCII with no quote and no backslash. Nothing is guessed: the
// first line that is anything else is why the file is left out, and the reason is returned instead of a text.
func canonicalAptConf(data []byte) (text, reason string) {
	if len(data) > maxAptFragmentBytes {
		return "", ConfTooLarge
	}
	if !utf8.Valid(data) {
		return "", ConfNotSimple
	}
	var b strings.Builder
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSuffix(line, "\r")
		for i := 0; i < len(line); i++ {
			if c := line[i]; (c < 0x20 && c != '\t') || c == 0x7f {
				return "", ConfNotSimple
			}
		}
		switch t := strings.Trim(line, " \t"); {
		case t == "", strings.HasPrefix(t, "//"):
			continue
		case strings.HasPrefix(t, "#"):
			return "", ConfHasDirective
		}
		m := aptStatement.FindStringSubmatch(line)
		if m == nil {
			return "", ConfNotSimple
		}
		name, value := m[1], m[2]
		if len(value) > maxAptValueBytes || !printableASCII(value) {
			return "", ConfNotSimple
		}
		// (a name that runs a program is named as such, though it is not in the list either)
		switch low := strings.ToLower(name); {
		case strings.HasPrefix(low, "acquire::") && (strings.Contains(low, "proxy-auto-detect") || strings.Contains(low, "proxyautodetect")):
			return "", ConfProxyAutoDetect
		case !keptAptName(name):
			return "", ConfNotAllowedSetting
		}
		b.WriteString(name + ` "` + value + "\";\n")
	}
	return b.String(), ""
}

func printableASCII(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 || s[i] > 0x7e {
			return false
		}
	}
	return true
}

// aptFragmentName says whether apt reads a file of that name in apt.conf.d (ReadConfigDir: GetListOfFilesInDir with the
// extension conf, and none allowed): letters, digits, "_", "-" and ":" (apt 2.2.4 lets a colon in, as run-parts does not: a
// fragment "99a:b.conf" is read by apt-config dump), not starting with a dot, and a dot only before an extension that is "conf".
func aptFragmentName(name string) bool {
	if name == "" || name[0] == '.' {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.' || c == ':') {
			return false
		}
	}
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i+1:] == "conf"
	}
	return true
}

// readAptConfFile reads one file of the box's apt configuration through the Probe, as the sandbox may have it: the canonical
// text of it (empty when there is nothing in it that is a setting), or the reason it is left out. ok is false when the file
// is not there (it went between the listing and the read), and then it is nothing; so is a directory when skipDir says so
// (apt passes over the directories of apt.conf.d without a word, so there is nothing to name).
func readAptConfFile(ctx context.Context, files Files, p string, skipDir bool) (text, reason string, ok bool, err error) {
	info, err := files.Lstat(ctx, p)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return "", "", false, nil
	case err != nil:
		return "", "", false, err
	case skipDir && info.Mode.IsDir():
		return "", "", false, nil
	case !info.Mode.IsRegular():
		return "", ConfNotRegular, true, nil
	}
	data, err := files.ReadFile(ctx, p)
	switch {
	case errors.Is(err, osreadiness.ErrTooLarge):
		return "", ConfTooLarge, true, nil
	case errors.Is(err, fs.ErrNotExist):
		return "", "", false, nil
	case err != nil:
		return "", "", false, err
	}
	text, reason = canonicalAptConf(data)
	return text, reason, true, nil
}

// ReadAptConfig reads the configuration of apt that the sandbox keeps, through the Probe: the files of /etc/apt/apt.conf.d that
// apt reads (at most maxAptFragments of them, the others are left out and named), and /etc/apt/apt.conf, each through
// canonicalAptConf: kept again in its canonical form, or left out whole with its reason.
func ReadAptConfig(ctx context.Context, files Files) (AptConfig, error) {
	var cfg AptConfig
	names, err := files.ReadDir(ctx, "/etc/apt/apt.conf.d")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, err
	}
	read := 0
	for _, name := range names {
		if !aptFragmentName(name) {
			continue
		}
		if read++; read > maxAptFragments {
			cfg.LeftOut = append(cfg.LeftOut, AptConfLeftOut{File: name, Reason: ConfTooLarge})
			continue
		}
		text, reason, ok, err := readAptConfFile(ctx, files, "/etc/apt/apt.conf.d/"+name, true)
		switch {
		case err != nil:
			return cfg, err
		case !ok:
		case reason != "":
			cfg.LeftOut = append(cfg.LeftOut, AptConfLeftOut{File: name, Reason: reason})
		case text != "":
			cfg.Fragments = append(cfg.Fragments, TreeFile{Path: "apt.conf.d/" + name, Content: text})
		}
	}
	text, reason, ok, err := readAptConfFile(ctx, files, "/etc/apt/apt.conf", false)
	switch {
	case err != nil:
		return cfg, err
	case !ok:
	case reason != "":
		cfg.LeftOut = append(cfg.LeftOut, AptConfLeftOut{File: "apt.conf", Reason: reason})
	case text != "":
		cfg.Main, cfg.HasMain = text, true
	}
	slices.SortFunc(cfg.LeftOut, compareLeftOut)
	return cfg, nil
}

// A SandboxFile is something to write in the sandbox directory: a file, or (Mode has fs.ModeDir) a directory.
type SandboxFile struct {
	Path    string
	Content []byte
	Mode    fs.FileMode
}

// SandboxFiles are all that is written in the sandbox directory dir before apt runs, in the order of writing: the
// directories (apt makes the ones it needs too, as root, but the measured recipe made them first), the tree of the
// sources, the copies of the configuration, and apt-config, which the environment names. Every path is inside dir.
func SandboxFiles(dir string, tree []TreeFile, cfg AptConfig) []SandboxFile {
	var out []SandboxFile
	for _, d := range []string{"/parts", "/apt.conf.d", "/lists/partial", "/cache/archives/partial"} {
		out = append(out, SandboxFile{Path: dir + d, Mode: fs.ModeDir | 0o700})
	}
	// where the temporary files of apt's methods go (SandboxEnv: TMPDIR): the methods run as _apt, which apt drops to, and
	// apt falls back to /tmp for a user that cannot write in TMPDIR, as it measured (apt 2.2.4: the files of the signature
	// check were in /tmp when TMPDIR was the sandbox directory itself, which only the core's user may write in). Anyone may
	// write here for as long as the plan runs, and the sticky bit keeps one from touching another's files.
	out = append(out, SandboxFile{Path: dir + "/" + SandboxTmp, Mode: fs.ModeDir | fs.ModeSticky | 0o777})
	for _, f := range tree {
		out = append(out, SandboxFile{Path: dir + "/" + f.Path, Content: []byte(f.Content), Mode: 0o600})
	}
	for _, f := range cfg.Fragments {
		out = append(out, SandboxFile{Path: dir + "/" + f.Path, Content: []byte(f.Content), Mode: 0o600})
	}
	if cfg.HasMain {
		out = append(out, SandboxFile{Path: dir + "/apt.conf", Content: []byte(cfg.Main), Mode: 0o600})
	}
	out = append(out, SandboxFile{Path: dir + "/apt-config", Content: []byte(AptConfigText(dir)), Mode: 0o600})
	return out
}

// SandboxTmp is the directory of the sandbox that TMPDIR names.
const SandboxTmp = "tmp"

// AptConfigText is the file that APT_CONFIG names, whole: three statements, read by apt before anything else. The parts and
// the main file of the sandbox, in place of the box's; and no log of the planner: apt writes the "eipp" log of every
// simulation to /var/log/apt/eipp.log.xz, and that is the log of the last REAL transaction of the box (apt 2.2.4 and 2.6.1 both
// write it for -s upgrade and -s dist-upgrade, and neither writes anything when Dir::Log::Planner is empty).
func AptConfigText(dir string) string {
	return "Dir::Etc::parts \"" + dir + "/apt.conf.d\";\n" +
		"Dir::Etc::main \"" + dir + "/apt.conf\";\n" +
		"Dir::Log::Planner \"\";\n"
}
