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

ENTRYPOINT ["/usr/local/bin/mockulus"]
