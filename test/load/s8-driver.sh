#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The leak half of S8 (SPEC §16.1): no growth in resident memory over a one hour
# soak. s8.js owns the ceiling — it holds the S2 stub set and traffic mix
# against the pod, reloads the snapshot once a minute, and fails the run when
# any sample crosses 256 MiB. What it cannot do is judge a trend. A k6 threshold
# is a statement about the distribution of a metric over a whole run and carries
# no sense of when a sample was taken, so `max<256` cannot tell a flat 200 MiB
# apart from an hour spent climbing from 120 MiB to 250 MiB. The second is the
# leak and the first is not, and only the timestamps distinguish them.
#
# So this script runs the soak and keeps the series itself: it starts s8.js at
# the soak duration, polls resident memory beside it, and compares the mean of
# the first decile of samples against the mean of the last. It scrapes /metrics
# rather than reading k6's own output because the k6 sample stream at S2 rates
# is millions of rows an hour and this comparison needs a few hundred numbers.
# That both are polling is deliberate and not duplication: k6 samples often
# because a ceiling is only as tight as the peak it caught, and this samples
# sparsely because a trend is read from means and wants the noise averaged out.
#
# The k6 run and the trend comparison both gate: the exit status is a failure if
# either the ceiling was crossed or memory grew.
#
#   test/load/s8-driver.sh
#   SOAK=15m WARMUP=120 test/load/s8-driver.sh
#   BASE=http://host:8080 OPS=http://host:9090 test/load/s8-driver.sh

set -euo pipefail

# The comparison below is arithmetic over numbers Prometheus writes as
# 1.97656576e+08, with a dot. Under a locale whose decimal separator is a comma,
# awk reads that as 1 and the whole soak reduces to a set of ones that agree
# with each other perfectly — a leak gate that always passes, on the developer
# machines least likely to notice.
export LC_ALL=C

here=$(cd "$(dirname "$0")" && pwd)

BASE="${BASE:-http://localhost:8080}"
ADMIN="${ADMIN:-$BASE}"
OPS="${OPS:-http://localhost:9090}"
SOAK="${SOAK:-1h}"
RATE="${RATE:-15000}"
STUBS="${STUBS:-1000}"
RELOAD_EVERY="${RELOAD_EVERY:-60s}"
# Fifteen seconds over an hour is 240 samples, so each decile is a couple of
# dozen readings — enough for a mean to survive the sawtooth of a GC cycle.
INTERVAL="${INTERVAL:-15}"
# Resident memory climbs to its working set in the first minutes of any run: the
# stub bodies are touched as traffic reaches them, the arena grows to the peak
# the allocator needs, and the scavenger has not yet done a pass. Comparing
# against a first decile taken from inside that climb would report every healthy
# soak as a leak, so the warm-up is dropped before the deciles are cut.
WARMUP="${WARMUP:-300}"
# Not zero, and deliberately. RSS is quantised by the page scavenger and drifts
# a little with where a GC cycle happens to land, so demanding two means agree
# exactly would fail on noise. Five percent of a reading in the low hundreds of
# MiB is a handful of megabytes, far under the 256 MiB ceiling and far over the
# noise, while a real leak over an hour is monotone and much larger than that.
LEAK_PCT="${LEAK_PCT:-5}"
SAMPLES="${OUT:-$(mktemp "${TMPDIR:-/tmp}/mockulus-s8-soak-XXXXXX")}"

rss_now() {
  curl -sf --max-time 10 "$OPS/metrics" |
    awk '$1 == "process_resident_memory_bytes" { print $2; exit }'
}

# Fail in the first second rather than the last: a rig whose ops port is not
# reachable, or whose process collector reports no RSS because it is not Linux,
# produces an hour of nothing and a comparison over an empty file.
if ! curl -sf --max-time 10 "$OPS/readyz" >/dev/null; then
  echo "the ops port $OPS does not answer /readyz" >&2
  exit 2
fi
if [ -z "$(rss_now || true)" ]; then
  echo "$OPS/metrics exposes no process_resident_memory_bytes: the process" >&2
  echo "collector reports it on Linux only, and metrics_enabled must be on." >&2
  exit 2
fi

echo "S8 soak: $SOAK at $RATE rps over $STUBS stubs, reload every $RELOAD_EVERY"
echo "samples: every ${INTERVAL}s into $SAMPLES"

k6 run \
  -e "BASE=$BASE" -e "ADMIN=$ADMIN" -e "OPS=$OPS" \
  -e "DURATION=$SOAK" -e "RATE=$RATE" -e "STUBS=$STUBS" \
  -e "RELOAD_EVERY=$RELOAD_EVERY" \
  "$here/s8.js" &
k6_pid=$!
trap 'kill "$k6_pid" 2>/dev/null || true' INT TERM

: >"$SAMPLES"
missed=0
while kill -0 "$k6_pid" 2>/dev/null; do
  rss=$(rss_now || true)
  if [ -n "$rss" ]; then
    printf '%s\t%s\n' "$(date +%s)" "$rss" >>"$SAMPLES"
  else
    missed=$((missed + 1))
  fi
  sleep "$INTERVAL"
done

k6_status=0
wait "$k6_pid" || k6_status=$?
trap - INT TERM

if [ "$missed" -gt 0 ]; then
  echo "warning: $missed scrape(s) of $OPS/metrics failed and are missing from the series" >&2
fi

echo
leak_status=0
awk -F'\t' -v warmup="$WARMUP" -v tol="$LEAK_PCT" '
  # n is set here rather than left to default. An array subscript is a string in
  # awk, so the first sample of an uninitialised counter lands in t[""] and the
  # series then starts with a phantom zero — which is a first decile dragged
  # down by a reading that never happened, and a healthy soak reported as a leak.
  BEGIN { n = 0; first = 0; last = 0 }
  { t[n] = $1 + 0; v[n] = $2 / 1048576; n++ }
  END {
    if (n == 0) {
      print "the soak collected no samples at all" > "/dev/stderr"
      exit 2
    }

    peak = 0
    for (i = 0; i < n; i++) if (v[i] > peak) peak = v[i]

    cut = t[0] + warmup
    m = 0
    for (i = 0; i < n; i++) if (t[i] >= cut) { w[m] = v[i]; m++ }

    # Twenty samples is the point below which a decile stops being a decile and
    # becomes one reading with a mean drawn around it.
    if (m < 20) {
      printf "the soak kept %d sample(s) after the %ds warm-up; a decile comparison needs 20\n", m, warmup > "/dev/stderr"
      exit 2
    }

    d = int(m / 10)
    if (d < 1) d = 1
    for (i = 0; i < d; i++) first += w[i]
    for (i = m - d; i < m; i++) last += w[i]
    first /= d
    last /= d
    growth = (last - first) / first * 100

    printf "samples:          %d total, %d after the %ds warm-up\n", n, m, warmup
    printf "first decile:     %.1f MiB (mean of %d)\n", first, d
    printf "last decile:      %.1f MiB (mean of %d)\n", last, d
    printf "growth:           %+.1f%% (gate: +%.1f%%)\n", growth, tol
    printf "peak:             %.1f MiB (ceiling 256 MiB, gated by the k6 run)\n", peak

    if (growth > tol) {
      fflush()
      printf "S8 leak gate FAILED: resident memory grew %+.1f%% across the soak\n", growth > "/dev/stderr"
      exit 1
    }
    print "S8 leak gate passed"
  }
' "$SAMPLES" || leak_status=$?

if [ "$k6_status" -ne 0 ]; then
  echo "k6 exited $k6_status: the ceiling or a load threshold was missed" >&2
fi
if [ "$k6_status" -ne 0 ] || [ "$leak_status" -ne 0 ]; then
  exit 1
fi

echo "S8 passed: series in $SAMPLES"
