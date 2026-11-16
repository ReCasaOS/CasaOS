#!/bin/sh
# Second round: kill mid-run, workdir after errors, conflict names, empty dirs, force, S3 (MinIO).
S=/run/r.sock
rclone rcd --rc-no-auth --rc-addr unix://$S >/tmp/rcd.log 2>&1 &
sleep 2
rc() { echo "\$ $1 $2"; rclone rc --unix-socket $S "$1" --json "$2" 2>&1 | grep -v -E '^\s*"(basePath|listing[12]|logFile|session|workDir|path[12]|workdir|backupDir[12]|resilient|recover|conflictResolve|maxDelete|output)"'; }
show() { echo "--- tree $1"; (cd "$1" 2>/dev/null && find . -type f | sort | while read f; do echo "$f = $(head -c 30 "$f" | tr -c '[:print:]' '.')"; done; find . -type d -empty | sed 's/$/ (empty dir)/'); }
h() { echo; echo "=================== $1"; }
BASE='"path1":"/t/p1","path2":"/t/p2","workdir":"/t/work","backupDir1":"/t/.p1-archive/s","backupDir2":"/t/.p2-archive/s","resilient":true,"recover":true,"conflictResolve":"none","maxDelete":50'


mkdir -p /t/s3root; rclone serve s3 --auth-key ak,sk --addr 127.0.0.1:9000 /t/s3root >/tmp/s3.log 2>&1 &
startrcd() { rclone rcd --rc-no-auth --rc-addr unix://$S >>/tmp/rcd.log 2>&1 & sleep 2; }
killpass() { # $1 = how to kill
  rc core/bwlimit '{"rate":"200k"}' >/dev/null
  OUT=$(rclone rc --unix-socket $S sync/bisync --json "{$BASE,\"_async\":true}" 2>&1); ID=$(echo "$OUT" | sed -n 's/.*"jobid": *\([0-9]*\).*/\1/p'); echo "job $ID"
  sleep 5
  if [ "$1" = stop ]; then rc job/stop "{\"jobid\":$ID}"; sleep 2; rc core/bwlimit '{"rate":"off"}' >/dev/null
  else pkill -9 -f 'rclone rcd'; sleep 1; rm -f $S; startrcd; fi
  echo "workdir after the kill:"; ls -a /t/work | tr '\n' ' '; echo
}
h "K3 resync, one normal pass, then job/stop in the middle, then next pass"
mkdir -p /t/p1 /t/p2; echo a >/t/p1/a; echo b >/t/p2/b
rc sync/bisync "{$BASE,\"resync\":true}"
echo n >/t/p1/n; rc sync/bisync "{$BASE}"
echo "workdir before:"; ls -a /t/work | tr '\n' ' '; echo
dd if=/dev/urandom of=/t/p1/big bs=1M count=6 2>/dev/null; echo late >/t/p1/late
killpass stop
rc sync/bisync "{$BASE}"
echo "big copied: $(cmp /t/p1/big /t/p2/big 2>&1 && echo yes)"; ls -a /t/work | tr '\n' ' '; echo
rc sync/bisync "{$BASE}"

h "K4 rcd killed -9 in the middle (power cut), restarted, next pass"
rc sync/bisync "{$BASE,\"resync\":true}" >/dev/null; echo n2 >/t/p1/n2; rc sync/bisync "{$BASE}" >/dev/null
dd if=/dev/urandom of=/t/p1/big3 bs=1M count=6 2>/dev/null
killpass power
rc sync/bisync "{$BASE}"
echo "big3 copied: $(cmp /t/p1/big3 /t/p2/big3 2>&1 && echo yes)"; ls -a /t/work | tr '\n' ' '; echo
echo "same with maxLock 2m:"
rc sync/bisync "{$BASE,\"maxLock\":\"2m\"}"
echo "partials left on p2:"; ls -a /t/p2

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
