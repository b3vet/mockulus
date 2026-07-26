# SPDX-License-Identifier: Apache-2.0

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
