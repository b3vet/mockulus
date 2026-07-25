#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The coverage floor of SPEC §19.2: merged binary coverage from the E2E run
# alone must reach the floor over internal/. It is the secondary net — the
# behavior catalog measures contract coverage, and this catches behaviors the
# catalog forgot to name.
#
# The gate passes at floor - TOLERANCE so run-to-run noise cannot flake it,
# per the zero-flake policy.

set -euo pipefail

cd "$(dirname "$0")/.."

COVERDIR="${GOCOVERDIR:-.coverdata}"
FLOOR_FILE="test/e2e/coverage_floor.txt"
EXCLUDE_FILE="test/e2e/coverage_exclude.txt"
TOLERANCE="0.5"

if [ ! -d "$COVERDIR" ] || [ -z "$(ls -A "$COVERDIR" 2>/dev/null)" ]; then
  echo "no coverage data in $COVERDIR; run the gate with GOCOVERDIR set" >&2
  exit 1
fi

floor=$(grep -v '^#' "$FLOOR_FILE" | tr -d '[:space:]')

profile=$(mktemp)
trap 'rm -f "$profile"' EXIT
go tool covdata textfmt -i="$COVERDIR" -o="$profile"

# Drop excluded files. Each entry is justified and reviewed; the list should
# stay near-empty — the denominator is kept honest by adding coverage, not
# exclusions.
filtered=$(mktemp)
trap 'rm -f "$profile" "$filtered"' EXIT
head -1 "$profile" > "$filtered"
if [ -s "$EXCLUDE_FILE" ] && grep -qv '^#' "$EXCLUDE_FILE" 2>/dev/null; then
  grep -v '^mode:' "$profile" \
    | grep -vFf <(grep -v '^#' "$EXCLUDE_FILE" | grep -v '^$') >> "$filtered" || true
else
  grep -v '^mode:' "$profile" >> "$filtered" || true
fi

total=$(go tool cover -func="$filtered" | awk '/^total:/ {gsub(/%/,"",$3); print $3}')

echo "E2E coverage of internal/: ${total}%  (floor ${floor}%, tolerance ${TOLERANCE}pp)"

if awk -v t="$total" -v f="$floor" -v tol="$TOLERANCE" 'BEGIN { exit !(t < f - tol) }'; then
  echo >&2
  echo "Coverage is below the floor. Add cases rather than exclusions:" >&2
  go tool cover -func="$filtered" | sort -k3 -n | head -20 >&2
  exit 1
fi
