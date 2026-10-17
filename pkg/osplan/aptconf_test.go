package osplan

import (
	"context"
	"errors"
	"io/fs"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ReCasaOS/CasaOS/pkg/osreadiness"
)

// ---- a machine in a map, for the tests of this package: it is the Probe of the report, and nothing else ------------

type fakeFiles struct {
	mu      sync.Mutex
	files   map[string]string
	devices map[string]bool
	links   map[string]string
	dirs    map[string]bool
	fsErr   map[string]error
	// readErr are errors of ReadFile alone (fsErr is every operation on the path)
	readErr map[string]error
	free    map[string]uint64
	cmds    map[string]fakeCmd
	ran     [][]string

	busy string
	// busySeq are the answers of MaintenanceBusy one after the other (the last one stays), when there are some
	busySeq   []string
	busyCalls int
	// fallback answers the commands that are not in cmds
	fallback func(argv []string) (osreadiness.Output, error, bool)
	busyErr  error
	euid     int
	arch     string
	now      time.Time
	step     time.Duration
}

type fakeCmd struct {
	text string
	exit int
	err  error
}

func newFiles() *fakeFiles {
	return &fakeFiles{
		files: map[string]string{}, devices: map[string]bool{}, links: map[string]string{}, dirs: map[string]bool{},
		fsErr: map[string]error{}, readErr: map[string]error{}, free: map[string]uint64{}, cmds: map[string]fakeCmd{},
		arch: "amd64", now: time.Date(2026, 10, 12, 9, 30, 0, 0, time.UTC),
	}
}

func (f *fakeFiles) resolve(p string) string {
	for i := 0; i < 8; i++ {
		target, ok := f.links[p]
		if !ok {
			break
		}
		if !strings.HasPrefix(target, "/") {
			target = path.Join(path.Dir(p), target)
		}
		p = target
	}
	return p
}

func (f *fakeFiles) isDir(p string) bool {
	if f.dirs[p] {
		return true
	}
	prefix := strings.TrimSuffix(p, "/") + "/"
	for name := range f.files {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	for name := range f.links {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	for name := range f.devices {
		if strings.HasPrefix(name, prefix) {
			return true
		}
	}
	return false
}

func nope(op, p string) error { return &fs.PathError{Op: op, Path: p, Err: fs.ErrNotExist} }

func (f *fakeFiles) ReadFile(_ context.Context, p string) ([]byte, error) {
	if err := f.fsErr[p]; err != nil {
		return nil, err
	}
	if err := f.readErr[p]; err != nil {
		return nil, err
	}
	p = f.resolve(p)
	if c, ok := f.files[p]; ok {
		if len(c) > osreadiness.MaxFileBytes {
			return nil, osreadiness.ErrTooLarge
		}
		return []byte(c), nil
	}
	if f.devices[p] || f.isDir(p) {
		return nil, &fs.PathError{Op: "read", Path: p, Err: errors.New("not a regular file")}
	}
	return nil, nope("open", p)
}

func (f *fakeFiles) ReadHead(ctx context.Context, p string, max int) ([]byte, error) {
	b, err := f.ReadFile(ctx, p)
	if err == nil && len(b) > max {
		b = b[:max]
	}
	return b, err
}

func (f *fakeFiles) ReadDir(_ context.Context, p string) ([]string, error) {
	if err := f.fsErr[p]; err != nil {
		return nil, err
	}
	if !f.isDir(p) {
		return nil, nope("open", p)
	}
	prefix := strings.TrimSuffix(p, "/") + "/"
	seen := map[string]bool{}
	for _, m := range []map[string]string{f.files, f.links} {
		for name := range m {
			if rest, ok := strings.CutPrefix(name, prefix); ok {
				first, _, _ := strings.Cut(rest, "/")
				seen[first] = true
			}
		}
	}
	for name := range f.devices {
		if rest, ok := strings.CutPrefix(name, prefix); ok {
			first, _, _ := strings.Cut(rest, "/")
			seen[first] = true
		}
	}
	names := []string{}
	for n := range seen {
		names = append(names, n)
	}
	slices.Sort(names)
	return names, nil
}

func (f *fakeFiles) stat(p string, follow bool) (osreadiness.FileInfo, error) {
	if err := f.fsErr[p]; err != nil {
		return osreadiness.FileInfo{}, err
	}
	if _, ok := f.links[p]; ok && !follow {
		return osreadiness.FileInfo{Mode: fs.ModeSymlink | 0o777}, nil
	}
	p = f.resolve(p)
	switch {
	case f.devices[p]:
		return osreadiness.FileInfo{Mode: fs.ModeDevice | fs.ModeCharDevice | 0o666}, nil
	case func() bool { _, ok := f.files[p]; return ok }():
		return osreadiness.FileInfo{Size: int64(len(f.files[p])), Mode: 0o644}, nil
	case f.isDir(p):
		return osreadiness.FileInfo{Mode: fs.ModeDir | 0o755}, nil
	}
	return osreadiness.FileInfo{}, nope("stat", p)
}

func (f *fakeFiles) Stat(_ context.Context, p string) (osreadiness.FileInfo, error) {
	return f.stat(p, true)
}
func (f *fakeFiles) Lstat(_ context.Context, p string) (osreadiness.FileInfo, error) {
	return f.stat(p, false)
}

func (f *fakeFiles) Readlink(_ context.Context, p string) (string, error) {
	if t, ok := f.links[p]; ok {
		return t, nil
	}
	return "", nope("readlink", p)
}

func key(argv ...string) string { return strings.Join(argv, "\x1f") }

func (f *fakeFiles) on(text string, exit int, argv ...string) {
	f.cmds[key(argv...)] = fakeCmd{text: text, exit: exit}
}

func (f *fakeFiles) Run(_ context.Context, argv ...string) (osreadiness.Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ran = append(f.ran, slices.Clone(argv))
	c, ok := f.cmds[key(argv...)]
	if !ok && f.fallback != nil {
		if out, err, handled := f.fallback(argv); handled {
			return out, err
		}
	}
	if !ok {
		return osreadiness.Output{}, osreadiness.ErrToolMissing
	}
	if c.err != nil {
		return osreadiness.Output{}, c.err
	}
	return osreadiness.Output{Text: c.text, ExitCode: c.exit}, nil
}

func (f *fakeFiles) FreeBytes(_ context.Context, p string) (uint64, error) {
	if v, ok := f.free[p]; ok {
		return v, nil
	}
	return 0, nope("statfs", p)
}

func (f *fakeFiles) MaintenanceBusy(context.Context) (string, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	busy := f.busy
	if len(f.busySeq) > 0 {
		busy = f.busySeq[min(f.busyCalls, len(f.busySeq)-1)]
		f.busyCalls++
	}
	return busy, busy != "", f.busyErr
}
func (f *fakeFiles) AppOperations(context.Context) ([]string, error) { return nil, nil }
func (f *fakeFiles) Now() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.now
	f.now = f.now.Add(f.step)
	return t
}
func (f *fakeFiles) EUID() int    { return f.euid }
func (f *fakeFiles) Arch() string { return f.arch }

// ---- the tests ---------------------------------------------------------------------------------------------------

func aptBox(fragments map[string]string) *fakeFiles {
	f := newFiles()
	f.dirs["/etc/apt/apt.conf.d"] = true
	for name, c := range fragments {
		f.files["/etc/apt/apt.conf.d/"+name] = c
	}
	return f
}

func names(list []TreeFile) []string {
	var out []string
	for _, f := range list {
		out = append(out, f.Path)
	}
	return out
}

// ---- the grammar ------------------------------------------------------------------------------------------------------

func TestTheGrammarKeepsWhatItUnderstandsAndWritesItAgainInOneForm(t *testing.T) {
	for _, tc := range []struct{ name, in, want string }{
		{"the proxy of a box", "Acquire::http::Proxy \"http://cache.example.lan:3142\";\nAcquire::Retries \"5\";\n",
			"Acquire::http::Proxy \"http://cache.example.lan:3142\";\nAcquire::Retries \"5\";\n"},
		{"comments and blank lines are dropped, the spacing is one", "// a comment\n\n\t// another\n  Acquire::Languages \t \"none\"  ;  \r\n\r\n",
			"Acquire::Languages \"none\";\n"},
		{"the name is kept as written, apt reads it in any case", "acquire::HTTP::proxy \"http://p:3128\";\nAPT::Install-Recommends \"false\";\napt::install-suggests \"0\";\n",
			"acquire::HTTP::proxy \"http://p:3128\";\nAPT::Install-Recommends \"false\";\napt::install-suggests \"0\";\n"},
		{"an empty value", "Acquire::http::Proxy \"\";\n", "Acquire::http::Proxy \"\";\n"},
		// the commonest proxy of a real box (squid-deb-proxy, apt-cacher): the proxy of one host, whose name has dots in it. Real
		// apt 2.2.4 reads it as such (TestRealAptReadsOnlyWhatTheSandboxKept)
		{"the proxy of a host", "Acquire::http::Proxy::deb.debian.org \"http://cache.example.lan:3142\";\nAcquire::http::Proxy::security.debian.org \"DIRECT\";\n",
			"Acquire::http::Proxy::deb.debian.org \"http://cache.example.lan:3142\";\nAcquire::http::Proxy::security.debian.org \"DIRECT\";\n"},
		{"the proxy of a host, https, in the case of the shouting", "ACQUIRE::HTTPS::PROXY::Download.Docker.COM \"http://p:3128\";\n", "ACQUIRE::HTTPS::PROXY::Download.Docker.COM \"http://p:3128\";\n"},
		{"the name of a host with an underscore, a dash, a digit and a final dot", "Acquire::http::Proxy::my_host-1.example.lan. \"http://p:3128\";\n", "Acquire::http::Proxy::my_host-1.example.lan. \"http://p:3128\";\n"},
		{"a single dot is a character of a host", "Acquire::ftp::Proxy::a.b \"1\";\n", "Acquire::ftp::Proxy::a.b \"1\";\n"},
		{"a name with digits and dashes", "Acquire::http::Dl-Limit \"50\";\nAcquire::By-Hash \"yes\";\nAcquire::ForceIPv4 \"1\";\n",
			"Acquire::http::Dl-Limit \"50\";\nAcquire::By-Hash \"yes\";\nAcquire::ForceIPv4 \"1\";\n"},
		{"what apt would take for a comment or a list is a value in quotes", "Acquire::http::Proxy \"a/*b\";\nAcquire::https::Proxy \"c*/d\";\nAcquire::ftp::Proxy \"http://x//y\";\nAcquire::Languages \"has # hash\";\nAcquire::Retries \"semi; colon { brace } #include x\";\n",
			"Acquire::http::Proxy \"a/*b\";\nAcquire::https::Proxy \"c*/d\";\nAcquire::ftp::Proxy \"http://x//y\";\nAcquire::Languages \"has # hash\";\nAcquire::Retries \"semi; colon { brace } #include x\";\n"},
		{"a comment may be any text, the setting is not", "// désactivé ‮\nAcquire::Languages \"none\";\n", "Acquire::Languages \"none\";\n"},
		{"nothing but comments", "// Acquire::Languages \"none\";\n// #include \"x\"\n", ""},
		{"nothing", "", ""},
		{"a value of the longest size", "Acquire::Languages \"" + strings.Repeat("a", 512) + "\";\n", "Acquire::Languages \"" + strings.Repeat("a", 512) + "\";\n"},
		{"the file of the largest size", "Acquire::Languages \"none\";\n// " + strings.Repeat("a", 64<<10-len("Acquire::Languages \"none\";\n// ")-1) + "\n",
			"Acquire::Languages \"none\";\n"},
	} {
		got, reason := canonicalAptConf([]byte(tc.in))
		if reason != "" || got != tc.want {
			t.Errorf("%s: %q, reason %q\nwant %q", tc.name, got, reason, tc.want)
		}
	}
}

func TestTheGrammarLeavesAFileOutWholeForTheFirstLineItDoesNotKnow(t *testing.T) {
	// the first four are the ones of a real run of apt 2.2.4 that a filter on the text let through
	for _, tc := range []struct{ name, in, reason string }{
		{"a hook whose name is spliced with a comment", "APT::Update::Post-In/**/voke-Success {\"touch /tmp/osp-canary-splice\";};\n", ConfNotSimple},
		{"an include", "#include \"/etc/osp-canary-include.conf\";\n", ConfHasDirective},
		{"the main file of apt set in a fragment", "Dir::Etc::main \"/etc/apt/apt.conf\";\n", ConfNotAllowedSetting},
		{"the cache of the packages set in a fragment", "Dir::Cache::pkgcache \"/var/cache/apt/pkgcache.bin\";\n", ConfNotAllowedSetting},
		{"a clear", "#clear APT::Update;\n", ConfHasDirective},
		{"a directive after a blank and a tab", "Acquire::Languages \"none\";\n \t#include \"x\"\n", ConfHasDirective},
		{"a hook as a plain statement", "APT::Update::Post-Invoke-Success \"touch /var/lib/apt/periodic/x\";\n", ConfNotAllowedSetting},
		{"a hook in the case of the shouting", "APT::UPDATE::PRE-INVOKE \"x\";\n", ConfNotAllowedSetting},
		{"a setting of dpkg", "DPkg::Pre-Install-Pkgs \"x\";\nDpkg::Options \"--force-confold\";\n", ConfNotAllowedSetting},
		{"a setting of APT that is not the two", "APT::Periodic::Update-Package-Lists \"1\";\n", ConfNotAllowedSetting},
		{"the two settings with something after them", "APT::Install-Recommends::Foo \"1\";\n", ConfNotAllowedSetting},
		{"the second of the two settings with something after it", "APT::Install-Suggests::Foo \"1\";\n", ConfNotAllowedSetting},
		{"the two settings with something in front", "XAPT::Install-Recommends \"1\";\n", ConfNotAllowedSetting},
		{"the second of the two settings with something in front", "XAPT::Install-Suggests \"1\";\n", ConfNotAllowedSetting},
		{"Acquire with something in front", "XAcquire::http::Proxy \"x\";\n", ConfNotAllowedSetting},
		{"Acquire in the middle of a name", "Foo::Acquire::http::Proxy \"x\";\n", ConfNotAllowedSetting},
		{"Acquire with nothing after it", "Acquire \"x\";\nAcquire:: \"x\";\n", ConfNotAllowedSetting},
		{"an Acquire name with a step in it", "Acquire::http/../Proxy \"x\";\n", ConfNotAllowedSetting},
		{"a segment that is only dots", "Acquire::http::Proxy::. \"x\";\n", ConfNotAllowedSetting},
		{"a segment that is two dots", "Acquire::http::Proxy::.. \"x\";\n", ConfNotAllowedSetting},
		{"a segment that is a row of dots, in the middle", "Acquire::http::...::Proxy \"x\";\n", ConfNotAllowedSetting},
		{"two dots in a segment", "Acquire::http::Proxy::deb..debian.org \"x\";\n", ConfNotAllowedSetting},
		{"two dots at the start of a segment", "Acquire::http::Proxy::..deb \"x\";\n", ConfNotAllowedSetting},
		{"two dots at the end of a segment", "Acquire::http::Proxy::deb.. \"x\";\n", ConfNotAllowedSetting},
		{"a segment with nothing in it", "Acquire::http::::Proxy \"x\";\n", ConfNotAllowedSetting},
		{"a name that ends with the separator", "Acquire::http::Proxy:: \"x\";\n", ConfNotAllowedSetting},
		{"a single colon in a segment", "Acquire::http::Proxy::a:b \"x\";\n", ConfNotAllowedSetting},
		{"three colons", "Acquire::http:::Proxy \"x\";\n", ConfNotAllowedSetting},
		{"a slash in a segment", "Acquire::http::Proxy::a/b \"x\";\n", ConfNotAllowedSetting},
		{"a letter that is not ASCII in a host", "Acquire::http::Proxy::déb.debian.org \"x\";\n", ConfNotAllowedSetting},
		{"a letter that folds to an ASCII one", "Acquire::http::Proxy::K.example \"x\";\n", ConfNotAllowedSetting},
		{"one of the two settings, with a letter that folds to an ASCII one", "APT::Install-Recommendſ \"1\";\n", ConfNotAllowedSetting},
		{"a dot in one of the two settings", "APT::Install-Recommends.x \"1\";\n", ConfNotAllowedSetting},
		{"a dot in place of the separator", "Acquire.http::Proxy \"x\";\n", ConfNotAllowedSetting},
		{"a program, for a host", "Acquire::http::Proxy-Auto-Detect::deb.debian.org \"/x\";\n", ConfProxyAutoDetect},
		{"a program, in the name of a host", "Acquire::http::Proxy::deb.Proxy-Auto-Detect.org \"/x\";\n", ConfProxyAutoDetect},
		{"a program, in the other spelling, for a host", "Acquire::https::ProxyAutoDetect::a.b \"/x\";\n", ConfProxyAutoDetect},
		{"a proxy that is a program", "Acquire::http::Proxy-Auto-Detect \"/usr/local/bin/find-proxy\";\n", ConfProxyAutoDetect},
		{"a proxy that is a program, in the other spelling", "Acquire::https::ProxyAutoDetect \"/x\";\n", ConfProxyAutoDetect},
		{"a proxy that is a program, shouting", "ACQUIRE::HTTP::PROXY-AUTO-DETECT \"/x\";\n", ConfProxyAutoDetect},
		{"a proxy that is a program, for a host", "Acquire::http::Proxy::Proxy-Auto-Detect \"/x\";\n", ConfProxyAutoDetect},
		// an open "Acquire::" would let these through: gpgv is run by apt as _apt, and
		// "--log-file=..." makes it write that file wherever it says, in the box's own /var/lib/apt/lists/partial for one
		{"gpgv's options, one that writes a file in the box's lists", "Acquire::gpgv::Options::0 \"--log-file=/var/lib/apt/lists/partial/osp-canary\";\n", ConfNotAllowedSetting},
		{"gpgv's options, one that writes a file in /var/tmp", "Acquire::gpgv::Options::1 \"--log-file=/var/tmp/osp-canary-x\";\n", ConfNotAllowedSetting},
		{"gpgv's options, plain", "Acquire::gpgv::Options \"--log-file=/var/tmp/x\";\n", ConfNotAllowedSetting},
		{"gpgv's options, in the case of the shouting", "ACQUIRE::GPGV::OPTIONS::0 \"--log-file=/x\";\n", ConfNotAllowedSetting},
		{"gpgv itself", "Acquire::gpgv \"x\";\n", ConfNotAllowedSetting},
		{"the proxy in a brace block", "Acquire {\n  http { Proxy \"http://p:3128\"; };\n};\n", ConfNotSimple},
		{"a list", "APT::NeverAutoRemove { \"^linux-.*\"; };\n", ConfNotSimple},
		{"a comment between /* and */", "/* Acquire::Languages \"none\"; */\n", ConfNotSimple},
		{"a statement with a comment after it", "Acquire::Languages \"none\"; // only English\n", ConfNotSimple},
		{"two statements on a line", "Acquire::A \"x\"; Acquire::B \"y\";\n", ConfNotSimple},
		{"a statement with no semicolon", "Acquire::Languages \"none\"\n", ConfNotSimple},
		{"a statement with no space", "Acquire::Languages\"none\";\n", ConfNotSimple},
		{"a statement with no quote", "Acquire::Languages none;\n", ConfNotSimple},
		{"a quote that is not closed", "Acquire::Languages \"none;\n", ConfNotSimple},
		{"a value over several lines", "Acquire::Languages \"no\nne\";\n", ConfNotSimple},
		{"a backslash", "Acquire::Languages \"a\\\"b\";\n", ConfNotSimple},
		{"a backslash that escapes nothing", "Acquire::Languages \"a\b\";\n", ConfNotSimple},
		{"a backslash that ends the value", "Acquire::Languages \"a\\\";\n", ConfNotSimple},
		{"a quote in the value", "Acquire::Languages \"a\"b\";\n", ConfNotSimple},
		{"a value that is not ASCII", "Acquire::Languages \"déjà\";\n", ConfNotSimple},
		{"a tab in the value", "Acquire::Languages \"a\tb\";\n", ConfNotSimple},
		{"a value too long", "Acquire::Languages \"" + strings.Repeat("a", 513) + "\";\n", ConfNotSimple},
		{"a NUL", "Acquire::Languages \"none\";\x00\n", ConfNotSimple},
		{"a control character in a comment", "// a\x01b\nAcquire::Languages \"none\";\n", ConfNotSimple},
		{"a delete", "// a\x7fb\n", ConfNotSimple},
		{"not UTF-8", "// \xff\xfe\n", ConfNotSimple},
		{"a file too large", "Acquire::Languages \"none\";\n// " + strings.Repeat("a", 64<<10) + "\n", ConfTooLarge},
		{"a file one byte too large", "Acquire::Languages \"none\";\n// " + strings.Repeat("a", 64<<10-len("Acquire::Languages \"none\";\n// ")) + "\n", ConfTooLarge},
		{"the first line decides: a statement it knows, a directive, a block", "Acquire::Languages \"x\";\n#include \"y\";\n/* z */\n", ConfHasDirective},
		{"the first line decides: a block, a directive", "/* z */\n#include \"y\";\n", ConfNotSimple},
	} {
		got, reason := canonicalAptConf([]byte(tc.in))
		if got != "" || reason != tc.reason {
			t.Errorf("%s: kept %q, reason %q, want %q", tc.name, got, reason, tc.reason)
		}
	}
}

func leftOut(file, reason string) AptConfLeftOut { return AptConfLeftOut{File: file, Reason: reason} }

// The allow-list, name by name and written out again here on purpose: the table of the code and this one are changed together,
// and by hand (the CI report keeps a python copy of the grammar, and its leg a canary for it).
func TestTheAllowListOfTheSandboxIsThisWholeListAndNoOther(t *testing.T) {
	// the table itself, method by method and entry by entry: an entry that is added to it, or taken from it, is an edit of this test too (a name that is not in the list
	// of refusals below would pass the checks by name, and the table of the code would have it)
	httpReads := []string{"Proxy", "Timeout", "Pipeline-Depth", "Dl-Limit", "Max-Age", "No-Cache", "AllowRedirect"}
	// the ftp method keeps Proxy and Timeout: what a box really sets. Its binary reads more (the strings of /usr/lib/apt/methods/ftp of apt 2.2.4 also hold Acquire::FTP::Passive,
	// Acquire::FTP::ForceExtended and Acquire::ftp::ProxyLogin), and those are refused below with the other settings that are not in the table
	ftpReads := []string{"Proxy", "Timeout"}
	if want := map[string][]string{"http": httpReads, "https": httpReads, "ftp": ftpReads}; !reflect.DeepEqual(sandboxAptSettings.PerMethod, want) {
		t.Errorf("the settings of the methods are %v, want %v", sandboxAptSettings.PerMethod, want)
	}
	if want := []string{"Proxy"}; !slices.Equal(sandboxAptSettings.PerHost, want) {
		t.Errorf("the settings that a host has of its own are %v, want %v", sandboxAptSettings.PerHost, want)
	}
	if want := []string{"Retries", "Languages", "PDiffs", "By-Hash", "Check-Valid-Until", "ForceIPv4", "ForceIPv6", "Queue-Mode"}; !slices.Equal(sandboxAptSettings.Top, want) {
		t.Errorf("the settings of Acquire:: itself are %v, want %v", sandboxAptSettings.Top, want)
	}
	if want := []string{"APT::Install-Recommends", "APT::Install-Suggests", "APT::Default-Release"}; !slices.Equal(sandboxAptSettings.Other, want) {
		t.Errorf("the other settings are %v, want %v", sandboxAptSettings.Other, want)
	}
	var kept []string
	for m, reads := range map[string][]string{"http": httpReads, "https": httpReads, "ftp": ftpReads} {
		for _, s := range reads {
			kept = append(kept, "Acquire::"+m+"::"+s)
		}
		kept = append(kept, "Acquire::"+m+"::Proxy::deb.debian.org", "Acquire::"+m+"::Proxy::my_host-1.example.lan.")
	}
	for _, s := range []string{"Retries", "Languages", "PDiffs", "By-Hash", "Check-Valid-Until", "ForceIPv4", "ForceIPv6", "Queue-Mode"} {
		kept = append(kept, "Acquire::"+s)
	}
	kept = append(kept, "APT::Install-Recommends", "APT::Install-Suggests", "APT::Default-Release")
	for _, name := range kept {
		for _, spelling := range []string{name, strings.ToLower(name), strings.ToUpper(name)} {
			if !keptAptName(spelling) {
				t.Errorf("%s is not kept", spelling)
			}
			if got, reason := canonicalAptConf([]byte(spelling + " \"1\";\n")); got != spelling+" \"1\";\n" || reason != "" {
				t.Errorf("%s: %q, reason %q", spelling, got, reason)
			}
		}
	}
	// what a box may not say, and why it is dangerous when it is the open "Acquire::" that lets it through: an option or a
	// program that apt hands to a helper it runs (gpgv, the compressors, the methods), a URL that apt trusts, a security
	// check that is put out. None of them is a thing that a box needs for the sandbox to make its plan.
	for _, name := range []string{
		"Acquire::gpgv::Options", "Acquire::gpgv::Options::0", "Acquire::gpgv::Options::", "Acquire::gpgv::Method", "Acquire::gpgv::Cmd", "Acquire::gpgv",
		"Acquire::http::Options", "Acquire::https::Options::0", "Acquire::ftp::Options", "Acquire::http::Method", "Acquire::http::Cmd", "Acquire::http::Command",
		"Acquire::http::Sandbox", "Acquire::Sandbox", "Acquire::Sandbox::User", "Acquire::Changelogs::URI::Origin::Debian", "Acquire::Changelogs::AlwaysOnline",
		"Acquire::CompressionTypes::xz", "Acquire::CompressionTypes::Order", "Acquire::CompressionTypes::xz::Comp-Type", "Acquire::CompressionTypes",
		"Acquire::http::Proxy-Auto-Detect", "Acquire::Auto-Detect", "Acquire::cdrom::mount", "Acquire::AllowInsecureRepositories", "Acquire::AllowWeakRepositories",
		"Acquire::https::Verify-Peer", "Acquire::https::CaInfo", "Acquire::https::SslForceVersion", "Acquire::http::User-Agent", "Acquire::http::Referer",
		"Acquire::IndexTargets::deb::Packages::MetaKey", "Acquire::Check-Date", "Acquire::Max-ValidTime", "Acquire::Min-ValidTime", "Acquire::Source-Symlinks",
		"Acquire::tor::Proxy", "Acquire::mirror::Proxy", "Acquire::file::Proxy", "Acquire::Proxy", "Acquire::Timeout", "Acquire::http", "Acquire",
		// what the ftp method reads and a box rarely sets: its whole file is left out as any setting that is not in the table
		"Acquire::ftp::Passive", "Acquire::FTP::Passive", "Acquire::FTP::ForceExtended", "Acquire::ftp::ProxyLogin", "Acquire::ftp::Passive::deb.debian.org",
		// what the ftp method does not read (the settings of the http method): a setting that does nothing is not kept for it
		"Acquire::ftp::Pipeline-Depth", "Acquire::ftp::Dl-Limit", "Acquire::ftp::Max-Age", "Acquire::ftp::No-Cache", "Acquire::ftp::AllowRedirect", "Acquire::FTP::ALLOWREDIRECT",
		// what a method has, and its neighbour's per-host form: the per-host form is the proxy's alone, and has one host
		"Acquire::http::Timeout::deb.debian.org", "Acquire::http::Dl-Limit::deb.debian.org", "Acquire::http::Proxy::deb.debian.org::Options", "Acquire::http::Proxy::a::b",
		"Acquire::ftp::Timeout::deb.debian.org", "Acquire::ftp::Dl-Limit::deb.debian.org",
		// the top ones have nothing under them
		"Acquire::Retries::0", "Acquire::Languages::en", "Acquire::Languages::", "Acquire::PDiffs::Foo",
		// a method that is not one of the three
		"Acquire::HTTPS2::Proxy", "Acquire::httpx::Timeout", "Acquire::ssh::Options",
		"APT::Default-Release::Foo", "APT::Default-Release::", "XAPT::Default-Release", "APT::Default-Releases", "APT::Default-Release.x", "APT::Default-Release::0",
		"APT::Update::Pre-Invoke", "APT::Install-Recommends::Foo", "APT::Install-Recommends::", "Dir::Etc::main", "Dir::Log::Planner", "DPkg::Pre-Invoke", "APT::Sandbox::User",
	} {
		if keptAptName(name) {
			t.Errorf("%s is kept", name)
		}
		want := ConfNotAllowedSetting
		if strings.Contains(strings.ToLower(name), "proxy-auto-detect") {
			want = ConfProxyAutoDetect
		}
		if got, reason := canonicalAptConf([]byte(name + " \"x\";\n")); got != "" || reason != want {
			t.Errorf("%s: kept %q, reason %q, want %q", name, got, reason, want)
		}
	}
}

func TestTheNamesOfTheFragmentsThatAptReads(t *testing.T) {
	for name, want := range map[string]bool{
		"99proxy": true, "10named.conf": true, "99a:b.conf": true, "99c:d": true, "a:b": true, "01-x_y": true, "two.dots.conf": true,
		".hidden": false, ".conf": false, "": false, "old.dpkg-old": false, "backup.conf.save": false, "with space": false, "a/b": false,
		"tilde~": false, "ends-with-a-dot.": false, "a.b": false, "README.txt": false, "99a;b": false, "99a:b.save": false,
	} {
		if got := aptFragmentName(name); got != want {
			t.Errorf("aptFragmentName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestTheFilesOfTheBoxAreKeptInTheirCanonicalFormOrLeftOutAndNamed(t *testing.T) {
	f := aptBox(map[string]string{
		"15update-stamp":       "APT::Update::Post-Invoke-Success {\n\t\"touch /var/lib/apt/periodic/update-success-stamp 2>/dev/null || true\";\n};\n",
		"20listchanges":        "DPkg::Pre-Install-Pkgs {\"/usr/bin/apt-listchanges --apt || test $? -lt 10\"; };\n",
		"docker-clean":         "DPkg::Post-Invoke { \"rm -f /var/cache/apt/archives/*.deb\"; };\n",
		"50unattended":         "// Dpkg::Options { \"--force-confold\"; };\nUnattended-Upgrade::Origins-Pattern {};\n",
		"98osp-canary-splice":  "APT::Update::Post-In/**/voke-Success {\"touch /var/lib/apt/periodic/osp-canary-splice\";};\n",
		"98osp-canary-include": "#include \"/etc/osp-canary-include.conf\";\n",
		"99proxy":              "Acquire::http::Proxy \"http://cache.example.lan:3142\";   // the office\nAcquire::Retries \"5\";\n",
		"99proxy-ok":           "// the office\nAcquire::http::Proxy   \"http://cache.example.lan:3142\" ;\nAcquire::Retries \"5\";\n",
		"99proxy-host":         "// the office, host by host\nAcquire::http::Proxy::deb.debian.org   \"http://cache.example.lan:3142\" ;\nAcquire::http::Proxy::security.debian.org \"DIRECT\";\n",
		"99proxy-dots":         "Acquire::http::Proxy::deb..debian.org \"http://cache.example.lan:3142\";\n",
		"20auto-upgrades":      "APT::Periodic::Update-Package-Lists \"1\";\nAPT::Periodic::Unattended-Upgrade \"1\";\n",
		"01autoremove":         "APT::NeverAutoRemove { \"^firmware-linux.*\"; };\n",
		"10named.conf":         "Acquire::Languages \"none\";\n",
		"only-comments":        "// nothing here\n",
		"empty":                "",
		"two.dots.conf":        "Acquire::Languages \"none\";\n",
		"normal-with_under99":  "APT::Install-Recommends \"0\";\n",
		"detect":               "Acquire::http::Proxy-Auto-Detect \"/usr/bin/x\";\n",
		"90gpgv":               "Acquire::gpgv::Options::0 \"--log-file=/var/lib/apt/lists/partial/osp-canary\";\n",
		// a colon is in a name that apt reads (GetListOfFilesInDir lets it in), with the extension or without it
		"99a:b.conf": "Acquire::Languages \"none\";\n",
		"99c:d":      "Acquire::Retries \"2\";\n",
		// the names that apt does not read
		"old.dpkg-old":     "Dir::Etc::main \"x\";\n",
		".hidden":          "Dir::Etc::main \"x\";\n",
		".hidden.conf":     "Dir::Etc::main \"x\";\n",
		".conf":            "Dir::Etc::main \"x\";\n",
		"with space":       "Dir::Etc::main \"x\";\n",
		"backup.conf.save": "Dir::Etc::main \"x\";\n",
		"README.txt":       "text",
		"tilde~":           "x",
		"ends-with-a-dot.": "x",
		"a.b":              "x",
	})
	f.devices["/etc/apt/apt.conf.d/70debconf-off"] = true
	f.links["/etc/apt/apt.conf.d/80link"] = "/usr/share/x/conf"
	f.files["/usr/share/x/conf"] = "Acquire::Languages \"none\";\n"
	f.links["/etc/apt/apt.conf.d/81null"] = "/dev/null"
	f.devices["/dev/null"] = true
	// a directory, which apt passes over without a word: there is nothing to name
	f.files["/etc/apt/apt.conf.d/90dir/inside"] = "Acquire::Languages \"none\";\n"
	f.files["/etc/apt/apt.conf.d/97big"] = "Acquire::Languages \"none\";\n// " + strings.Repeat("a", 64<<10)
	f.files["/etc/apt/apt.conf.d/97huge"] = strings.Repeat("a", osreadiness.MaxFileBytes+1)
	cfg, err := ReadAptConfig(context.Background(), f)
	if err != nil {
		t.Fatal(err)
	}
	wantKept := map[string]string{
		"apt.conf.d/10named.conf":        "Acquire::Languages \"none\";\n",
		"apt.conf.d/99a:b.conf":          "Acquire::Languages \"none\";\n",
		"apt.conf.d/99c:d":               "Acquire::Retries \"2\";\n",
		"apt.conf.d/99proxy-host":        "Acquire::http::Proxy::deb.debian.org \"http://cache.example.lan:3142\";\nAcquire::http::Proxy::security.debian.org \"DIRECT\";\n",
		"apt.conf.d/99proxy-ok":          "Acquire::http::Proxy \"http://cache.example.lan:3142\";\nAcquire::Retries \"5\";\n",
		"apt.conf.d/normal-with_under99": "APT::Install-Recommends \"0\";\n",
		"apt.conf.d/two.dots.conf":       "Acquire::Languages \"none\";\n",
	}
	if len(cfg.Fragments) != len(wantKept) {
		t.Errorf("kept = %v", names(cfg.Fragments))
	}
	for _, fr := range cfg.Fragments {
		if want, ok := wantKept[fr.Path]; !ok || fr.Content != want {
			t.Errorf("kept %s = %q, want %q", fr.Path, fr.Content, want)
		}
	}
	wantOut := []AptConfLeftOut{
		leftOut("01autoremove", ConfNotSimple),
		leftOut("15update-stamp", ConfNotSimple),
		leftOut("20auto-upgrades", ConfNotAllowedSetting),
		leftOut("20listchanges", ConfNotSimple),
		leftOut("50unattended", ConfNotSimple),
		leftOut("70debconf-off", ConfNotRegular),
		leftOut("80link", ConfNotRegular),
		leftOut("81null", ConfNotRegular),
		leftOut("90gpgv", ConfNotAllowedSetting),
		leftOut("97big", ConfTooLarge),
		leftOut("97huge", ConfTooLarge),
		leftOut("98osp-canary-include", ConfHasDirective),
		leftOut("98osp-canary-splice", ConfNotSimple),
		leftOut("99proxy", ConfNotSimple),
		leftOut("99proxy-dots", ConfNotAllowedSetting),
		leftOut("detect", ConfProxyAutoDetect),
		leftOut("docker-clean", ConfNotSimple),
	}
	if !slices.Equal(cfg.LeftOut, wantOut) {
		t.Errorf("left out:\n got %v\nwant %v", cfg.LeftOut, wantOut)
	}
	if cfg.HasMain {
		t.Error("an apt.conf that the box has not")
	}
}

func TestAptDotConfIsReadAsTheFragmentsAre(t *testing.T) {
	read := func(f *fakeFiles) AptConfig {
		t.Helper()
		cfg, err := ReadAptConfig(context.Background(), f)
		if err != nil {
			t.Fatal(err)
		}
		return cfg
	}
	f := aptBox(nil)
	f.files["/etc/apt/apt.conf"] = "// the proxy\nAcquire::http::Proxy   \"http://p:3128\";\n"
	if cfg := read(f); !cfg.HasMain || cfg.Main != "Acquire::http::Proxy \"http://p:3128\";\n" || len(cfg.LeftOut) != 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
	f.files["/etc/apt/apt.conf"] = "DPkg::Post-Invoke { \"x\"; };\n"
	if cfg := read(f); cfg.HasMain || cfg.Main != "" || !slices.Equal(cfg.LeftOut, []AptConfLeftOut{leftOut("apt.conf", ConfNotSimple)}) {
		t.Fatalf("cfg = %+v", cfg)
	}
	f.files["/etc/apt/apt.conf"] = "#include \"/etc/apt/other\"\n"
	if cfg := read(f); cfg.HasMain || !slices.Equal(cfg.LeftOut, []AptConfLeftOut{leftOut("apt.conf", ConfHasDirective)}) {
		t.Fatalf("cfg = %+v", cfg)
	}
	f.files["/etc/apt/apt.conf"] = "// only a comment\n"
	if cfg := read(f); cfg.HasMain || len(cfg.LeftOut) != 0 {
		t.Fatalf("cfg = %+v", cfg)
	}
	delete(f.files, "/etc/apt/apt.conf")
	f.links["/etc/apt/apt.conf"] = "/etc/apt/real.conf"
	f.files["/etc/apt/real.conf"] = "Acquire::Languages \"none\";\n"
	if cfg := read(f); cfg.HasMain || !slices.Equal(cfg.LeftOut, []AptConfLeftOut{leftOut("apt.conf", ConfNotRegular)}) {
		t.Fatalf("a link: cfg = %+v", cfg)
	}
	delete(f.links, "/etc/apt/apt.conf")
	f.files["/etc/apt/apt.conf"] = strings.Repeat("a", osreadiness.MaxFileBytes+1)
	if cfg := read(f); cfg.HasMain || !slices.Equal(cfg.LeftOut, []AptConfLeftOut{leftOut("apt.conf", ConfTooLarge)}) {
		t.Fatalf("too large for the probe: cfg = %+v", cfg)
	}
	f.files["/etc/apt/apt.conf"] = "Acquire::Languages \"none\";\n// " + strings.Repeat("a", 64<<10)
	if cfg := read(f); cfg.HasMain || !slices.Equal(cfg.LeftOut, []AptConfLeftOut{leftOut("apt.conf", ConfTooLarge)}) {
		t.Fatalf("too large for the sandbox: cfg = %+v", cfg)
	}
	delete(f.files, "/etc/apt/apt.conf")
	if cfg := read(f); cfg.HasMain || len(cfg.LeftOut) != 0 {
		t.Fatalf("no apt.conf: cfg = %+v", cfg)
	}
	// a fragment called apt.conf is not apt.conf: both are named, in a fixed order
	f.files["/etc/apt/apt.conf.d/apt.conf"] = "Dir::Etc::main \"x\";\n"
	f.files["/etc/apt/apt.conf"] = "#clear APT::Update;\n"
	want := []AptConfLeftOut{leftOut("apt.conf", ConfHasDirective), leftOut("apt.conf", ConfNotAllowedSetting)}
	if cfg := read(f); !slices.Equal(cfg.LeftOut, want) {
		t.Fatalf("both called apt.conf: cfg = %+v", cfg)
	}
	// the main file is read after the fragments and is listed among them by its name
	f.files["/etc/apt/apt.conf.d/zz"] = "Dir::Etc::main \"x\";\n"
	delete(f.files, "/etc/apt/apt.conf.d/apt.conf")
	want = []AptConfLeftOut{leftOut("apt.conf", ConfHasDirective), leftOut("zz", ConfNotAllowedSetting)}
	if cfg := read(f); !slices.Equal(cfg.LeftOut, want) {
		t.Fatalf("the main file after a fragment: cfg = %+v", cfg)
	}
}

func TestAFragmentThatIsGoneOrCannotBeReadIsNotAKeptOne(t *testing.T) {
	f := aptBox(map[string]string{"10a": "Acquire::Languages \"none\";\n", "20b": "Acquire::Languages \"none\";\n"})
	f.fsErr["/etc/apt/apt.conf.d/10a"] = &fs.PathError{Op: "stat", Path: "x", Err: fs.ErrNotExist}
	cfg, err := ReadAptConfig(context.Background(), f)
	if err != nil || !slices.Equal(names(cfg.Fragments), []string{"apt.conf.d/20b"}) || len(cfg.LeftOut) != 0 {
		t.Fatalf("one that went: cfg = %+v, err = %v", cfg, err)
	}
	// an error that is not "it is not there" is an error: nothing is guessed about what the sandbox would have got
	for _, p := range []string{"/etc/apt/apt.conf.d", "/etc/apt/apt.conf.d/20b", "/etc/apt/apt.conf"} {
		for _, readOnly := range []bool{false, true} {
			if readOnly && p == "/etc/apt/apt.conf.d" {
				continue
			}
			g := aptBox(map[string]string{"20b": "Acquire::Languages \"none\";\n"})
			g.files["/etc/apt/apt.conf"] = "Acquire::Languages \"none\";\n"
			errs := g.fsErr
			if readOnly {
				errs = g.readErr
			}
			errs[p] = &fs.PathError{Op: "open", Path: p, Err: fs.ErrPermission}
			if _, err := ReadAptConfig(context.Background(), g); !errors.Is(err, fs.ErrPermission) {
				t.Errorf("%s (the read alone: %v): err = %v", p, readOnly, err)
			}
		}
	}
	// a file that went between its stat and its read is nothing too
	g := aptBox(map[string]string{"20b": "Acquire::Languages \"none\";\n"})
	g.files["/etc/apt/apt.conf"] = "Acquire::Languages \"none\";\n"
	g.readErr["/etc/apt/apt.conf.d/20b"] = &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist}
	g.readErr["/etc/apt/apt.conf"] = &fs.PathError{Op: "open", Path: "x", Err: fs.ErrNotExist}
	if cfg, err := ReadAptConfig(context.Background(), g); err != nil || len(cfg.Fragments) != 0 || cfg.HasMain || len(cfg.LeftOut) != 0 {
		t.Fatalf("gone before the read: cfg = %+v, err = %v", cfg, err)
	}
}

func TestAFragmentThatTheFirstHundredsDidNotLeaveRoomForIsLeftOutAndNamed(t *testing.T) {
	many := aptBox(nil)
	for i := 0; i < 205; i++ {
		many.files["/etc/apt/apt.conf.d/f"+strconv.Itoa(1000+i)] = "Acquire::Languages \"none\";\n"
	}
	cfg, err := ReadAptConfig(context.Background(), many)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Fragments) != 200 || len(cfg.LeftOut) != 5 || cfg.LeftOut[0] != leftOut("f1200", ConfTooLarge) || cfg.LeftOut[4] != leftOut("f1204", ConfTooLarge) {
		t.Errorf("kept %d, left out %v", len(cfg.Fragments), cfg.LeftOut)
	}
}

func TestNoAptConfDirectoryIsNoFragments(t *testing.T) {
	f := newFiles()
	cfg, err := ReadAptConfig(context.Background(), f)
	if err != nil || len(cfg.Fragments) != 0 || len(cfg.LeftOut) != 0 {
		t.Fatalf("cfg = %+v, err = %v", cfg, err)
	}
}

func TestTheFilesOfTheSandboxAreAllInsideItAndAptConfigIsThreeStatements(t *testing.T) {
	cfg := AptConfig{
		Fragments: []TreeFile{{Path: "apt.conf.d/99proxy", Content: "Acquire::Retries \"5\";\n"}},
		Main:      "Acquire::Retries \"7\";\n", HasMain: true,
	}
	tree := []TreeFile{{Path: "parts/x.list", Content: "x\n"}, {Path: "sources.list", Content: "y\n"}}
	files := SandboxFiles(testDir, tree, cfg)
	got := map[string]SandboxFile{}
	for _, f := range files {
		if !strings.HasPrefix(f.Path, testDir+"/") || strings.Contains(f.Path, "..") || path.Clean(f.Path) != f.Path {
			t.Errorf("a file outside the sandbox: %q", f.Path)
		}
		if _, dup := got[f.Path]; dup {
			t.Errorf("%s twice", f.Path)
		}
		got[f.Path] = f
	}
	wantDirs := []string{"/parts", "/apt.conf.d", "/lists/partial", "/cache/archives/partial"}
	for _, d := range wantDirs {
		f, ok := got[testDir+d]
		if !ok || f.Mode&fs.ModeDir == 0 || f.Mode.Perm() != 0o700 || f.Mode&fs.ModeSticky != 0 {
			t.Errorf("directory %s: %+v", d, f)
		}
	}
	// the one directory that anyone may write in, with the sticky bit: where the methods of apt, which run as another user, put
	// their temporary files (the environment names it)
	if f, ok := got[testDir+"/tmp"]; !ok || f.Mode != fs.ModeDir|fs.ModeSticky|0o777 {
		t.Errorf("directory /tmp: %+v", f)
	}
	wantConfig := "Dir::Etc::parts \"" + testDir + "/apt.conf.d\";\nDir::Etc::main \"" + testDir + "/apt.conf\";\nDir::Log::Planner \"\";\n"
	if c := string(got[testDir+"/apt-config"].Content); c != wantConfig {
		t.Errorf("apt-config = %q", c)
	}
	for path, want := range map[string]string{
		"/sources.list":       "y\n",
		"/parts/x.list":       "x\n",
		"/apt.conf.d/99proxy": "Acquire::Retries \"5\";\n",
		"/apt.conf":           "Acquire::Retries \"7\";\n",
		"/apt-config":         wantConfig,
	} {
		f, ok := got[testDir+path]
		if !ok || string(f.Content) != want || f.Mode.Perm() != 0o600 || f.Mode&fs.ModeDir != 0 {
			t.Errorf("file %s: %+v", path, f)
		}
	}
	// nothing else is written: the directories above, the temporary one, the four files of the plan, the config
	if len(files) != len(wantDirs)+1+5 {
		t.Errorf("%d files: %v", len(files), files)
	}
	// without apt.conf there is none in the sandbox
	cfg.HasMain, cfg.Main = false, ""
	for _, f := range SandboxFiles(testDir, tree, cfg) {
		if f.Path == testDir+"/apt.conf" {
			t.Error("an apt.conf that the box has not")
		}
	}
}
