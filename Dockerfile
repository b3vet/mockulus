# SPDX-License-Identifier: Apache-2.0

# UI stage. The admin UI is a static site that internal/adminui embeds with
# //go:embed, so it is an input to `go build` rather than something that could
# be copied into the runtime image afterwards — it has to exist as files before
# the Go stage compiles, which is why this stage comes first.
#
# Node lives here and only here. Building the UI inside the image is what keeps
# `docker build .` self-contained for a clean checkout on a host with no Node,
# which is the shape the release pipeline builds in.
FROM node:22-alpine AS ui

# The same directory the Go stage uses, so the tree inside the image matches the
# tree in the repository. vite resolves its output directory relative to
# ui/vite.config.ts (`../internal/adminui/dist`), and a stage that laid the
# sources out differently would put the bundle somewhere that only made sense
# here.
WORKDIR /src

# corepack ships with the Node image and takes the pnpm version from
# package.json's packageManager field, so the version is pinned by the
# repository rather than by whatever npm's latest tag resolved to on the day the
# image was built.
RUN corepack enable

# Manifests and the lockfile first, for the reason the Go stage copies
# go.mod/go.sum before the sources: resolving and fetching dependencies is the
# expensive layer, and it should only be invalidated by a change to the files
# that actually determine its result. Editing a .svelte file rebuilds the bundle
# and nothing else.
COPY package.json pnpm-lock.yaml pnpm-workspace.yaml .npmrc ./
COPY ui/package.json ui/
RUN pnpm install --frozen-lockfile

COPY ui/ ui/
RUN pnpm run build

# Build stage. CGO is off so the result is a static binary that runs on the
# distroless static base with no libc at all (SPEC §15.1).
FROM golang:1.25-bookworm AS build

ARG VERSION=dev
# COVER=1 produces the coverage-instrumented variant the E2E gate runs against.
# It is never published (SPEC §19.1).
ARG COVER=""

WORKDIR /src

COPY go.mod go.sum ./
RUN go mod download

COPY . .

# The bundle, placed before the compile so //go:embed all:dist picks up the real
# site instead of the committed .gitkeep placeholder that keeps a Node-less
# `go build` working. .dockerignore keeps any locally built dist/ out of the
# context precisely so this line is the only way the image can acquire one: what
# the image serves is then always what this build produced, never what happened
# to be sitting in the developer's working tree.
COPY --from=ui /src/internal/adminui/dist ./internal/adminui/dist

RUN set -eux; \
    if [ -n "$COVER" ]; then COVERFLAG="-cover -covermode=atomic"; else COVERFLAG=""; fi; \
    CGO_ENABLED=0 go build $COVERFLAG \
        -trimpath \
        -ldflags "-s -w -X main.version=${VERSION}" \
        -o /out/mockulus ./cmd/mockulus

# Runtime stage: nonroot, no shell, read-only friendly.
FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/mockulus /usr/local/bin/mockulus
COPY LICENSE NOTICE /usr/local/share/mockulus/

EXPOSE 8080 9090
USER nonroot:nonroot

# The base has no shell and no curl, so the binary probes itself. It reads the
# same configuration the server did, which is what makes the check follow a
# moved admin_port instead of testing a default nothing is listening on.
#
# /healthz on the ops port, never the mock port: an unmatched mock request is a
# 404 by design (SPEC §5.4), so a check aimed there would call a working pod
# unhealthy the moment a suite reset its stubs. Kubernetes uses the probes in
# the chart (SPEC §15.2); this is for everyone running the image directly.
HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=3 \
    CMD ["/usr/local/bin/mockulus", "-healthcheck"]

ENTRYPOINT ["/usr/local/bin/mockulus"]
