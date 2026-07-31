// SPDX-License-Identifier: Apache-2.0

import { ErrorCode, type ErrorCodeValue } from './codes.js';

/**
 * One problem the server found, as it appears in an error envelope.
 *
 * `source.pointer` is the JSON Pointer of the offending element — the field that
 * makes a 422 actionable, because it names where in the document to look rather
 * than only what was wrong.
 */
export interface MockulusProblem {
  code: number;
  title?: string;
  detail?: string;
  source?: { pointer?: string };
}

/**
 * A non-2xx answer from the admin API.
 *
 * Every problem the server found is carried, not just the first: mockulus
 * collects the whole list before answering, so a mapping with three unsupported
 * fields is one round trip and three entries here rather than three round trips.
 * Reading only `problems[0]` throws that away.
 *
 * Branch on {@link code} rather than on {@link status}. Five distinct problems
 * answer 422, and the code is what tells them apart; the guards below are the
 * cases worth naming.
 */
export class MockulusError extends Error {
  /** The HTTP status the request was answered with. */
  readonly status: number;
  /** Every problem the server reported, in the order it reported them. */
  readonly problems: readonly MockulusProblem[];
  /** The method and path that were called, for a message worth reading. */
  readonly method: string;
  readonly path: string;
  /** The raw body, when it was not an error envelope this could parse. */
  readonly body: string | undefined;

  constructor(init: {
    status: number;
    method: string;
    path: string;
    problems?: readonly MockulusProblem[];
    body?: string;
  }) {
    super(describe(init));
    this.name = 'MockulusError';
    this.status = init.status;
    this.problems = init.problems ?? [];
    this.method = init.method;
    this.path = init.path;
    this.body = init.body;
  }

  /**
   * The first problem's code, which is the one to branch on when a call can
   * only fail one way. `undefined` when the answer carried no envelope — a
   * bodyless 404 or a proxy's own error page.
   */
  get code(): number | undefined {
    return this.problems[0]?.code;
  }

  /** Whether any problem carries this code. */
  has(code: ErrorCodeValue): boolean {
    return this.problems.some((p) => p.code === code);
  }

  /**
   * The request journal is off, which is the default. Every journal-backed call
   * answers this until `journal_enabled` is set, and it is a configuration
   * answer rather than a failure — worth its own branch because the fix is a
   * deployment change and no amount of retrying will help.
   */
  get isJournalDisabled(): boolean {
    return this.has(ErrorCode.JournalDisabled);
  }

  /**
   * The stub store is unavailable, so an admin write cannot be made durable.
   * Reads keep working from the in-memory snapshot, which is the point of the
   * degraded mode: this is the one class here worth retrying.
   */
  get isStoreUnavailable(): boolean {
    return this.has(ErrorCode.StoreUnavailable);
  }

  /**
   * The document used a real WireMock feature mockulus does not implement.
   * `pointers()` names the fields; the server's detail links the roadmap.
   */
  get isUnsupportedFeature(): boolean {
    return this.has(ErrorCode.UnsupportedFeature);
  }

  /** The path is not part of the supported admin surface. */
  get isUnsupportedEndpoint(): boolean {
    return this.has(ErrorCode.UnsupportedEndpoint);
  }

  /** A create named a stub id that already exists. */
  get isDuplicateStubId(): boolean {
    return this.has(ErrorCode.DuplicateStubId);
  }

  /** The admin token is missing or wrong. */
  get isUnauthorized(): boolean {
    return this.status === 401;
  }

  /**
   * Every JSON Pointer the server named, which for a rejected mapping is the
   * list of fields to fix.
   */
  pointers(): string[] {
    return this.problems.map((p) => p.source?.pointer).filter((p): p is string => Boolean(p));
  }
}

function describe(init: {
  status: number;
  method: string;
  path: string;
  problems?: readonly MockulusProblem[];
  body?: string;
}): string {
  const head = `${init.method} ${init.path} answered ${init.status}`;
  const problems = init.problems ?? [];
  if (problems.length === 0) {
    // A bodyless answer, or one this could not parse. Saying so is more useful
    // than an empty problem list the caller has to interpret.
    const raw = init.body?.trim();
    return raw ? `${head}: ${truncate(raw, 200)}` : `${head} with no error body`;
  }
  // Every problem, not the first. A 422 that names three fields and reports one
  // sends the reader back for another round trip, which is the thing mockulus'
  // collect-all envelope exists to prevent.
  const detail = problems
    .map((p) => {
      const where = p.source?.pointer ? ` at ${p.source.pointer}` : '';
      return `[${p.code}]${where} ${p.detail ?? p.title ?? ''}`.trimEnd();
    })
    .join('; ');
  return `${head}: ${detail}`;
}

function truncate(s: string, max: number): string {
  return s.length <= max ? s : `${s.slice(0, max)}…`;
}

/** Narrows an unknown caught value to a {@link MockulusError}. */
export function isMockulusError(err: unknown): err is MockulusError {
  return err instanceof MockulusError;
}
