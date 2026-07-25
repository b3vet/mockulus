#!/usr/bin/env bash
# SPDX-License-Identifier: Apache-2.0
#
# Verifies that every mockulus-authored source file carries an SPDX header
# (SPEC §22.1). Vendored trees keep their upstream headers and are excluded.

set -euo pipefail

cd "$(dirname "$0")/.."

# Vendored code keeps upstream headers; see THIRD_PARTY_LICENSES.
EXCLUDE_DIRS='./internal/handlebars/*'

missing=()
while IFS= read -r file; do
  case "$file" in
    $EXCLUDE_DIRS) continue ;;
  esac
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
