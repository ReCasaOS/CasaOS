#!/bin/sh
# Trials for the scheduled sync spec, against a real rclone rcd over a unix socket.
S=/run/r.sock
rclone version | head -1
rclone rcd --rc-no-auth --rc-addr unix://$S >/tmp/rcd.log 2>&1 &
sleep 2
rc() { echo "\$ $1 $2"; rclone rc --unix-socket $S "$1" --json "$2" 2>&1; echo "-> exit $?"; }
show() { echo "--- tree $1"; (cd "$1" 2>/dev/null && find . -type f | sort | while read f; do echo "$f = $(head -c 40 "$f")"; done; find . -type d -empty | sed 's/$/ (empty dir)/'); }
h() { echo; echo "=================== $1"; }

h "T1 sync/copy + BackupDir keeps the overwritten version"
mkdir -p /t/a /t/b
echo old >/t/b/f1; touch -d '2026-01-01 00:00:00' /t/b/f1
echo newer-content >/t/a/f1; echo two >/t/a/f2
rc sync/copy '{"srcFs":"/t/a","dstFs":"/t/b","_config":{"BackupDir":"/t/.b-archive/s1"}}'
show /t/b; show /t/.b-archive

h "T2 mirror off: file deleted at source stays"
rm /t/a/f2
rc sync/copy '{"srcFs":"/t/a","dstFs":"/t/b","_config":{"BackupDir":"/t/.b-archive/s2"}}'
show /t/b

h "T3 sync/sync + BackupDir: deleted file goes to the archive"
rc sync/sync '{"srcFs":"/t/a","dstFs":"/t/b","_config":{"BackupDir":"/t/.b-archive/s3"}}'
show /t/b; show /t/.b-archive

h "T4 MaxDelete exceeded on sync/sync"
mkdir -p /t/c /t/d
for i in 1 2 3 4 5 6 7 8 9 10; do echo x$i >/t/d/g$i; done
cp /t/d/g1 /t/d/g2 /t/c/
rc sync/sync '{"srcFs":"/t/c","dstFs":"/t/d","_config":{"MaxDelete":3,"BackupDir":"/t/.d-archive/s1"}}'
echo "files left in d: $(ls /t/d | wc -l)"; show /t/.d-archive

h "T4b empty source, sync/sync without guard (what the pre-check must prevent)"
mkdir -p /t/e /t/f; echo keep >/t/f/k
rc sync/sync '{"srcFs":"/t/e","dstFs":"/t/f","_config":{"MaxDelete":0}}'
show /t/f

h "T5 BackupDir through an alias remote and a crypt remote"
mkdir -p /t/raw/root /t/raw2
rc config/create '{"name":"cb-raw","type":"local","parameters":{}}'
rc config/create '{"name":"al","type":"alias","parameters":{"remote":"/t/raw/root"}}'
rc config/create '{"name":"cr","type":"crypt","parameters":{"remote":"/t/raw2","password":"secret"},"opt":{"obscure":true}}'
echo old >/t/raw/root/sync-f1-placeholder; rm /t/raw/root/sync-f1-placeholder
rc sync/copy '{"srcFs":"/t/a","dstFs":"al:sync"}'
echo changed-content-longer >/t/a/f1
rc sync/sync '{"srcFs":"/t/a","dstFs":"al:sync","_config":{"BackupDir":"al:sync-archive/s1"}}'
show /t/raw
rc sync/copy '{"srcFs":"/t/a","dstFs":"cr:sync"}'
echo changed-again-even-longer-content >/t/a/f1
rc sync/sync '{"srcFs":"/t/a","dstFs":"cr:sync","_config":{"BackupDir":"cr:sync-archive/s1"}}'
rc operations/list '{"fs":"cr:","remote":"sync-archive/s1"}'
echo "raw2 names:"; find /t/raw2 | head -8
h "T5b BackupDir on a different remote name for the same place (expected refusal?)"
rc sync/sync '{"srcFs":"/t/a","dstFs":"al:sync","_config":{"BackupDir":"/t/raw/root/sync-archive/s2"}}'
h "T5c BackupDir overlapping the destination"
rc sync/sync '{"srcFs":"/t/a","dstFs":"/t/b","_config":{"BackupDir":"/t/b/.archive"}}'

W=/t/work
h "B1 bisync first pass (resync), async, stats"
mkdir -p /t/p1 /t/p2/emptydir
echo only1 >/t/p1/x; echo only2 >/t/p2/y
echo one >/t/p1/same; echo two-two-two >/t/p2/same; touch -d '2026-01-01 00:00:00' /t/p1/same
BASE='"path1":"/t/p1","path2":"/t/p2","workdir":"/t/work","backupDir1":"/t/.p1-archive","backupDir2":"/t/.p2-archive","resilient":true,"recover":true,"conflictResolve":"none","maxDelete":50'
echo "\$ sync/bisync resync _async"; OUT=$(rclone rc --unix-socket $S sync/bisync --json "{$BASE,\"resync\":true,\"_async\":true}" 2>&1); echo "$OUT"
ID=$(echo "$OUT" | sed -n 's/.*"jobid": *\([0-9]*\).*/\1/p')
sleep 3
rc job/status "{\"jobid\":$ID}" | head -20
rc core/stats "{\"group\":\"job/$ID\"}" | grep -E 'bytes|transfers|totalBytes|exit'
show /t/p1; show /t/p2; echo "workdir:"; ls /t/work

h "B2 normal pass: add on 1, modify on 2, delete on 1, delete on 2"
sleep 1
echo added >/t/p1/new1; echo y-modified-on-2 >/t/p2/y; rm /t/p1/x; mkdir -p /t/p1/newempty
echo gone2 >/t/p1/del2; rc sync/bisync "{$BASE}" >/dev/null; sleep 1; rm /t/p2/del2
rc sync/bisync "{$BASE}"
show /t/p1; show /t/p2; show /t/.p1-archive; show /t/.p2-archive

h "B3 conflict on photo.jpg and on noext"
echo base >/t/p1/photo.jpg; echo base >/t/p1/noext; rc sync/bisync "{$BASE}" >/dev/null; sleep 1
echo edit-from-one >/t/p1/photo.jpg; echo edit-from-two-longer >/t/p2/photo.jpg
echo edit-from-one >/t/p1/noext; echo edit-from-two-longer >/t/p2/noext
rc sync/bisync "{$BASE}"
show /t/p1; show /t/p2

h "B6 killed in the middle (job/stop), then next pass"
dd if=/dev/urandom of=/t/p1/big bs=1M count=8 2>/dev/null
OUT=$(rclone rc --unix-socket $S sync/bisync --json "{$BASE,\"_async\":true,\"_config\":{\"BwLimit\":\"500k\"}}" 2>&1); ID=$(echo "$OUT" | sed -n 's/.*"jobid": *\([0-9]*\).*/\1/p'); echo "job $ID"
sleep 4
rc core/stats "{\"group\":\"job/$ID\"}" | grep -E '"bytes"|"speed"|"totalBytes"|"name"'
rc job/stop "{\"jobid\":$ID}"
sleep 2
rc job/status "{\"jobid\":$ID}" | grep -E 'error|finished|success'
echo "workdir after stop:"; ls /t/work
echo "big on p2: $(ls -la /t/p2/big 2>&1)"
rc sync/bisync "{$BASE}"
echo "big on p2 after next pass: $(ls -la /t/p2/big 2>&1)"; echo "workdir:"; ls /t/work

h "B6b same without recover/resilient after a stop"
dd if=/dev/urandom of=/t/p1/big2 bs=1M count=8 2>/dev/null
NOREC='"path1":"/t/p1","path2":"/t/p2","workdir":"/t/work","maxDelete":50'
OUT=$(rclone rc --unix-socket $S sync/bisync --json "{$NOREC,\"_async\":true,\"_config\":{\"BwLimit\":\"500k\"}}" 2>&1); ID=$(echo "$OUT" | sed -n 's/.*"jobid": *\([0-9]*\).*/\1/p')
sleep 4; rc job/stop "{\"jobid\":$ID}" >/dev/null; sleep 2
rc sync/bisync "{$NOREC}"
rc sync/bisync "{$BASE}"

h "B5 more than half of one side deleted"
N1=$(find /t/p1 -type f | wc -l); echo "files on p1 before: $N1"
find /t/p1 -type f | head -n $((N1 - 1)) | xargs rm -f
rc sync/bisync "{$BASE}"
echo "p2 files after: $(find /t/p2 -type f | wc -l)"
echo "next pass after the refusal:"
rc sync/bisync "{$BASE}"
echo "p1: $(find /t/p1 -type f | wc -l) p2: $(find /t/p2 -type f | wc -l)"

h "B5b one side empty (unmounted disk)"
rc sync/bisync "{$BASE,\"resync\":true}" >/dev/null
mv /t/p1 /t/p1.away; mkdir /t/p1
rc sync/bisync "{$BASE}"
echo "p2 files after: $(find /t/p2 -type f | wc -l)"
rmdir /t/p1; mv /t/p1.away /t/p1
rc sync/bisync "{$BASE}"

h "B4 workdir lost"
rm -rf /t/work
rc sync/bisync "{$BASE}"
echo "p1: $(find /t/p1 -type f | wc -l) p2: $(find /t/p2 -type f | wc -l)"

h "B9 resync brings back a file deleted on one side only"
rc sync/bisync "{$BASE,\"resync\":true}" >/dev/null
rm /t/p1/y
rm -rf /t/work
rc sync/bisync "{$BASE,\"resync\":true}" | tail -3
echo "y on p1 again: $(ls /t/p1/y 2>&1)"

h "rcd log, bisync lines about resync / abort"
grep -i -E 'resync|abort|critical|lock|max.delete|too many' /tmp/rcd.log | sed 's/^[0-9/]* [0-9:]* //' | sort | uniq -c | sort -rn | head -40
