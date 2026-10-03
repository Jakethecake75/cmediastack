# syntax=docker/dockerfile:1.7
#
# CMediaStack container image.
#
# Hardening properties this file is responsible for (requirements §8):
#   * pure-Go static binary, so the runtime image needs no libc and no shell
#   * distroless runtime: no package manager, no busybox, nothing to pivot to
#   * non-root UID baked into the image, not left to the runtime to remember
#   * pinned base images by digest so a rebuild is reproducible
#   * ffmpeg and ffprobe present, static, and NOT from a package manager
#
# The remaining hardening (read-only rootfs, dropped capabilities, seccomp,
# no-new-privileges, tmpfs) is applied at run time and lives in
# docker-compose.yml, because Docker cannot express it in an image.

# --- media tools -------------------------------------------------------------
#
# ffmpeg and ffprobe, statically linked, copied into the runtime image.
#
# # Why they are here at all
#
# Because without them nothing in Phase 4 works. This was not obvious from
# reading the file: the image built, started, served the library, and answered
# every playback request with a bare 500 — because `ffprobe` is resolved from
# PATH and a distroless image has no PATH worth speaking of. Found by running
# the built container and asking it to play something, which is the only way it
# was ever going to be found.
#
# # Why a static build rather than apt
#
# `apt-get install ffmpeg` means a runtime image with a package manager, a
# shell, and several hundred shared libraries — which discards every property
# the distroless base is here to provide. Two static binaries keep all of them.
#
# # What this costs, stated rather than buried
#
# These binaries come from a third party (mwader/static-ffmpeg, the standard
# image for this purpose), so they are a supply-chain dependency in the trust
# path of the most security-sensitive component in the system — the thing
# ADR-0020's jail exists to contain. It is PINNED BY DIGEST, so the bytes are
# fixed and a rebuild cannot silently get different ones. Building ffmpeg from
# source here was considered and rejected as disproportionate: ffmpeg's
# configure matrix is its own maintenance project, and a bad build of it would
# be a worse outcome than a pinned known-good one.
FROM mwader/static-ffmpeg:7.1@sha256:a8090df5f5608daef387e1b2e93b98aaacb4d92153ad904e7d715c725724fca4 AS ffmpeg

# --- build -------------------------------------------------------------------
FROM golang:1.26-bookworm@sha256:9fdc884aacc3bec89b20ffc69f4bb369c78210e3e4f600387b5128b12c199f81 AS build

WORKDIR /src

# Dependencies first so that a source-only change does not re-download them.
COPY go.mod go.sum ./
RUN go mod download && go mod verify

COPY . .

ARG VERSION=dev
# CGO_ENABLED=0 is load-bearing, not an optimisation: it is what makes the
# binary runnable on a distroless static base and portable across hosts
# (ADR-0002).
#   -trimpath      strips local build paths out of the binary
#   -buildvcs      records the commit, so a running instance is identifiable
#   -s -w          drop the symbol table and DWARF; smaller, less to read
RUN CGO_ENABLED=0 GOOS=linux go build \
        -trimpath \
        -buildvcs=true \
        -ldflags="-s -w -X main.Version=${VERSION}" \
        -o /out/cmediastack \
        ./cmd/cmediastack

# Verify it really is static. A dynamically linked binary would fail at run
# time inside distroless/static, and failing here is much easier to diagnose.
RUN test -z "$(go version -m /out/cmediastack | grep -F 'CGO_ENABLED=1')" || \
    (echo 'FATAL: binary was built with cgo' && exit 1)

# --- runtime -----------------------------------------------------------------
# distroless/static: no shell, no package manager, no libc. If an attacker gets
# code execution inside this container there is nothing here to build on.
FROM gcr.io/distroless/static-debian12:nonroot@sha256:afa5c872c891853ca7fcf1f12c3edb23f7eeef36189728842dd51042ff57f7ab

# nonroot in this image is UID/GID 65532. Declared explicitly so that a
# `docker run` without compose is still non-root.
USER 65532:65532

COPY --from=build --chown=65532:65532 /out/cmediastack /usr/local/bin/cmediastack

# The media tools, on the PATH the application looks them up on. Mode 0555:
# readable and executable by everyone, writable by nobody, including the user
# the process runs as — a parser cannot rewrite itself.
COPY --from=ffmpeg --chmod=0555 /ffmpeg  /usr/local/bin/ffmpeg
COPY --from=ffmpeg --chmod=0555 /ffprobe /usr/local/bin/ffprobe

# /config holds the database and configuration; /media is the library. Both are
# mounted at run time. Declaring them documents the contract.
VOLUME ["/config"]

EXPOSE 8080
# The management listener binds to loopback by default and is deliberately NOT
# exposed: /metrics and /health/detail must not be reachable from outside.

ENTRYPOINT ["/usr/local/bin/cmediastack"]
CMD ["--config", "/config/config.yaml"]
