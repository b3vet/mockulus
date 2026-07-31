#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# The npm half of the license gate of SPEC §22.1. `go-licenses` resolves the Go
# module graph and stops there, so nothing under the pnpm workspace is visible
# to it: not the toolchain that builds the admin UI, and not the packages whose
# code vite inlines into the bundle that ends up inside the shipped binary. This
# script is the other half of the same gate, held to the same policy — permissive
# licenses only, no copyleft, and a license the tool cannot name is a failure
# rather than a shrug.
#
# Why every package and not only `dependencies`: ui/package.json declares no
# runtime dependencies at all. Svelte is a compiler and Tailwind is a build step,
# so what reaches the browser is code that devDependencies emitted or inlined.
# A gate that honoured the dependencies/devDependencies split would therefore
# check nothing whatsoever today while looking like it checked everything, which
# is worse than not having one. The split is ignored; every resolved package is
# held to the allowlist.

set -euo pipefail

cd "$(dirname "$0")/.."

# The allowlist.
#
# The first five entries are the Go gate's list verbatim (Makefile's
# LICENSE_ALLOWLIST), kept identical so that one repository does not end up
# running two different license policies depending on which language a
# dependency happens to be written in. The remainder are the permissive families
# an npm tree surfaces and a Go one does not:
#
#   MIT-0          MIT with the attribution clause struck out. Strictly fewer
#                  obligations than MIT, which is already allowed.
#   0BSD           the same idea from the BSD side; public-domain-equivalent,
#                  and how a large part of the ecosystem publishes tiny
#                  packages.
#   CC0-1.0        a public domain dedication. It grants no patent rights, which
#                  is a real caveat for code and not one for the data-only
#                  packages that use it here (mdn-data is a table of CSS
#                  property names).
#   BlueOak-1.0.0  OSI-approved and permissive, and unlike MIT it carries an
#                  express patent grant — permissive in the way Apache-2.0 is,
#                  with a shorter notice requirement. Several packages that
#                  half the ecosystem depends on (lru-cache, minimatch) moved
#                  to it.
#
# Everything else fails, and that deliberately includes SPDX expressions such as
# "(MIT OR Apache-2.0)". A dual license is a choice somebody has to make and
# record, not a string to pattern-match past, so it either arrives in the
# exception list below with the choice written down or it does not arrive.
ALLOWED=(
  Apache-2.0
  MIT
  BSD-2-Clause
  BSD-3-Clause
  ISC
  MIT-0
  0BSD
  CC0-1.0
  BlueOak-1.0.0
)

# Per-package exceptions, as "<name-glob>|<license>". Each one has to state why
# a package outside the allowlist is nonetheless safe to depend on. The glob is
# there because packages that ship a prebuilt native binary publish one npm
# package per target triple, and a dozen near-identical lines would hide the
# argument in the noise.
#
# lightningcss is Tailwind v4's CSS transformer: a Rust binary the build shells
# out to. MPL-2.0 is file-level copyleft over MPL-licensed *files*, and the CSS
# it emits from our stylesheets is not one of them, so nothing it touches
# becomes subject to it and none of it is linked into the bundle or the Go
# binary. The exception is scoped to the package name for exactly that reason —
# an MPL-2.0 package that did end up inside the shipped bundle would still fail
# this gate, which is the distinction worth keeping.
#
# argparse and type-fest arrive together, under openapi-typescript, which turns
# api/openapi.yaml into the SDK's committed types. Neither can reach anything we
# ship: the generator runs at development time, its output is TypeScript we then
# read and commit, and no line of either package is in that output, in the admin
# UI bundle or in the Go binary.
#
# argparse is Python-2.0 — the Python Software Foundation license, permissive
# and imposing nothing on a caller — and reaches us through js-yaml, which the
# generator uses to read the contract.
#
# type-fest publishes as "(MIT OR CC0-1.0)", which is the dual-license case this
# gate refuses to pattern-match past. The choice is therefore recorded here
# rather than shrugged at: **we take type-fest under MIT**, which is on the
# allowlist above and is the term the rest of this tree's dependencies are used
# under. CC0-1.0 would also have been acceptable; the point is that somebody
# chose, and wrote it down.
EXCEPTIONS=(
  "lightningcss*|MPL-2.0"
  "argparse|Python-2.0"
  "type-fest|(MIT OR CC0-1.0)"
)

if ! command -v pnpm >/dev/null 2>&1; then
  echo "pnpm is not on PATH. Install the Node version in .nvmrc and run" >&2
  echo "'corepack enable'; the pnpm version follows package.json." >&2
  exit 1
fi

if [ ! -d node_modules ]; then
  echo "no node_modules; run 'pnpm install' (or 'make ui-build') first" >&2
  exit 1
fi

is_allowed() {
  local license=$1 entry
  for entry in "${ALLOWED[@]}"; do
    if [ "$entry" = "$license" ]; then
      return 0
    fi
  done
  return 1
}

is_excepted() {
  local name=$1 license=$2 entry pattern permitted
  if [ ${#EXCEPTIONS[@]} -eq 0 ]; then
    return 1
  fi
  for entry in "${EXCEPTIONS[@]}"; do
    pattern=${entry%%|*}
    permitted=${entry##*|}
    # The pattern is unquoted so bash treats it as a glob, which is the point.
    # shellcheck disable=SC2053
    if [ "$license" = "$permitted" ] && [[ $name == $pattern ]]; then
      return 0
    fi
  done
  return 1
}

# `pnpm licenses list` reports a map of license name to the packages carrying
# it, over everything the lockfile resolved for this workspace.
listing=$(pnpm licenses list --json)

# node reads the JSON because this script cannot run at all without it — pnpm is
# a Node program — so parsing with node adds no dependency the gate did not
# already have. jq would add one, and it is not installed everywhere this runs.
#
# The program is single-quoted deliberately: ${license} and ${pkg.name} below are
# JavaScript template-literal placeholders, and the shell must keep its hands off
# them. shellcheck cannot tell that from the outside, hence the exemption.
# shellcheck disable=SC2016
flattened=$(printf '%s' "$listing" | node -e '
  const byLicense = JSON.parse(require("node:fs").readFileSync(0, "utf8"));
  for (const [license, packages] of Object.entries(byLicense)) {
    for (const pkg of packages) {
      const versions = (pkg.versions || []).join(",");
      process.stdout.write(`${license}\t${pkg.name}\t${versions}\n`);
    }
  }
')

total=0
denied=()
excepted=()

while IFS=$'\t' read -r license name versions; do
  if [ -z "$name" ]; then
    continue
  fi
  total=$((total + 1))

  if is_allowed "$license"; then
    continue
  fi

  if is_excepted "$name" "$license"; then
    excepted+=("$name@$versions ($license)")
    continue
  fi

  denied+=("$name@$versions ($license)")
done <<<"$flattened"

if [ "$total" -eq 0 ]; then
  echo "pnpm resolved no packages; the install is incomplete, which is not a pass" >&2
  exit 1
fi

# Exceptions are printed on every run, passing or failing. A gate whose
# exceptions are only visible to whoever opens the script is a gate that quietly
# stops meaning what its name says.
if [ ${#excepted[@]} -gt 0 ]; then
  echo "Permitted by a named exception (see the argument in $0):"
  printf '  %s\n' "${excepted[@]}"
fi

if [ ${#denied[@]} -gt 0 ]; then
  echo >&2
  echo "${#denied[@]} npm package(s) carry a license that is not on the allowlist:" >&2
  printf '  %s\n' "${denied[@]}" >&2
  echo >&2
  echo "Allowed: ${ALLOWED[*]}" >&2
  echo >&2
  echo "Drop the dependency, or add a named exception to $0 with the reason it" >&2
  echo "cannot reach the shipped bundle. Do not widen the allowlist to make one" >&2
  echo "package pass — that decision applies to every package that follows it." >&2
  exit 1
fi

echo "npm licenses: $total package(s), all permissively licensed."
