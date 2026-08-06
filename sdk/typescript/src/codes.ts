// SPDX-License-Identifier: Apache-2.0

/**
 * The error codes the mockulus admin API answers with, from SPEC Appendix B.
 *
 * These are the codes themselves rather than a way of handling them. The
 * `MockulusError` that carries one, and the guards that ask what it means,
 * arrive with the client. What is here is contract data: the numbers a caller
 * compares against, and the mapping to the HTTP status each is answered with.
 *
 * Two of them are WireMock's own — 10 and 109 — and they are its values rather
 * than ours because a client that already special-cases a WireMock code keeps
 * working. Everything at 1000 and above is mockulus'; WireMock has no code
 * there, so nothing can collide.
 *
 * An error body always carries an array. Every 422 lists *every* problem it
 * found rather than failing on the first, so a mapping with three unsupported
 * fields is one round trip and not three.
 */
export const ErrorCode = {
  /** Malformed JSON, or a document that violates the stub schema. */
  Malformed: 10,
  /** A create naming a stub id that already exists. */
  DuplicateStubId: 109,
  /** A real WireMock feature mockulus does not implement. The pointer names the field. */
  UnsupportedFeature: 1000,
  /** An admin endpoint outside the supported matrix. */
  UnsupportedEndpoint: 1001,
  /** An unknown template helper, or a template that does not parse. */
  Template: 1002,
  /** A regular expression that compiles on neither engine. */
  Regex: 1003,
  /** An unknown `transformers` entry. */
  UnknownTransformer: 1004,
  /** An unknown key in a settings write. */
  UnknownSetting: 1005,
  /** A `matchesJsonSchema` operand that is JSON but not a usable schema. */
  InvalidSchema: 1006,
  /** The request journal is disabled, so a journal-backed call cannot be answered. */
  JournalDisabled: 1010,
  /** The stub store is unavailable, so an admin write cannot be durable. */
  StoreUnavailable: 1020,
  /** Scenario state could not be read or written. */
  ScenarioUnavailable: 1021,
  /** A stub's `bodyFileName` names a file that is not there. */
  BodyFileMissing: 1022,
  /** The request body exceeds `max_body_bytes`. */
  BodyTooLarge: 1030,
  /** An unknown scenario, or a state that scenario has no transition to. */
  InvalidScenarioState: 1031,
} as const;

/** One of the codes in {@link ErrorCode}. */
export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode];

/**
 * The HTTP status each code is answered with.
 *
 * The pairing is fixed by the server: a code determines its status, so a caller
 * that has one can reason about the other without a second lookup. It is worth
 * knowing that several codes share a status — five different things answer 422
 * — which is why the code rather than the status is what a caller should branch
 * on.
 */
export const ErrorCodeStatus: Readonly<Record<ErrorCodeValue, number>> = {
  [ErrorCode.Malformed]: 422,
  [ErrorCode.DuplicateStubId]: 422,
  [ErrorCode.UnsupportedFeature]: 422,
  [ErrorCode.UnsupportedEndpoint]: 404,
  [ErrorCode.Template]: 422,
  [ErrorCode.Regex]: 422,
  [ErrorCode.UnknownTransformer]: 422,
  [ErrorCode.UnknownSetting]: 422,
  [ErrorCode.InvalidSchema]: 422,
  [ErrorCode.JournalDisabled]: 500,
  [ErrorCode.StoreUnavailable]: 503,
  [ErrorCode.ScenarioUnavailable]: 500,
  [ErrorCode.BodyFileMissing]: 500,
  [ErrorCode.BodyTooLarge]: 413,
  [ErrorCode.InvalidScenarioState]: 400,
};
