#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Holds the shipped image to the size budget of SPEC §15.1.
#
# The budget exists because the operational profile is the reason this project
# is Go at all (SPEC §2, D1): a small image is what makes horizontal scale-out
# cheap, and an image that quietly doubles takes the argument with it. Nothing
# checked it until the admin UI arrived — the one addition with an obvious path
# to growing without anybody noticing, since a bundle grows by dependency rather
# than by anyone writing more of it.
#
# WHAT IS MEASURED, and why this is stated rather than assumed. Docker will tell
# you at least three different numbers for one image:
#
#   * `docker image inspect --format {{.Size}}` reports the sum of layer sizes.
#     On a classic daemon that is uncompressed; on a containerd-backed one it
#     can report the compressed size, which is roughly four times smaller. The
#     same command on two developers' machines answers differently.
#   * `docker images` adds attestation and multi-platform manifest entries for a
#     buildx image, which is a number about the manifest list rather than about
#     what a node pulls.
#   * The flattened filesystem — what actually occupies disk on the node — is
#     the same everywhere.
#
# So the flattened filesystem is what this measures. It is the number the budget
# was always about, and it is the only one that does not depend on which daemon
# happened to run the build.
set -euo pipefail

cd "$(dirname "$0")/.."

IMAGE=${1:-}
BUDGET_MB=${IMAGE_BUDGET_MB:-40}

if [ -z "$IMAGE" ]; then
  echo "usage: $0 <image-ref>   (e.g. $0 ghcr.io/b3vet/mockulus:dev)" >&2
  exit 2
fi

if ! docker image inspect "$IMAGE" >/dev/null 2>&1; then
  echo "no such image: $IMAGE — build it first (make image)" >&2
  exit 1
fi

container=$(docker create "$IMAGE")
trap 'docker rm -f "$container" >/dev/null 2>&1 || true' EXIT

bytes=$(docker export "$container" | wc -c | tr -d ' ')
mb=$(awk -v b="$bytes" 'BEGIN { printf "%.1f", b / 1048576 }')

# The UI bundle's own contribution, reported rather than budgeted separately.
# It is a rounding error against the binary today, and the reason to print it is
# that the day it stops being one, this line is where that shows up.
ui_bytes=0
if [ -d internal/adminui/dist ]; then
  ui_bytes=$(find internal/adminui/dist -type f -exec cat {} + 2>/dev/null | wc -c | tr -d ' ')
fi
ui_kb=$(awk -v b="${ui_bytes:-0}" 'BEGIN { printf "%.0f", b / 1024 }')

echo "image:        $IMAGE"
echo "filesystem:   ${mb} MB (flattened; the number a node stores)"
echo "embedded UI:  ${ui_kb} kB of that, compiled into the binary"
echo "budget:       ${BUDGET_MB} MB (SPEC §15.1)"

# Compared in bytes rather than by re-parsing the megabyte string above. In a
# locale that formats decimals with a comma, `printf "%.1f"` produces "40,9" and
# awk then reads that back as 40 — so an image just over the line would pass,
# on some machines and not others. Integers have no such reading.
budget_bytes=$(awk -v b="$BUDGET_MB" 'BEGIN { printf "%d", b * 1048576 }')
over=$([ "$bytes" -gt "$budget_bytes" ] && echo 1 || echo 0)
if [ "$over" = "1" ]; then
  echo >&2
  echo "The image is over the §15.1 budget." >&2
  echo >&2
  echo "This is a design constraint rather than a lint: the deployment model is N" >&2
  echo "replicas behind a Service, so image size is paid on every scale-out and on" >&2
  echo "every rolling upgrade. Either bring it back under, or change the budget in" >&2
  echo "SPEC §15.1 and here together, with the reason written down." >&2
  exit 1
fi

echo "within budget."
