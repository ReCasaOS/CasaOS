#!/bin/sh
# Fifth round: no-change pass after the first pass, deletions across a stop, maxLock on a long pass.
S=/run/r.sock
rclone rcd --rc-no-auth --rc-addr unix://$S >/tmp/rcd.log 2>&1 &
sleep 2
rc() { echo "\$ $1 $(echo "$2" | sed 's/"path1".*"maxLock":"2m"/BASE/')"; rclone rc --unix-socket $S "$1" --json "$2" 2>&1 | grep -E '"error"' | cut -c1-160; }
h() { echo; echo "=================== $1"; }
BASE='"path1":"/t/p1","path2":"/t/p2","workdir":"/t/work","backupDir1":"/t/.p1-archive/s","backupDir2":"/t/.p2-archive/s","resilient":true,"recover":true,"conflictResolve":"none","maxDelete":50,"maxLock":"2m"'
start() { OUT=$(rclone rc --unix-socket $S sync/bisync --json "{$BASE,\"_async\":true}" 2>&1); ID=$(echo "$OUT" | sed -n 's/.*"jobid": *\([0-9]*\).*/\1/p'); echo "job $ID"; }
slow() { rclone rc --unix-socket $S core/bwlimit --json "{\"rate\":\"$1\"}" >/dev/null; }
wd() { echo "workdir: $(ls /t/work | sed 's/t_p1..t_p2.//' | tr '\n' ' ')"; }

h "R1 first pass = resync + a normal pass with nothing to do, then a stop, then recovery"
mkdir -p /t/p1 /t/p2; echo a >/t/p1/a; echo b >/t/p2/b
rc sync/bisync "{$BASE,\"resync\":true}"; wd
rc sync/bisync "{$BASE}"; wd
dd if=/dev/urandom of=/t/p1/big bs=1M count=6 2>/dev/null
slow 200k; start; sleep 5; rc job/stop "{\"jobid\":$ID}"; sleep 2; slow off; wd
rc sync/bisync "{$BASE}"; wd
echo "big copied: $(cmp /t/p1/big /t/p2/big 2>&1 && echo yes)"

h "R2 deletions made before a pass that is stopped"
echo d1 >/t/p1/del-on-box; echo d2 >/t/p2/del-on-cloud; rc sync/bisync "{$BASE}"
rm /t/p1/del-on-box; rm /t/p2/del-on-cloud
dd if=/dev/urandom of=/t/p1/big2 bs=1M count=6 2>/dev/null
slow 200k; start; sleep 5; rc job/stop "{\"jobid\":$ID}"; sleep 2; slow off
echo "after the stop:  box: $(ls /t/p1 | tr '\n' ' ') | cloud: $(ls /t/p2 | tr '\n' ' ')"
rc sync/bisync "{$BASE}"
echo "after recovery:  box: $(ls /t/p1 | tr '\n' ' ') | cloud: $(ls /t/p2 | tr '\n' ' ')"
echo "archives: box: $(ls /t/.p1-archive/s 2>/dev/null | tr '\n' ' ') | cloud: $(ls /t/.p2-archive/s 2>/dev/null | tr '\n' ' ')"
rc sync/bisync "{$BASE}"
echo "one pass later:  box: $(ls /t/p1 | tr '\n' ' ') | cloud: $(ls /t/p2 | tr '\n' ' ')"

h "R2b same, stopped by a power cut (rcd killed)"
echo d3 >/t/p1/del-on-box2; rc sync/bisync "{$BASE}"; rm /t/p1/del-on-box2
dd if=/dev/urandom of=/t/p1/big3 bs=1M count=6 2>/dev/null
slow 200k; start; sleep 5; pkill -9 -f 'rclone rcd'; sleep 1; rm -f $S; rclone rcd --rc-no-auth --rc-addr unix://$S >>/tmp/rcd.log 2>&1 & sleep 150
rc sync/bisync "{$BASE}"
echo "after recovery:  box: $(ls /t/p1 | tr '\n' ' ') | cloud: $(ls /t/p2 | tr '\n' ' ')"

h "R3 a pass longer than maxLock (2m): does a second one start at 150 s?"
dd if=/dev/urandom of=/t/p1/huge bs=1M count=45 2>/dev/null
slow 200k; start; FIRST=$ID
sleep 150
echo "first still running: $(rclone rc --unix-socket $S job/status --json "{\"jobid\":$FIRST}" | grep finished)"
echo "lock: $(ls -la --time-style=+%T /t/work/*.lck 2>&1 | awk '{print $6}')  now: $(date +%T)"
echo "second pass asked now:"
rc sync/bisync "{$BASE}"
rc job/stop "{\"jobid\":$FIRST}"; slow off

h "rcd log, error lines"
grep -E 'ERROR|critical|abort|expired|lock' /tmp/rcd.log | sed 's/^[0-9/]* [0-9:]* //; s/\x1b\[[0-9;]*m//g' | cut -c1-200 | sort | uniq -c | sort -rn | head -20
