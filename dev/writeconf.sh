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

mkdir -p "$ETC" "$RUN" "$LIB" "$LOG" "$DATA" "$DATA/AppData" "$WWW" "$LIB/db" "$LIB/conf" "$LIB/appstore" "$LIB/apps"

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
DataPath=$LIB

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

cat >"$LIB/fake-disks.json" <<'EOF'
{
  "blockdevices": [
    {
      "name": "vda",
      "path": "/dev/vda",
      "type": "disk",
      "tran": "sata",
      "size": 10737418240,
      "ro": false,
      "rm": false,
      "hotplug": false,
      "serial": "FAKEVDA",
      "subsystems": "block:scsi:pci",
      "children": [
        {
          "name": "vda1",
          "path": "/dev/vda1",
          "type": "part",
          "fstype": "ext4",
          "mountpoint": "/",
          "size": 10737418240,
          "uuid": "11111111-1111-1111-1111-111111111111",
          "fssize": "10737418240",
          "fsused": "1073741824",
          "fsavail": "9663676416"
        }
      ]
    },
    {
      "name": "vdb",
      "path": "/dev/vdb",
      "type": "disk",
      "tran": "sata",
      "size": 21474836480,
      "ro": false,
      "rm": false,
      "hotplug": false,
      "serial": "FAKEVDB",
      "subsystems": "block:scsi:pci",
      "children": [
        {
          "name": "vdb1",
          "path": "/dev/vdb1",
          "type": "part",
          "fstype": "ext4",
          "mountpoint": "",
          "size": 21474836480,
          "uuid": "22222222-2222-2222-2222-222222222222",
          "fssize": "21474836480",
          "fsused": "0",
          "fsavail": "21474836480"
        }
      ]
    }
  ]
}
EOF
