#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Verifies that every mockulus-authored source file carries an SPDX header
# (SPEC §22.1). Vendored trees would keep their upstream headers and be excluded
# here; there are none, so nothing is.
#
# internal/handlebars used to be excluded as vendored, which it never became:
# SPEC §10.1 was resolved the other way and the engine is ours (see the package
# comment). An exclusion for a tree we wrote is a hole in the check that nobody
# reads as one, so the list is empty until a tree is genuinely vendored.
#
# That last sentence is why ui/ arrives here with no exemption of its own. The
# admin UI will eventually hold shadcn-svelte components copied in from
# upstream, carrying MIT notices rather than ours, and the SOW is right that
# those are a vendored tree in the sense this list exists for. But not one such
# file exists yet, and an exemption written ahead of the files it exempts is the
# hole above with a longer fuse: it would sit in the tree pre-authorising a
# directory, and the first mockulus-authored component someone drops in that
# same directory would sail past the check with no header and nobody the wiser.
# The entry costs one line on the day the first upstream component lands, and on
# that day the reviewer can see the MIT notice in the diff and confirm the path
# is the one that actually needs excluding. Until then the list stays empty,
# which is the only state in which it is telling the truth.

set -euo pipefail

cd "$(dirname "$0")/.."

# The trees below are pruned rather than filtered out afterwards: find never
# descends into a pruned directory at all, so this is a guarantee about what
# gets read rather than a claim about how good the pattern list is.
#
#   node_modules  installed packages — other people's sources, at any depth.
#                 The pnpm workspace has one at the root and one per package,
#                 so this matches by name and not by a fixed path.
#   dist trees    build output. internal/adminui/dist holds what vite emitted
#                 and dist/ holds what goreleaser assembled; neither contains a
#                 line anyone here wrote, and both can contain anything at all.
#   bin           compiled binaries, for the same reason.
#
# .ts and .svelte join .go and .sh with the admin UI: they are the languages the
# repo now authors source in, and a file this project wrote is held to the same
# bar whichever of the four it is written in.
missing=()
while IFS= read -r file; do
  if ! head -5 "$file" | grep -q 'SPDX-License-Identifier: Apache-2.0'; then
    missing+=("$file")
  fi
done < <(find . \
  \( -name node_modules \
     -o -name .git \
     -o -path './internal/adminui/dist' \
     -o -path './dist' \
     -o -path './bin' \
     -o -path './sdk/typescript/dist' \) -prune \
  -o -type f \( -name '*.go' -o -name '*.sh' -o -name '*.ts' -o -name '*.svelte' \) -print)

if [ ${#missing[@]} -gt 0 ]; then
  echo "Missing SPDX-License-Identifier header in ${#missing[@]} file(s):" >&2
  printf '  %s\n' "${missing[@]}" >&2
  echo >&2
  echo "Add this as the first line, in the file's own comment syntax:" >&2
  echo "  // SPDX-License-Identifier: Apache-2.0        (.go, .ts)" >&2
  echo "  # SPDX-License-Identifier: Apache-2.0         (.sh, below the shebang)" >&2
  echo "  <!-- SPDX-License-Identifier: Apache-2.0 -->  (.svelte)" >&2
  exit 1
fi

echo "SPDX headers present in all mockulus-authored sources."
