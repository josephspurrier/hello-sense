# syntax=docker/dockerfile:1
#
# orb: the whole Hello Sense backend in one Go binary. Device edge on :8081,
# app API on :9999, background worker, and Apple push.
#
# Build context is the orb source tree (ORB_SRC in .env), not this directory.

# ------------------------------------------------------------------ build ---
# Pinned to the toolchain the module declares. Building on BUILDPLATFORM and
# cross-compiling to TARGETARCH keeps an arm64 image cheap to produce from an
# amd64 CI machine, which matters for the OCI VM.
FROM --platform=$BUILDPLATFORM golang:1.26-alpine AS build

WORKDIR /src

# Modules first, in their own layer, so editing Go source does not re-download
# the dependency graph on every build.
COPY go.mod go.sum ./
RUN --mount=type=cache,target=/go/pkg/mod go mod download

COPY . .

ARG TARGETOS
ARG TARGETARCH
# CGO_ENABLED=0 makes the binary static. orb has no cgo dependencies: the
# Postgres driver is pgx (pure Go) and there is no sqlite.
RUN --mount=type=cache,target=/go/pkg/mod \
    --mount=type=cache,target=/root/.cache/go-build \
    CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
    go build -trimpath -ldflags='-s -w' -o /out/orb ./cmd/orb

# ---------------------------------------------------------------- runtime ---
FROM alpine:3.22

# Two packages, both load-bearing.
#
# ca-certificates: orb posts to APNs over HTTPS. Without a trust store every
#   push fails certificate verification, and the failure looks like Apple
#   rejecting the token rather than like a missing file.
#
# tzdata: NOT optional, and the reason this image is not `scratch`. orb calls
#   time.LoadLocation in three places, including the alarm path
#   (internal/api/alarms.go) and the sleeper's own zone
#   (internal/store/api.go, SenseZone). Without the zone database those calls
#   return an error and the code falls back to a fixed numeric offset, SILENTLY.
#   The symptom is an alarm exactly one hour wrong after a daylight saving
#   change, which that source calls "the single most annoying bug this code
#   could have". A scratch or distroless-static image reintroduces it, and
#   nothing fails at startup to warn you.
RUN apk add --no-cache ca-certificates tzdata

# A fixed uid so a mounted APNs key can be chowned to it from the host:
#     chown 10001 secrets/AuthKey_*.p8 && chmod 600 secrets/*.p8
RUN addgroup -g 10001 -S orb && adduser -u 10001 -S -G orb orb

COPY --from=build /out/orb /usr/local/bin/orb

USER orb:orb
EXPOSE 8081 9999

ENTRYPOINT ["/usr/local/bin/orb"]
