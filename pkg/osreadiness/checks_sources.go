package osreadiness

import (
	"context"
	"regexp"
	"slices"
	"strings"
)

// The checks of the sources of apt. They read the files apt reads and say what they find; nothing
// is fetched, so whether the next release exists on each mirror is not said here (it is said to
// be checked when the plan is made: sources_network_checked_later).
func init() {
	register(
		Check{Code: "sources_cdrom", Group: GroupSources, Title: "No source is a disc or a local directory", Run: checkSourcesLocal},
		Check{Code: "sources_alias", Group: GroupSources, Title: "No source names a release by an alias", Run: checkSourcesAlias},
		Check{Code: "sources_pin", Group: GroupSources, Title: "No release is pinned", Run: checkSourcesPin},
		Check{Code: "sources_proposed", Group: GroupSources, Title: "No source is the proposed updates", Run: checkSourcesProposed},
		Check{Code: "sources_format", Group: GroupSources, Title: "Every source file can be read", Run: checkSourcesFormat},
		Check{Code: "sources_third_party", Group: GroupSources, Title: "The sources are Debian's or Ubuntu's", Run: checkSourcesThirdParty},
		Check{Code: "sources_backports", Group: GroupSources, Title: "No source is a backports archive", Run: checkSourcesBackports},
		Check{Code: "sources_firmware", Group: GroupSources, Title: "The firmware component is in the sources", Hops: debianHops, Run: checkSourcesFirmware},
		Check{Code: "sources_network_checked_later", Group: GroupSources, Title: "The mirrors and the security archive are checked when the plan is made", Run: checkSourcesLater},
	)
}

// filesOf are the files of the entries that match, once each, sorted.
func filesOf(entries []sourceEntry, match func(sourceEntry) bool) []string {
	files := []string{}
	for _, e := range entries {
		if match(e) && !slices.Contains(files, e.File) {
			files = append(files, e.File)
		}
	}
	slices.Sort(files)
	return files
}

// suitesOf are the suites of the entries that match, for the ones that say so, once each, sorted.
func suitesOf(entries []sourceEntry, match func(sourceEntry, string) bool) []string {
	suites := []string{}
	for _, e := range entries {
		for _, suite := range e.Suites {
			if match(e, suite) && !slices.Contains(suites, suite) {
				suites = append(suites, suite)
			}
		}
	}
	slices.Sort(suites)
	return suites
}

// unlessUnreadable is the Result of a check of the lines of the sources, or the pass of one that found
// nothing in the files it understood while a file was not: that file may be what it looks for, and
// sources_format is the check that says what is wrong with it.
func unlessUnreadable(set sourcesSet, found Result) Result {
	if found.Severity == OK && len(set.Unreadable) > 0 {
		return unknownWith(ReasonUnreadable)
	}
	return found
}

// sourcesProblem is the Result of a check that refuses because of some of the entries, or the
// pass of one that finds none.
func sourcesProblem(files, suites []string, title, detail string) Result {
	if len(files) == 0 {
		return pass()
	}
	data := D{"files": files}
	if len(suites) > 0 {
		data["suites"] = suites
	}
	return Result{Severity: Refuse, Title: title, Detail: detail, Data: data, Action: ActionReviewSources}
}

func checkSourcesLocal(ctx context.Context, p Probe) Result {
	set, err := sourcesOf(ctx, p)
	if err != nil {
		return unknown(err)
	}
	return unlessUnreadable(set, sourcesProblem(filesOf(set.Entries, sourceEntry.local), nil,
		"A source is a disc or a local directory",
		"A CD-ROM or a local directory is not an archive the next release can be fetched from: remove it from the sources, or put the network archive in its place."))
}

func checkSourcesAlias(ctx context.Context, p Probe) Result {
	set, err := sourcesOf(ctx, p)
	if err != nil {
		return unknown(err)
	}
	alias := func(e sourceEntry, suite string) bool { return e.distro() && isAlias(suite) }
	return unlessUnreadable(set, sourcesProblem(
		filesOf(set.Entries, func(e sourceEntry) bool { return e.distro() && slices.ContainsFunc(e.Suites, isAlias) }),
		suitesOf(set.Entries, alias),
		"A source names a release by an alias",
		"A name like stable or oldstable means the release that is current when apt reads it: the sources change release by themselves. They must name the release (bullseye, jammy) instead."))
}

func checkSourcesProposed(ctx context.Context, p Probe) Result {
	set, err := sourcesOf(ctx, p)
	if err != nil {
		return unknown(err)
	}
	proposed := func(e sourceEntry, suite string) bool { return e.distro() && isProposed(suite) }
	return unlessUnreadable(set, sourcesProblem(
		filesOf(set.Entries, func(e sourceEntry) bool { return e.distro() && slices.ContainsFunc(e.Suites, isProposed) }),
		suitesOf(set.Entries, proposed),
		"A source is the proposed updates",
		"The proposed updates are packages that are not ready for use: the upgrade does not go through them."))
}

func checkSourcesFormat(ctx context.Context, p Probe) Result {
	set, err := sourcesOf(ctx, p)
	if err != nil {
		return unknown(err)
	}
	return sourcesProblem(set.Unreadable, nil,
		"A source file cannot be read",
		"A .list file must have lines of the form 'deb [options] uri suite component', and a .sources file stanzas with Types, URIs and Suites. The upgrade rewrites the sources, and cannot rewrite a file it does not understand.")
}

func checkSourcesThirdParty(ctx context.Context, p Probe) Result {
	set, err := sourcesOf(ctx, p)
	if err != nil {
		return unknown(err)
	}
	other := func(e sourceEntry) bool { return !e.distro() && !e.local() }
	files := filesOf(set.Entries, other)
	if len(files) == 0 {
		return unlessUnreadable(set, pass())
	}
	data := D{"files": files}
	var hosts []string
	for _, e := range set.Entries {
		if host := e.host(); other(e) && host != "" && !slices.Contains(hosts, host) {
			hosts = append(hosts, host)
		}
	}
	slices.Sort(hosts)
	if len(hosts) > 0 {
		data["hosts"] = hosts
	}
	// Docker's is the one that is rewritten for the new release after the hop; the others stay off.
	if docker := filesOf(set.Entries, func(e sourceEntry) bool { return other(e) && e.host() == "download.docker.com" }); len(docker) > 0 {
		data["docker_files"] = docker
	}
	return Result{
		Severity: Warn, Title: "Some software comes from other sources than Debian or Ubuntu",
		Detail: "These sources are paused for the upgrade, since the next release may not exist on them. Docker's is put back, for the new release, after it. The others stay paused until you turn them on.",
		// no action fits: Docker's source is on every machine of ReCasaOS and the hop handles it, and checking
		// again never makes it go; the others stay paused until the owner turns them on
		Data: data, Action: ActionNone,
	}
}

func checkSourcesBackports(ctx context.Context, p Probe) Result {
	set, err := sourcesOf(ctx, p)
	if err != nil {
		return unknown(err)
	}
	files := filesOf(set.Entries, func(e sourceEntry) bool { return e.distro() && slices.ContainsFunc(e.Suites, isBackports) })
	if len(files) == 0 {
		return unlessUnreadable(set, pass())
	}
	return Result{
		Severity: Info, Title: "The backports lines will be dropped",
		Detail: "Backports are made for one release: the upgrade removes these lines, and the packages that came from them are upgraded with the rest.",
		Data:   D{"files": files}, Action: ActionNone,
	}
}

// firmwarePackage is a package of firmware or of the processor's microcode, which Debian 12 moved
// to its own component, non-free-firmware. firmware-linux-free is firmware that is free, and was in
// main already.
var firmwarePackage = regexp.MustCompile(`^(?:firmware-.+|.+-microcode)$`)

const freeFirmwarePackage = "firmware-linux-free"

// firmwarePatterns are what dpkg-query is asked for, to find them.
var firmwarePatterns = []string{"firmware-*", "*-microcode"}

func checkSourcesFirmware(ctx context.Context, p Probe) Result {
	set, err := sourcesOf(ctx, p)
	if err != nil {
		return unknown(err)
	}
	packages, err := installedPackages(ctx, p, firmwarePatterns...)
	if err != nil {
		return unknown(err)
	}
	firmware := []string{}
	for _, pkg := range packages {
		if firmwarePackage.MatchString(pkg.Name) && pkg.Name != freeFirmwarePackage {
			firmware = append(firmware, pkg.Name)
		}
	}
	firmware = names(firmware)

	// The lines of Debian's own archive that the hop adds the component to: where non-free is,
	// and, if firmware is installed, where main is.
	nonFree, lacking := false, false
	for _, e := range set.Entries {
		if !e.distro() || slices.Contains(e.Components, "non-free-firmware") {
			continue
		}
		switch {
		case slices.Contains(e.Components, "non-free"):
			nonFree, lacking = true, true
		case len(firmware) > 0 && slices.Contains(e.Components, "main"):
			lacking = true
		}
	}
	if !lacking {
		return unlessUnreadable(set, pass())
	}
	data := D{"non_free": nonFree}
	if len(firmware) > 0 {
		data["packages"] = firmware
	}
	return Result{
		Severity: Info, Title: "The non-free-firmware component will be added to the sources",
		Detail: "Debian 12 keeps firmware in a component of its own: the upgrade adds it where non-free is used, or where firmware or microcode is installed, so that it goes on being updated.",
		Data:   data, Action: ActionNone,
	}
}

func checkSourcesLater(context.Context, Probe) Result {
	return Result{
		Severity: Info, Title: "The mirrors and the security archive are checked when the plan is made",
		Detail: "Whether the next release exists on each mirror, and whether the security archive still has its files, needs the network: the plan step asks, and this report does not.",
		Action: ActionNone,
	}
}

// defaultReleasePattern is the Default-Release of "apt-config dump": apt's keys do not care for case, and
// the dump says a key as it was typed.
var defaultReleasePattern = regexp.MustCompile(`(?mi)^APT::Default-Release\s+"([^"]*)"\s*;`)

// prefName is a name apt reads in preferences.d, as it reads one in sources.list.d (see aptSourceName).
var prefName = regexp.MustCompile(`^[A-Za-z0-9_:-][A-Za-z0-9_:.-]*$`)

// prefFile says whether apt reads a file of /etc/apt/preferences.d: no extension, or .pref, and
// only the characters apt accepts in a name (a colon, and no dot at the start).
func prefFile(name string) bool {
	if !prefName.MatchString(name) {
		return false
	}
	return !strings.Contains(name, ".") || strings.HasSuffix(name, ".pref")
}

// pinsRelease says whether a preferences file has a "Pin: release" line: a pin that names a
// release (a=, n=, v=, c=, o=, l=). Ubuntu Pro's own pins, which name only its label (UbuntuESM,
// UbuntuESMApps), are not the owner's.
func pinsRelease(text string) bool {
	for _, line := range strings.Split(text, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), ":")
		if !ok || !strings.EqualFold(strings.TrimSpace(key), "pin") {
			continue
		}
		fields := strings.Fields(value)
		if len(fields) < 2 || !strings.EqualFold(fields[0], "release") {
			continue
		}
		selectors := strings.Split(strings.Join(fields[1:], ""), ",")
		if !slices.ContainsFunc(selectors, func(s string) bool {
			return !strings.HasPrefix(s, "o=UbuntuESM") && !strings.HasPrefix(s, "l=UbuntuESM")
		}) {
			continue
		}
		return true
	}
	return false
}

func checkSourcesPin(ctx context.Context, p Probe) Result {
	out, err := p.Run(ctx, "apt-config", "dump")
	if err != nil {
		return unknown(err)
	}
	if out.ExitCode != 0 {
		return unknownWith(ReasonFailed)
	}
	defaultRelease := ""
	if m := defaultReleasePattern.FindStringSubmatch(out.Text); m != nil {
		defaultRelease = m[1]
	}

	pinned := []string{}
	check := func(name, path string) error {
		data, found, err := readRegular(ctx, p, path)
		if err != nil {
			return err
		}
		if found && pinsRelease(string(data)) {
			pinned = append(pinned, name)
		}
		return nil
	}
	if err := check("preferences", "/etc/apt/preferences"); err != nil {
		return unknown(err)
	}
	entries, err := p.ReadDir(ctx, "/etc/apt/preferences.d")
	if err != nil && !isNotExist(err) {
		return unknown(err)
	}
	for _, name := range entries {
		if prefFile(name) {
			if err := check(name, "/etc/apt/preferences.d/"+name); err != nil {
				return unknown(err)
			}
		}
	}
	if defaultRelease == "" && len(pinned) == 0 {
		return pass()
	}
	data := D{"files": pinned}
	if token(defaultRelease, 64) != "" {
		data["default_release"] = defaultRelease
	} else if defaultRelease != "" {
		data["default_release_set"] = true
	}
	return Result{
		Severity: Refuse, Title: "A release is pinned",
		Detail: "A pin or a default release makes apt prefer one release over the others, and the upgrade between two releases does not work with it. It has to go for the upgrade.",
		Data:   data, Action: ActionReviewSources,
	}
}
