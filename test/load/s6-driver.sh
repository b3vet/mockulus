#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# S6 of SPEC §16.1: an instance that starts with 10k stubs already in Couchbase
# must answer /readyz within 5 s. This is the half of the scenario k6 cannot do.
#
# The clock for a cold start starts before the process has an HTTP surface, and
# k6 can neither stop nor start the thing it is measuring, so the scenario is
# split between two programs rather than faked inside one. This script owns the
# lifecycle: it seeds the corpus, stops the instance, notes the instant it
# launched a new one, and hands that instant to s6.js as T0_MS. s6.js owns
# everything reachable over HTTP — the poll loop on /readyz, the proof that the
# instance really came up with the corpus loaded, and the threshold. The gate
# lives there and only there, so the two halves cannot disagree about whether a
# run passed; what this script adds is the bounded wait on every step it owns,
# so a rig that never comes up, never goes down, or never finishes booting
# fails the job with a message instead of hanging it.
#
# t0 is taken immediately after START_CMD returns, not before. A launch command
# is a docker or kubectl invocation with hundreds of milliseconds of its own
# overhead, and charging that to a 5 s budget would be measuring the CLI. The
# bias that leaves is small and in the safe direction — the instance has been
# alive for a moment by then, so the reported number is a slight under-report
# bounded by how long the command takes to hand back, which is why the overhead
# is printed and why an unusually slow launch command is called out.
#
# The corpus has to be in the store before the process starts and has to be
# gone afterwards, so every k6 phase runs with KEEP=true and cleanup happens
# once, in the exit trap. A shared load rig is a shared namespace (SPEC §5.1):
# the sweep selects on the suite metadata rather than calling a global reset,
# which would take S1's stubs with it.
#
#   START_CMD='docker compose -f my-cb-rig.yaml up -d mockulus' \
#   STOP_CMD='docker compose -f my-cb-rig.yaml stop mockulus' \
#   ./test/load/s6-driver.sh
#
# There is no checked-in rig to default to: test/load/compose.yaml is the
# memory-store rig S1 runs against, and a cold start over the memory driver has
# nothing to load. The commands are required rather than guessed, and the store
# driver is checked before any of it runs.

set -euo pipefail
export LC_ALL=C

here=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
script="$here/s6.js"

BASE="${BASE:-http://localhost:8080}"
ADMIN="${ADMIN:-http://localhost:9090}"
STUBS="${STUBS:-10000}"
MIX="${MIX:-s2}"
BATCH="${BATCH:-500}"
STORE="${STORE:-couchbase}"
REPEAT="${REPEAT:-1}"
K6="${K6:-k6}"

# Budgets. BUDGET_MS is the SLO and is here only to be printed — the pass/fail
# is s6.js's threshold, which is a constant in that file for the same reason a
# release criterion is not a command-line flag.
BUDGET_MS=5000
DEADLINE_MS="${DEADLINE_MS:-30000}"
SETTLE_TIMEOUT="${SETTLE_TIMEOUT:-120}"
STOP_TIMEOUT="${STOP_TIMEOUT:-60}"
LAUNCH_SLACK_MS="${LAUNCH_SLACK_MS:-500}"

START_CMD="${START_CMD:-}"
STOP_CMD="${STOP_CMD:-}"

if [ -z "$START_CMD" ] || [ -z "$STOP_CMD" ]; then
  echo "s6: START_CMD and STOP_CMD are required — this script has to own the" >&2
  echo "    instance lifecycle, and only you know what runs your rig." >&2
  echo "    START_CMD must return once the instance has been launched, not" >&2
  echo "    once it is ready; STOP_CMD must return once it is going away." >&2
  exit 2
fi

# now_ms is the one thing bash does not have. GNU date answers it directly, but
# BSD date passes %3N through unrecognised rather than failing, so the answer is
# checked for shape instead of trusted; bash 5's EPOCHREALTIME carries the same
# clock in microseconds, and python3 is the last resort. LC_ALL=C above is what
# keeps EPOCHREALTIME's decimal separator a dot under a comma locale.
now_ms() {
  local t
  t=$(date +%s%3N 2>/dev/null || true)
  case "$t" in
    '' | *[!0-9]*) ;;
    *)
      if [ "${#t}" -ge 13 ]; then
        printf '%s\n' "$t"
        return 0
      fi
      ;;
  esac
  if [ -n "${EPOCHREALTIME:-}" ]; then
    printf '%s\n' "$(( ${EPOCHREALTIME%.*} * 1000 + 10#${EPOCHREALTIME#*.} / 1000 ))"
    return 0
  fi
  python3 -c 'import time; print(int(time.time() * 1000))'
}

# probe answers the HTTP status of a URL, or 000 when nothing is listening.
# A booting instance walks 000 → 503 → 200, and the whole point of the wait
# loops below is to tell those three apart.
probe() {
  curl -s -o /dev/null -w '%{http_code}' --max-time 2 "$1" || true
}

wait_ready() {
  local deadline=$(( SECONDS + $1 ))
  while [ "$SECONDS" -lt "$deadline" ]; do
    [ "$(probe "$ADMIN/readyz")" = "200" ] && return 0
    sleep 0.2
  done
  return 1
}

# wait_gone waits for the listener to stop answering at all. A 503 is not gone:
# it is an instance that is still up, and starting the clock while the old one
# still holds the port would time a readiness flip rather than a cold start.
wait_gone() {
  local deadline=$(( SECONDS + $1 ))
  while [ "$SECONDS" -lt "$deadline" ]; do
    [ "$(probe "$ADMIN/readyz")" = "000" ] && return 0
    sleep 0.2
  done
  return 1
}

k6_run() {
  "$K6" run \
    -e "BASE=$BASE" -e "ADMIN=$ADMIN" \
    -e "STUBS=$STUBS" -e "MIX=$MIX" -e "BATCH=$BATCH" \
    -e "STORE=$STORE" -e "DEADLINE_MS=$DEADLINE_MS" \
    -e "KEEP=true" \
    "$@" "$script"
}

swept=0
sweep() {
  local rc=$?
  if [ "$swept" -eq 0 ]; then
    swept=1
    echo "s6: sweeping the corpus"
    # Best effort by design: the run's verdict has already been decided, and a
    # rig that went away underneath us must not turn a reported number into an
    # unreported crash. The sweep is idempotent, so the next run recovers.
    k6_run -e PHASE=sweep ||
      echo "s6: WARNING — the sweep failed; $STUBS stubs may still be on the rig" >&2
  fi
  return "$rc"
}

echo "s6: rig mock=$BASE admin=$ADMIN stubs=$STUBS mix=$MIX budget=${BUDGET_MS}ms"

if ! wait_ready "$SETTLE_TIMEOUT"; then
  echo "s6: $ADMIN/readyz did not answer 200 within ${SETTLE_TIMEOUT}s — the rig has to" >&2
  echo "    be up and ready before it can be seeded" >&2
  exit 1
fi

health=$(curl -sf --max-time 5 "$ADMIN/__admin/health" || true)
driver=$(printf '%s' "$health" | sed -n 's/.*"driver":"\([a-z]*\)".*/\1/p')
if [ -z "$driver" ]; then
  echo "s6: could not read the store driver from $ADMIN/__admin/health" >&2
  exit 1
fi
if [ "$driver" = "memory" ]; then
  echo "s6: the rig is running the memory store. A cold start over it loads nothing," >&2
  echo "    so there is no S6 measurement to take here — point the rig at Couchbase." >&2
  exit 1
fi
if [ "$driver" != "$STORE" ]; then
  echo "s6: NOTE — store is '$driver', and SPEC §16.1 states S6 against '$STORE'."
  echo "    The number below is a rig number, not the release-gate number."
fi

# The signal traps exist so that an interrupted run still exits through the
# sweep rather than dying on the spot and leaving $STUBS stubs on a rig every
# other scenario shares.
trap sweep EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

echo "s6: seeding $STUBS stubs"
k6_run -e PHASE=seed

for run in $(seq 1 "$REPEAT"); do
  echo "s6: run $run/$REPEAT — stopping the instance"
  eval "$STOP_CMD"
  if ! wait_gone "$STOP_TIMEOUT"; then
    echo "s6: the admin port was still answering ${STOP_TIMEOUT}s after STOP_CMD;" >&2
    echo "    a cold start cannot be timed against an instance that never went cold" >&2
    exit 1
  fi

  launch_started=$(now_ms)
  eval "$START_CMD"
  t0=$(now_ms)
  overhead=$(( t0 - launch_started ))
  echo "s6: launched at t0=$t0 (START_CMD took ${overhead}ms)"
  if [ "$overhead" -gt "$LAUNCH_SLACK_MS" ]; then
    echo "s6: WARNING — START_CMD took ${overhead}ms to return. That time is not" >&2
    echo "    charged to the cold start, so the number below under-reports by up" >&2
    echo "    to that much. A launch command that returns promptly measures better." >&2
  fi

  log=$(mktemp)
  rc=0
  k6_run -e PHASE=measure -e "T0_MS=$t0" 2>&1 | tee "$log" || rc=$?

  ms=$(sed -n 's/.*s6_cold_start_ms=\([0-9]*\).*/\1/p' "$log" | tail -1)
  rm -f "$log"

  if [ -z "$ms" ]; then
    echo "s6: run $run/$REPEAT FAILED — no measurement was taken" >&2
    if [ "$rc" -eq 0 ]; then
      rc=1
    fi
    exit "$rc"
  fi
  if [ "$rc" -ne 0 ]; then
    echo "s6: run $run/$REPEAT FAILED — cold start ${ms}ms against a ${BUDGET_MS}ms budget" >&2
    exit "$rc"
  fi
  echo "s6: run $run/$REPEAT passed — cold start ${ms}ms against a ${BUDGET_MS}ms budget"
done

echo "s6: $REPEAT run(s) inside the ${BUDGET_MS}ms budget"
