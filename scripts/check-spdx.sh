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

set -euo pipefail

cd "$(dirname "$0")/.."

missing=()
while IFS= read -r file; do
  if ! head -5 "$file" | grep -q 'SPDX-License-Identifier: Apache-2.0'; then
    missing+=("$file")
  fi
done < <(find . -type f \( -name '*.go' -o -name '*.sh' \) -not -path './.git/*')

if [ ${#missing[@]} -gt 0 ]; then
  echo "Missing SPDX-License-Identifier header in ${#missing[@]} file(s):" >&2
  printf '  %s\n' "${missing[@]}" >&2
  echo >&2
  echo "Add this as the first line:" >&2
  echo "  // SPDX-License-Identifier: Apache-2.0" >&2
  exit 1
fi

echo "SPDX headers present in all mockulus-authored sources."
