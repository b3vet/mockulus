// SPDX-License-Identifier: Apache-2.0

// Generates src/generated/types.ts from the repo's OpenAPI contract.
//
// The output is committed rather than generated at install time, for the same
// reason SPEC §13's configuration table and docs/compatibility.md are: a reader
// of this repository — and the admin UI, which imports this package from the
// workspace — should never need a generation step to see what the types are.
// `pnpm run gen:check` regenerates and diffs, so the committed copy cannot drift
// from the contract it claims to describe.
//
// Two things happen here that `openapi-typescript` does not do on its own. The
// SPDX header is prepended, because the file is committed and every committed
// source in this tree carries one. And the contract's path is resolved from this
// script rather than from the working directory, so the script behaves the same
// whether pnpm ran it from the package or from the workspace root.

import { execFileSync } from 'node:child_process';
import { readFileSync, writeFileSync, mkdirSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageRoot = join(dirname(fileURLToPath(import.meta.url)), '..');
const contract = join(packageRoot, '..', '..', 'api', 'openapi.yaml');
const output = join(packageRoot, 'src', 'generated', 'types.ts');

const HEADER = `// SPDX-License-Identifier: Apache-2.0

// Generated from api/openapi.yaml by \`pnpm run gen\`. Do not edit by hand: the
// drift gate regenerates this file and fails on a diff, so an edit here is a
// change that will be reverted by the next person who runs the generator.

`;

mkdirSync(dirname(output), { recursive: true });

// --default-non-nullable=false is load-bearing rather than a style choice.
// Left at its default, openapi-typescript treats every property that declares a
// `default:` as non-optional, on the reasoning that a server fills one in. That
// is true of a response and false of a request, and these schemas are shared
// between the two — `RequestPattern` is one type on both sides because the
// server literally reuses one type. So the request side has to win, or a caller
// is forced to pass `schemaVersion` on every matcher and `caseInsensitive` on
// every criterion in order to satisfy the compiler, spelling out the very
// defaults the contract documents so they need not be spelled out.
execFileSync('openapi-typescript', [contract, '--default-non-nullable', 'false', '-o', output], {
  cwd: packageRoot,
  stdio: 'inherit',
});

writeFileSync(output, HEADER + readFileSync(output, 'utf8'));

execFileSync('prettier', ['--write', output], { cwd: packageRoot, stdio: 'inherit' });
