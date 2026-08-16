#!/bin/bash
# Replicate the Travis CI build of kitsune 1.9.2 EXACTLY (import order, build
# order, no extra flags), using CCS 6.0.1 + CGT 5.1.5 in the linux/386 container.
set -e
ECL=/root/ti/ccsv6/eclipse
JAVA=$ECL/jre/bin/java
L=$(find $ECL/plugins -name "org.eclipse.equinox.launcher_*.jar" | head -1)
WS=/root/ws
run(){ xvfb-run -a "$JAVA" -jar "$L" -nosplash "$@"; }

rm -rf "$WS"
cd /src

for loc in kitsune/main/ccs simplelink/ccs oslib/ccs third_party/fatfs/ccs driverlib/ccs; do
  echo "== import $loc =="
  run -data "$WS" -application com.ti.ccstudio.apps.projectImport -ccs.location "./$loc" 2>&1 | grep -aiE "Done|error|Exception" | head -5 || true
done

echo "== build libs (driverlib simplelink oslib fatfs) =="
run -data "$WS" -application com.ti.ccstudio.apps.projectBuild -ccs.projects driverlib simplelink oslib fatfs 2>&1 | tail -40

echo "== build kitsune =="
run -data "$WS" -application com.ti.ccstudio.apps.projectBuild -ccs.projects kitsune 2>&1 | tail -40

echo "== results =="
for f in /src/kitsune/main/ccs/exe/kitsune.out /src/kitsune/main/ccs/exe/kitsune.bin /src/exe/kitsune.out /src/exe/kitsune.bin; do
  [ -f "$f" ] && echo "$f $(stat -c%s "$f")B sha1=$(sha1sum "$f" | cut -d" " -f1)"
done
echo "TARGET .bin sha1=0c5f639e1290df0e3a5f8641d670923ed71a5e63 (146864B)"
