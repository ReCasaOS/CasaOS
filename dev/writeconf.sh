#!/usr/bin/env bash
set -euo pipefail

if [[ $# -ne 2 ]]; then
	echo "usage: writeconf.sh DEVROOT CASAOS_REPO" >&2
	exit 1
fi

DEVROOT=$(cd "$1" && pwd)
CASAOS_REPO=$(cd "$2" && pwd)
SHELL="$CASAOS_REPO/build/sysroot/usr/share/casaos/shell"
ETC="$DEVROOT/etc"
RUN="$DEVROOT/run"
LIB="$DEVROOT/lib"
LOG="$DEVROOT/log"
DATA="$DEVROOT/data"
WWW="$DEVROOT/www"

mkdir -p "$ETC" "$RUN" "$LIB" "$LOG" "$DATA" "$WWW" "$LIB/db" "$LIB/conf" "$LIB/appstore" "$LIB/apps"

cat >"$ETC/gateway.ini" <<EOF
[common]
runtimepath=$RUN

[gateway]
port=8081
address=127.0.0.1
tlscert=
tlskey=
httpsport=0
LogPath=$LOG
LogSaveName=gateway
LogFileExt=log
EOF

cat >"$ETC/message-bus.conf" <<EOF
[common]
RuntimePath=$RUN

[app]
LogPath=$LOG
LogSaveName=message-bus
LogFileExt=log
DBPath=$LIB
EOF

cat >"$ETC/user-service.conf" <<EOF
[common]
RuntimePath=$RUN

[app]
LogPath=$LOG
LogSaveName=user-service
LogFileExt=log
DBPath=$LIB/db
UserDataPath=$LIB
FirstAdmin=web
EOF

cat >"$ETC/local-storage.conf" <<EOF
[common]
RuntimePath=$RUN

[app]
LogPath=$LOG
LogSaveName=local-storage
LogFileExt=log
DBPath=$LIB/db
ShellPath=$SHELL

[server]
USBAutoMount=
EnableMergerFS=false
EOF

cat >"$ETC/app-management.conf" <<EOF
[common]
RuntimePath=$RUN

[app]
LogPath=$LOG
LogSaveName=app-management
LogFileExt=log
AppStorePath=$LIB/appstore
AppsPath=$LIB/apps

[server]
appstore = https://cdn.jsdelivr.net/gh/IceWhaleTech/CasaOS-AppStore@gh-pages/store/main.zip
appstore = https://github.com/bigbeartechworld/big-bear-casaos/archive/refs/heads/master.zip
EOF

cat >"$ETC/casaos.conf" <<EOF
[common]
RuntimePath=$RUN

[app]
PAGE_SIZE = 10
LogPath=$LOG
LogSaveName=log
LogFileExt=log
DBPath=$LIB
ShellPath=$SHELL
UserDataPath=$LIB/conf

[server]
RunMode=release
SambaMainConfig=managed
ImageManaged=false

[file]
ManagementRoots=$DATA
EOF
