#!/bin/sh
# Makes the files of this directory: what the real apt says of the two commands that the plan of a module runs (ModuleSimArgv and
# ModuleEstimateArgv), for each of the cases the plan has to tell apart. Run it as root on a Linux host with a Debian chroot to spare
# (a COPY of it: it installs and holds throwaway packages in it).
#
#   sh capture.sh prepare <chroot>                      builds the throwaway packages in <chroot>/var/rvrepo
#   (serve that directory over http, e.g. "python3 -m http.server 8761" in it)
#   sh capture.sh run <chroot> <label> <url> <outdir>   points apt at <url>, installs the starting state, runs each case and writes
#                                                       <outdir>/<label>-<case>-{sim,estimate}.{txt,rc}; it fails if the estimate
#                                                       command changed the dpkg status, the history of apt or the planner log
#   sh capture.sh trivialonly <chroot> <label> <outdir> what the command of an earlier version (Trivial-Only) does to the same box
#   sh capture.sh marked <chroot> <label> <outdir>      what the commands do when apt's own list of automatically installed packages is not the sandbox's (the commands of an
#                                                       earlier version): the output for a package that is installed as a dependency, and whether the box's list was rewritten
#
# The files are the output of apt with 2>&1, in English, with the environment and the sandbox directory of the plan
# (/var/tmp/casaos-os-plan.4242). The throwaway packages are all named rvm-*; "rvmclean" is the one that a regular expression finds, and "rvm-autodep" is installed as a dependency
# would be (apt-mark auto), which apt marks as installed by hand when it is asked to install it.
set -e
here=$(cd "$(dirname "$0")" && pwd)
SB=/var/tmp/casaos-os-plan.4242

case "$1" in
prepare)
	C=$2
	cp "$0" "$C/root/capture.sh"
	chroot "$C" /bin/sh /root/capture.sh inside-prepare
	;;
run)
	C=$2; LABEL=$3; URL=$4; OUT=$5
	mkdir -p "$OUT"
	cp "$0" "$C/root/capture.sh"
	chroot "$C" /bin/sh /root/capture.sh inside-run "$LABEL" "$URL" /root/cap
	cp "$C"/root/cap/* "$OUT"/
	;;
marked)
	C=$2; LABEL=$3; OUT=$4
	mkdir -p "$OUT"
	cp "$0" "$C/root/capture.sh"
	chroot "$C" /bin/sh /root/capture.sh inside-marked "$LABEL" /root/cap
	cp "$C"/root/cap/* "$OUT"/
	;;
trivialonly)
	C=$2; LABEL=$3; OUT=$4
	cp "$0" "$C/root/capture.sh"
	chroot "$C" /bin/sh /root/capture.sh inside-trivialonly "$LABEL" /root/cap
	cp "$C"/root/cap/* "$OUT"/
	;;
inside-prepare)
	R=/var/rvrepo; L=/var/rvstate
	rm -rf $R /tmp/rvb $L; mkdir -p $R /tmp/rvb $L
	# mk name version extra-control-line destination installed-size-in-KiB
	mk() {
		d=/tmp/rvb/$1_$2; mkdir -p $d/DEBIAN
		kb=${5:-1}
		{ echo "Package: $1"; echo "Version: $2"; echo "Architecture: all"; echo "Installed-Size: $kb"; echo "Maintainer: t <t@example.org>"
		  echo "Description: $1 test package"; if [ -n "$3" ]; then printf '%s\n' "$3"; fi; } > $d/DEBIAN/control
		mkdir -p $d/usr/share/$1; echo "$1 $2" > $d/usr/share/$1/data
		if [ "$kb" -gt 4 ]; then head -c $((kb*1024)) /dev/urandom > $d/usr/share/$1/blob; fi
		dpkg-deb -b $d "$4/$1_$2_all.deb" >/dev/null
	}
	mk rvm-solo 1.0 "" $R 12                      # installs by itself
	mk rvm-main 1.0 "Depends: rvm-dep" $R 40      # needs a package that the box does not have
	mk rvm-dep 1.0 "" $R 300
	mk rvm-newer 1.0 "Conflicts: rvm-old" $R 6    # removes the installed rvm-old
	mk rvm-old 1.0 "" $L 80
	mk rvm-down 1.0 "" $R 20                      # the box has 2.0, a pin of 1001 asks for 1.0
	mk rvm-down 2.0 "" $L 50
	mk rvm-held 1.0 "" $L 5                       # held, and 2.0 is on offer
	mk rvm-held 2.0 "" $R 9
	mk rvm-lib 1.0 "" $L 5                        # held, and what rvm-needs asks for is 2.0
	mk rvm-lib 2.0 "" $R 5
	mk rvm-needs 1.0 "Depends: rvm-lib (>= 2)" $R 5
	mk rvm-bad 1.0 "Depends: rvm-nothere" $R 5
	mk rvm-done 1.0 "" $R 5                       # installed, and the newest
	cp $R/rvm-done_1.0_all.deb $L/
	mk rvm-autodep 1.0 "" $L 9                    # installed as a dependency of something that is gone
	mk rvmclean 1.0 "" $R 5                       # what "rvm+clean" and "rvm.lean" are read as regular expressions for
	mk rvm-prov 1.0 "Provides: rvm-virt" $R 5     # one provider of a virtual package
	mk rvm-prov2 1.0 "Provides: rvm-virt2" $R 5   # two
	mk rvm-prov3 1.0 "Provides: rvm-virt2" $R 5
	(cd $R && apt-ftparchive packages . > Packages && gzip -k -f Packages)
	echo "now serve $R over http"
	;;
inside-run)
	LABEL=$2; URL=$3; OUT=$4
	L=/var/rvstate
	mkdir -p /etc/apt/sources.list.d
	for f in /etc/apt/sources.list.d/*.list; do if [ -f "$f" ]; then mv "$f" "$f.off"; fi; done
	echo "deb [trusted=yes] $URL ./" > /etc/apt/sources.list.d/rv.list
	rm -f /var/lib/apt/lists/*Packages*
	apt-get update >/dev/null 2>&1
	dpkg -i $L/rvm-old_1.0_all.deb $L/rvm-down_2.0_all.deb $L/rvm-held_1.0_all.deb $L/rvm-lib_1.0_all.deb $L/rvm-done_1.0_all.deb $L/rvm-autodep_1.0_all.deb >/dev/null 2>&1
	apt-mark auto rvm-autodep >/dev/null
	printf 'Package: rvm-down\nPin: version 1.0\nPin-Priority: 1001\n' > /etc/apt/preferences.d/rvm
	apt-mark hold rvm-held rvm-lib >/dev/null
	rm -rf $OUT; mkdir -p $OUT
	# the sandbox as the plan makes it (SandboxFiles): the cache, the temporary directory, apt's own configuration file with no planner log
	rm -rf $SB; mkdir -p $SB/parts $SB/apt.conf.d $SB/lists/partial $SB/cache/archives/partial $SB/tmp; chmod 1777 $SB/tmp
	printf 'Dir::Etc::parts "%s/apt.conf.d";\nDir::Etc::main "%s/apt.conf";\nDir::Log::Planner "";\n' $SB $SB > $SB/apt-config
	: > $SB/extended_states
	state() { { dpkg -l | md5sum; md5sum < /var/lib/dpkg/status; md5sum < /var/lib/apt/extended_states; wc -c < /var/log/apt/history.log; ls /var/log/apt; } 2>&1; }
	apt_run() { # kind, then the arguments of apt-get
		kind=$1; shift
		env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C LANG=C DEBIAN_FRONTEND=noninteractive TMPDIR=$SB/tmp APT_CONFIG=$SB/apt-config \
			apt-get -o Dir::Cache=$SB/cache -o Debug::NoLocking=true -o Dir::Log::Planner= -o Dir::State::extended_states=$SB/extended_states "$@" > $OUT/$LABEL-$CASE-$kind.txt 2>&1 < /dev/null && rc=0 || rc=$?
		echo $rc > $OUT/$LABEL-$CASE-$kind.rc
	}
	# (the planner log may be there from the set-up above: the question is whether the commands write it)
	rm -f /var/log/apt/eipp.log.xz
	before=$(state)
	while read CASE NAMES; do
		[ -n "$CASE" ] || continue
		apt_run sim -s install --no-install-recommends $NAMES
		apt_run estimate --print-uris install --no-install-recommends $NAMES
	done <<'CASES'
trivial rvm-solo
nontrivial rvm-main
removal rvm-newer
downgrade rvm-down
hold rvm-held
nothing rvm-done
unmet-held rvm-needs
unmet-missing rvm-bad
regex rvm.lean
regexplus rvm+clean
virtual-one rvm-virt
virtual-two rvm-virt2
unknown rvm-unknown
mixed rvm-solo rvm-done
auto rvm-autodep
CASES
	after=$(state)
	if [ "$before" != "$after" ] || [ -e /var/log/apt/eipp.log.xz ]; then
		echo "THE COMMANDS CHANGED THE BOX:"; echo "$before"; echo "--- after"; echo "$after"; ls -l /var/log/apt; exit 1
	fi
	dpkg -l 'rvm*' > $OUT/$LABEL-dpkg-l-after.txt
	echo "unchanged: dpkg status, dpkg -l, apt's list of automatically installed packages, history.log, no planner log"
	;;
inside-marked)
	LABEL=$2; OUT=$3
	mkdir -p $OUT
	rm -rf $SB; mkdir -p $SB/parts $SB/apt.conf.d $SB/lists/partial $SB/cache/archives/partial $SB/tmp; chmod 1777 $SB/tmp
	printf 'Dir::Etc::parts "%s/apt.conf.d";\nDir::Etc::main "%s/apt.conf";\nDir::Log::Planner "";\n' $SB $SB > $SB/apt-config
	cp -p /var/lib/apt/extended_states $OUT/extended_states.before
	before=$(md5sum < /var/lib/apt/extended_states)
	for kind in sim estimate; do
		if [ $kind = sim ]; then verb="-s install"; else verb="--print-uris install"; fi
		env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C LANG=C DEBIAN_FRONTEND=noninteractive TMPDIR=$SB/tmp APT_CONFIG=$SB/apt-config \
			apt-get -o Dir::Cache=$SB/cache -o Debug::NoLocking=true -o Dir::Log::Planner= $verb --no-install-recommends rvm-autodep > $OUT/$LABEL-auto-marked-$kind.txt 2>&1 < /dev/null && rc=0 || rc=$?
		echo $rc > $OUT/$LABEL-auto-marked-$kind.rc
	done
	if [ "$before" = "$(md5sum < /var/lib/apt/extended_states)" ]; then echo unchanged; else echo rewritten; fi > $OUT/$LABEL-auto-marked-extended-states.txt
	cp -p $OUT/extended_states.before /var/lib/apt/extended_states; rm -f $OUT/extended_states.before
	;;
inside-trivialonly)
	LABEL=$2; OUT=$3
	mkdir -p $OUT
	rm -rf $SB; mkdir -p $SB/parts $SB/apt.conf.d $SB/lists/partial $SB/cache/archives/partial $SB/tmp; chmod 1777 $SB/tmp
	printf 'Dir::Etc::parts "%s/apt.conf.d";\nDir::Etc::main "%s/apt.conf";\nDir::Log::Planner "";\n' $SB $SB > $SB/apt-config
	env -i PATH=/usr/sbin:/usr/bin:/sbin:/bin LC_ALL=C LANG=C DEBIAN_FRONTEND=noninteractive TMPDIR=$SB/tmp APT_CONFIG=$SB/apt-config \
		apt-get -o Dir::Cache=$SB/cache -o Debug::NoLocking=true -o APT::Get::Trivial-Only=true install --no-install-recommends rvm-solo > $OUT/$LABEL-trivial-trivialonly.txt 2>&1 < /dev/null && rc=0 || rc=$?
	echo $rc > $OUT/$LABEL-trivial-trivialonly.rc
	dpkg -l rvm-solo | tail -1 > $OUT/$LABEL-trivial-trivialonly-dpkg.txt
	;;
*)
	echo "usage: see the head of this file" >&2; exit 2
	;;
esac
