#!/bin/bash
# Compiles orb-algo against the suripu fat jar.
#
# No Maven: the suripu-app shaded jar already contains suripu-core and every
# transitive dependency (guava, joda-time, jackson, protobuf), so javac needs
# nothing else. That avoids resolving 2016-era dependencies from repositories
# that no longer exist.
set -euo pipefail

ROOT=$(cd "$(dirname "$0")" && pwd)
JAR=${JAR:-$ROOT/../infrastructure/suripu-app/target/suripu-app-0.6.0-SNAPSHOT.jar}

[ -f "$JAR" ] || { echo "fat jar not found: $JAR" >&2; exit 1; }

mkdir -p "$ROOT/build"
docker run --rm \
  -v "$(cd "$(dirname "$JAR")" && pwd):/jars:ro" \
  -v "$ROOT:/src" -w /src \
  eclipse-temurin:8-jdk \
  javac -cp "/jars/$(basename "$JAR")" -d build $(cd "$ROOT" && ls src/*.java | sed 's|^|/src/|')

echo "compiled to $ROOT/build"
