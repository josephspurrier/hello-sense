# syntax=docker/dockerfile:1
#
# orb-algo: a thin HTTP shim over Hello's original sleep algorithms.
#
# Two parts. The engine is suripu-app-*.jar, a 77 MB shaded jar containing
# suripu-core and every transitive dependency, kept unmodified because
# reimplementing the timeline algorithms is not something anyone should do
# twice. The shim is ten Java files that expose it over HTTP.
#
# THE JAR IS NOT IN THIS REPOSITORY. It is Hello's build output, too large and
# not ours to redistribute. It is supplied through a named build context called
# `jars`, wired up in docker-compose.yml. See README.md, "The suripu jar".
#
# Build context is the orb-algo source tree (ORB_ALGO_SRC in .env).

# ------------------------------------------------------------------ build ---
# No Maven. The shaded jar already carries guava, joda-time, jackson and
# protobuf, so javac needs nothing else, which avoids resolving 2016-era
# artifacts from repositories that no longer exist.
FROM eclipse-temurin:8-jdk AS build

ARG SURIPU_JAR_NAME=suripu-app-0.6.0-SNAPSHOT.jar
WORKDIR /src

COPY --from=jars ${SURIPU_JAR_NAME} /algo.jar
COPY src/ src/

RUN mkdir -p build && javac -cp /algo.jar -d build src/*.java

# ---------------------------------------------------------------- runtime ---
# JRE rather than JDK: 406 MB against 511 MB, and nothing compiles at runtime.
FROM eclipse-temurin:8-jre

RUN groupadd -g 10002 algo && useradd -u 10002 -g algo -s /usr/sbin/nologin -M algo

WORKDIR /app
COPY --from=build /algo.jar      /app/algo.jar
COPY --from=build /src/build     /app/build
COPY models/                     /app/models/

ENV ORB_ALGO_PORT=8090 \
    ORB_ALGO_MODELS=/app/models \
    JAVA_OPTS=""

USER algo:algo
EXPOSE 8090

# JAVA_OPTS is unset by default, which reproduces the pre-container behaviour
# exactly. See .env.example for why capping the heap is left as a decision.
ENTRYPOINT ["sh", "-c", "exec java $JAVA_OPTS -cp /app/algo.jar:/app/build Server"]
