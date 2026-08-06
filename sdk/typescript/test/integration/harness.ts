// SPDX-License-Identifier: Apache-2.0

import { spawn, type ChildProcessByStdio } from 'node:child_process';
import type { Readable } from 'node:stream';
import { existsSync } from 'node:fs';
import { createInterface } from 'node:readline';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const packageRoot = join(dirname(fileURLToPath(import.meta.url)), '..', '..');
const repoRoot = join(packageRoot, '..', '..');

/** A mockulus process this suite started, and the addresses it reported. */
export interface Server {
  mockUrl: string;
  adminUrl: string;
  stop(): Promise<void>;
}

/**
 * The JSON the server writes on the line that says it is up. Ports are read
 * from here rather than assumed, because every instance is started on port 0 —
 * two suites on one machine must not collide, and a fixed port is also the
 * shape of the mistake that once had a probe recording answers from a stray
 * process that happened to be listening.
 */
interface StartupLine {
  msg?: string;
  mock_addr?: string;
  admin_addr?: string;
}

/** Where the binary is. Built by `make build`, or pointed at by the caller. */
export function binaryPath(): string {
  return process.env['MOCKULUS_BINARY'] ?? join(repoRoot, 'bin', 'mockulus');
}

/**
 * Starts a mockulus and waits for it to report its addresses.
 *
 * Every instance uses the memory store and port 0. `env` overlays whatever the
 * case needs — the journal, an admin token — on top of that.
 */
export async function startServer(env: Record<string, string> = {}): Promise<Server> {
  const binary = binaryPath();
  if (!existsSync(binary)) {
    throw new Error(
      `no mockulus binary at ${binary}. Run \`make build\` from the repository root, ` +
        `or set MOCKULUS_BINARY to one.`,
    );
  }

  // stdin is closed rather than piped — nothing here writes to the server —
  // which is what makes the process type null-stdin rather than the fully
  // piped one.
  const child: ChildProcessByStdio<null, Readable, Readable> = spawn(binary, [], {
    env: {
      ...process.env,
      MOCKULUS_PORT: '0',
      MOCKULUS_ADMIN_PORT: '0',
      MOCKULUS_LOG_FORMAT: 'json',
      // Shutdown is immediate: these cases start a process per file and a drain
      // window would be paid on every one of them for no benefit.
      MOCKULUS_SHUTDOWN_DRAIN: '0',
      ...env,
    },
    stdio: ['ignore', 'pipe', 'pipe'],
  });

  const logs: string[] = [];
  const collect = (chunk: Buffer) => {
    logs.push(chunk.toString());
  };
  child.stderr.on('data', collect);

  const addresses = await new Promise<{ mock: string; admin: string }>((resolve, reject) => {
    const lines = createInterface({ input: child.stdout });
    const timer = setTimeout(() => {
      lines.close();
      reject(new Error(`mockulus never reported a startup line within 30s.\n${logs.join('')}`));
    }, 30_000);

    child.once('exit', (code) => {
      clearTimeout(timer);
      reject(new Error(`mockulus exited with code ${code} before starting.\n${logs.join('')}`));
    });

    lines.on('line', (line) => {
      logs.push(line + '\n');
      let parsed: StartupLine;
      try {
        parsed = JSON.parse(line) as StartupLine;
      } catch {
        return;
      }
      if (parsed.msg === 'mockulus started' && parsed.mock_addr && parsed.admin_addr) {
        clearTimeout(timer);
        lines.close();
        resolve({ mock: parsed.mock_addr, admin: parsed.admin_addr });
      }
    });
  });

  const stop = () =>
    new Promise<void>((resolve) => {
      if (child.exitCode !== null) return resolve();
      child.once('exit', () => resolve());
      child.kill('SIGTERM');
      // A process that ignores SIGTERM must not hold the suite open. The Go
      // side has its own drain timeout; this is the backstop under it.
      setTimeout(() => child.kill('SIGKILL'), 10_000).unref();
    });

  return {
    mockUrl: `http://${normalize(addresses.mock)}`,
    adminUrl: `http://${normalize(addresses.admin)}`,
    stop,
  };
}

/**
 * Turns a listener address into something a client can dial.
 *
 * The server reports what it bound, which for the default wildcard bind is
 * `[::]:54321`. That is a valid thing to have bound and not a valid thing to
 * connect to, so the host is replaced with the loopback the test is on.
 */
function normalize(addr: string): string {
  const port = addr.slice(addr.lastIndexOf(':') + 1);
  return `127.0.0.1:${port}`;
}
