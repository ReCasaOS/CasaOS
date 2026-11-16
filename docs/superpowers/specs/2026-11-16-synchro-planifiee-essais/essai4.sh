#!/bin/sh
# Second round: kill mid-run, workdir after errors, conflict names, empty dirs, force, S3 (MinIO).
S=/run/r.sock
rclone rcd --rc-no-auth --rc-addr unix://$S >/tmp/rcd.log 2>&1 &
sleep 2
rc() { echo "\$ $1 $2"; rclone rc --unix-socket $S "$1" --json "$2" 2>&1 | grep -v -E '^\s*"(basePath|listing[12]|logFile|session|workDir|path[12]|workdir|backupDir[12]|resilient|recover|conflictResolve|maxDelete|output)"'; }
show() { echo "--- tree $1"; (cd "$1" 2>/dev/null && find . -type f | sort | while read f; do echo "$f = $(head -c 30 "$f" | tr -c '[:print:]' '.')"; done; find . -type d -empty | sed 's/$/ (empty dir)/'); }
h() { echo; echo "=================== $1"; }
BASE='"path1":"/t/p1","path2":"/t/p2","workdir":"/t/work","backupDir1":"/t/.p1-archive/s","backupDir2":"/t/.p2-archive/s","resilient":true,"recover":true,"conflictResolve":"none","maxDelete":50'



mkdir -p /t/s3root; rclone serve s3 --addr 127.0.0.1:9000 /t/s3root >/tmp/s3.log 2>&1 &
startrcd() { rclone rcd --rc-no-auth --rc-addr unix://$S >>/tmp/rcd.log 2>&1 & sleep 2; }
BASE="$BASE,\"maxLock\":\"2m\""
h "K5 power cut with maxLock 2m, retry at once, then after 150 s"
mkdir -p /t/p1 /t/p2; echo a >/t/p1/a; echo b >/t/p2/b
rc sync/bisync "{$BASE,\"resync\":true}" >/dev/null; echo n >/t/p1/n; rc sync/bisync "{$BASE}" >/dev/null
dd if=/dev/urandom of=/t/p1/big bs=1M count=6 2>/dev/null; echo gone >/t/p2/b2; rc sync/bisync "{$BASE}" >/dev/null; dd if=/dev/urandom of=/t/p1/big2 bs=1M count=6 2>/dev/null; rm /t/p1/b2
rc core/bwlimit '{"rate":"200k"}' >/dev/null
rclone rc --unix-socket $S sync/bisync --json "{$BASE,\"_async\":true}" >/dev/null
sleep 5; pkill -9 -f 'rclone rcd'; sleep 1; rm -f $S; startrcd
echo "lock file:"; cat /t/work/*.lck; echo
echo "at once:"; rc sync/bisync "{$BASE}" | cut -c1-120 | head -3
sleep 150
echo "after 150 s:"; rc sync/bisync "{$BASE}" | cut -c1-200
echo "big2 copied: $(cmp /t/p1/big2 /t/p2/big2 2>&1 && echo yes)   b2 on p2: $(ls /t/p2/b2 2>&1)"
echo "p2:"; ls -a /t/p2 | tr '\n' ' '; echo; echo "workdir:"; ls -a /t/work | tr '\n' ' '; echo
show /t/.p2-archive | cut -c1-60
h "S3 (rclone serve s3): one-way with archive, then bisync"
rc config/create '{"name":"s3","type":"s3","parameters":{"provider":"Rclone","endpoint":"http://127.0.0.1:9000","access_key_id":"ak","secret_access_key":"sk"}}' | head -3
rc operations/mkdir '{"fs":"s3:","remote":"bucket"}'
mkdir -p /t/u/dir/emptyd; echo v1 >/t/u/f; echo keep >/t/u/dir/g
rc sync/copy '{"srcFs":"/t/u","dstFs":"s3:bucket/sync"}'
sleep 1; echo v2-longer >/t/u/f; rm /t/u/dir/g
rc sync/sync '{"srcFs":"/t/u","dstFs":"s3:bucket/sync","_config":{"BackupDir":"s3:bucket/sync-archive/s1"}}'
rclone rc --unix-socket $S operations/list --json '{"fs":"s3:bucket","remote":"","opt":{"recurse":true}}' | grep -E '"Path"|"ModTime"'
echo "same content, new date only (touch): does it re-upload?"
touch /t/u/f
rclone rc --unix-socket $S sync/copy --json '{"srcFs":"/t/u","dstFs":"s3:bucket/sync","_async":true}' >/tmp/j; ID=$(sed -n 's/.*"jobid": *\([0-9]*\).*/\1/p' /tmp/j); sleep 2
rclone rc --unix-socket $S core/stats --json "{\"group\":\"job/$ID\"}" | grep -E '"transfers"|"checks"|"bytes"'
SB='"path1":"/t/q","path2":"s3:bucket/both","workdir":"/t/work3","backupDir1":"/t/.q-archive/s","backupDir2":"s3:bucket/both-archive/s","resilient":true,"recover":true,"conflictResolve":"none","maxDelete":50'
mkdir -p /t/q/d; echo one >/t/q/one; echo deep >/t/q/d/deep
rc sync/bisync "{$SB,\"resync\":true}"
rc sync/bisync "{$SB}"
sleep 1
echo from-s3 >/tmp/x; rclone rc --unix-socket $S operations/copyfile --json '{"srcFs":"/tmp","srcRemote":"x","dstFs":"s3:bucket/both","dstRemote":"from-s3.txt"}' >/dev/null
rclone rc --unix-socket $S operations/deletefile --json '{"fs":"s3:bucket/both","remote":"d/deep"}' >/dev/null
echo one-edited >/t/q/one; echo c1 >/t/q/conf.txt
rc sync/bisync "{$SB}"
sleep 1
echo conf-local >/t/q/conf.txt; echo conf-remote-longer >/tmp/c; rclone rc --unix-socket $S operations/copyfile --json '{"srcFs":"/tmp","srcRemote":"c","dstFs":"s3:bucket/both","dstRemote":"conf.txt"}' >/dev/null
rc sync/bisync "{$SB}"
show /t/q; show /t/.q-archive
rclone rc --unix-socket $S operations/list --json '{"fs":"s3:bucket","remote":"","opt":{"recurse":true,"filesOnly":true}}' | grep '"Path"'
echo "second identical pass (nothing should move):"
rclone rc --unix-socket $S sync/bisync --json "{$SB,\"_async\":true}" >/tmp/j; ID=$(sed -n 's/.*"jobid": *\([0-9]*\).*/\1/p' /tmp/j); sleep 3
rclone rc --unix-socket $S core/stats --json "{\"group\":\"job/$ID\"}" | grep -E '"transfers"|"deletes"|"renames"'

h "rcd log, error lines"
grep -E 'ERROR|critical|abort' /tmp/rcd.log | sed 's/^[0-9/]* [0-9:]* //; s/\x1b\[[0-9;]*m//g' | cut -c1-230 | sort | uniq -c | sort -rn | head -30
