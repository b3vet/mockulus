// SPDX-License-Identifier: Apache-2.0

/**
 * The matcher vocabulary, under the names WireMock's Java DSL gives it.
 *
 * One criterion over a value, used identically in `bodyPatterns`, as the value
 * of a header, query parameter, cookie, path parameter or form parameter, by
 * verification criteria and by `find-by-metadata` — there is one definition of
 * what "matches" means across the product (SPEC §5.2), so there is one set of
 * functions here rather than one per position.
 *
 * Two rules shape every signature below, and both come from the server rather
 * than from taste.
 *
 * **A modifier is written where its matcher is.** `caseInsensitive`,
 * `ignoreArrayOrder`, `ignoreExtraElements`, `schemaVersion`,
 * `truncateExpected`, `truncateActual`, `applyTruncationLast` and
 * `actualFormat` are not matchers; the server refuses the ones that have
 * nothing to modify, and WireMock accepts every one of them anywhere and drops
 * it silently, so a misspelled modifier there reads as though it had been
 * applied (deviations #49, #53). Making each modifier a parameter of the
 * function that emits its matcher is what makes the refused document
 * unwritable: there is no `schemaVersion()` to call on its own, so there is no
 * way to attach one to an `equalTo`.
 *
 * **A document carries one matcher key.** mockulus reads several keys on one
 * document as a conjunction; WireMock honours only the first key its binding
 * visits and discards the rest (deviation #26). Every function here emits a
 * single-key document, and {@link and} is how a conjunction is written, so a
 * stub these builders produce means the same thing on both servers.
 */

import type { ContentMatcher } from '../types.js';
import { asBase64 } from './base64.js';

/**
 * The phantom key that tells a matcher this package built from an object that
 * happens to have the same fields.
 *
 * It exists only in the type system — nothing ever writes it, so a built
 * matcher serialises to exactly the JSON the contract describes and nothing
 * more. What it buys is that the guarantees this module enforces cannot be
 * sidestepped by handing a raw document to {@link Matcher}-typed parameter: a
 * `{ equalTo: 'x', schemaVersion: 'V7' }` literal is a valid
 * {@link ContentMatcher} and a 422, and it is not a `Matcher`.
 *
 * `declare const` rather than a real symbol because there is nothing to create:
 * a value would be a runtime cost paid for a compile-time distinction.
 */
declare const position: unique symbol;

/**
 * A criterion accepted in every matcher position.
 *
 * It is a {@link ContentMatcher} — assignable anywhere the contract's document
 * type is wanted, including `client.mappings.findByMetadata` — with a marker
 * saying where it may appear.
 */
export interface Matcher extends ContentMatcher {
  readonly [position]: 'any';
}

/**
 * A criterion accepted only as a top-level entry in `bodyPatterns`.
 *
 * {@link binaryEqualTo} is the whole of this category. It compares the
 * subject's raw bytes, and WireMock declares its combinators and the object
 * form of `matchesJsonPath` over string patterns, so a `binaryEqualTo` nested
 * inside `not`, `and`, `or` or a JSONPath criterion is refused there and here —
 * as is one used as the value of a header, query parameter or cookie, where
 * there are no raw bytes to compare against.
 */
export interface BodyOnlyMatcher extends ContentMatcher {
  readonly [position]: 'body';
}

/** What a `bodyPatterns` entry may be: any matcher, plus the byte-oriented one. */
export type BodyPattern = Matcher | BodyOnlyMatcher;

/**
 * The JSON Schema drafts `matchesJsonSchema` compiles under.
 *
 * Read off the contract's own field rather than restated, so the day a draft is
 * added or removed the set here moves with it instead of drifting quietly out
 * of date.
 */
export type JsonSchemaVersion = NonNullable<ContentMatcher['schemaVersion']>;

/**
 * WireMock's `DateTimeTruncation` values, reached through the field that takes
 * one for the same no-second-copy reason as {@link JsonSchemaVersion}.
 *
 * `LAST_DAY_OF_*` zeroes the time of day exactly as the `FIRST_` values do — it
 * names a day, not the end of one.
 */
export type DateTimeTruncation = NonNullable<ContentMatcher['truncateExpected']>;

/** The units a now-relative operand may be written in. There is no `weeks`. */
export type TemporalUnit = 'seconds' | 'minutes' | 'hours' | 'days' | 'months' | 'years';

/** The three spellings of a signed count a now-relative operand accepts. */
type SignedCount = `${number}` | `+${number}` | `-${number}`;

/**
 * An operand that resolves against the clock at match time rather than naming a
 * fixed moment.
 *
 * The spacing is exact on the server, because it is exact in WireMock: `now`,
 * `now ±N units`, and a bare `±N units` with the keyword left off. `now+2days`,
 * `now + 2 days`, a doubled space or a singular unit all register on WireMock
 * and then never match, and are refused here at registration (deviation #49).
 *
 * The type exists to carry one fact into the signatures below —
 * `truncateExpected` applies to a now-relative operand and to nothing else —
 * rather than to validate an operand, which is the server's job and happens
 * against the clock. {@link nowOffset} is the way to build one from values
 * computed at runtime, where a literal type cannot help.
 */
export type NowRelative =
  'now' | `now ${SignedCount} ${TemporalUnit}` | `${SignedCount} ${TemporalUnit}`;

/**
 * How the request's own value is read, and whether it may be truncated.
 *
 * The two are one decision because the server refuses their inert combination:
 * `truncateActual` truncates a value that parsed to a zoned instant, which a
 * custom pattern never yields — only ISO-8601, `unix` and `epoch` do — so a
 * truncation beside a pattern would be a parameter that could not take effect
 * (deviation #50).
 */
type ActualReading =
  | {
      /**
       * Reads the request's value as a count rather than as ISO-8601: `unix` is
       * seconds and `epoch` is milliseconds. It **replaces** ISO parsing rather
       * than extending it.
       */
      actualFormat?: 'unix' | 'epoch';
      /** Truncates the request's value before comparing. */
      truncateActual?: DateTimeTruncation;
    }
  | {
      /**
       * A Java date pattern such as `dd/MM/yyyy`, replacing ISO-8601 parsing of
       * the request's value. A pattern naming no date or time field can never
       * match and is refused at registration.
       */
      actualFormat: string;
      truncateActual?: never;
    };

/**
 * What may be said about a now-relative expected value.
 *
 * `applyTruncationLast` chooses whether the truncation or the offset is applied
 * first, so without a truncation there is no order to choose and the server
 * refuses it. The two spellings are one union member each, which is what makes
 * the second case a type error rather than a 422.
 */
export type RelativeDateTimeOptions = ActualReading &
  (
    | {
        /**
         * Truncates the expected value — the `now ±N units` one — before
         * comparing.
         */
        truncateExpected: DateTimeTruncation;
        /**
         * Applies the truncation after the offset rather than before it. The
         * order is observable: with `now +3 days` and a first-day-of-month
         * truncation the two orderings name different days.
         */
        applyTruncationLast?: boolean;
      }
    | { truncateExpected?: never; applyTruncationLast?: never }
  );

/**
 * What may be said about a literal expected value — everything except the
 * expected-side truncation.
 *
 * `truncateExpected` has no effect on a literal date-time, which is a
 * statically detectable case and so a refusal on the server rather than a
 * silence (deviation #50). A caller whose now-relative operand is computed at
 * runtime, and therefore typed `string` rather than as a literal, lands here:
 * {@link nowOffset} is the way back, because its return type says what it built.
 */
export type LiteralDateTimeOptions = ActualReading;

/**
 * The options a date-time matcher takes, chosen by the operand it was given.
 *
 * The conditional is what makes the expected-side truncation available on
 * `before('now +3 days', …)` and unavailable on
 * `before('2021-06-14T00:00:00Z', …)` without two overloads and the resolution
 * failures they produce when neither fits.
 */
type DateTimeOptionsFor<Expected extends string> = Expected extends NowRelative
  ? RelativeDateTimeOptions
  : LiteralDateTimeOptions;

/**
 * Marks a document as a matcher built here.
 *
 * The assertion is the one place in this module where the phantom key is
 * conjured, and it is a widening of nothing: the key does not exist at runtime,
 * so the object returned is the document that was passed in.
 */
function anywhere(document: ContentMatcher): Matcher {
  return document as Matcher;
}

/** {@link anywhere} for the byte-oriented matcher, which nests nowhere. */
function bodyOnly(document: ContentMatcher): BodyOnlyMatcher {
  return document as BodyOnlyMatcher;
}

/** Exact string equality. */
export function equalTo(value: string): Matcher {
  return anywhere({ equalTo: value });
}

/**
 * Exact string equality with case folded.
 *
 * A separate function rather than a flag on {@link equalTo} because
 * `caseInsensitive` is read by `equalTo` and by nothing else: as a parameter it
 * could be written beside a `contains`, where the server accepts it — WireMock
 * does too — and it would then do nothing at all.
 *
 * The folding is Unicode simple case folding, where Java folds per UTF-16 code
 * unit. The two disagree in both directions and neither is more correct
 * (deviation #43), so a criterion that leans on the folding of a non-ASCII
 * script is worth checking against both servers.
 */
export function equalToIgnoreCase(value: string): Matcher {
  return anywhere({ equalTo: value, caseInsensitive: true });
}

/**
 * Exact byte equality, against bytes rather than against their encoding.
 *
 * The wire field is base64 and this takes the bytes, because base64 is a
 * transport spelling and not something a caller should have to produce
 * correctly. A caller who already holds the encoded form may pass it as a
 * string; it is validated here rather than at registration, so a typo in a
 * fixture is a `TypeError` at the call site instead of a 422 from the server.
 *
 * Only valid as a top-level `bodyPatterns` entry — see {@link BodyOnlyMatcher}.
 */
export function binaryEqualTo(value: Uint8Array | ArrayBuffer | string): BodyOnlyMatcher {
  return bodyOnly({ binaryEqualTo: asBase64(value, 'binaryEqualTo') });
}

/** The subject contains this substring. */
export function containing(value: string): Matcher {
  return anywhere({ contains: value });
}

/**
 * The subject does not contain this substring.
 *
 * Over a repeated header or query parameter this is any-of like every other
 * matcher, and therefore **not** the complement of {@link containing}: a key
 * carrying `a` and `b` satisfies both at once (SPEC §6.6).
 */
export function notContaining(value: string): Matcher {
  return anywhere({ doesNotContain: value });
}

/**
 * A Java-compatible regular expression, matched in full against the subject.
 *
 * WireMock anchors these, with DOTALL on and MULTILINE off, and so does the
 * server. A pattern RE2 cannot express runs on a fallback engine under a match
 * timeout; one that compiles on neither is refused 422 with code 1003, which is
 * a check no builder can make without shipping a regex engine.
 */
export function matching(pattern: string): Matcher {
  return anywhere({ matches: pattern });
}

/** The negated form of {@link matching}, any-of over a repeated key like its twin. */
export function notMatching(pattern: string): Matcher {
  return anywhere({ doesNotMatch: pattern });
}

/** How {@link equalToJson} compares, beyond structural equality. */
export interface EqualToJsonOptions {
  /**
   * Gives up array positions and keeps the count. Together with
   * {@link EqualToJsonOptions.ignoreExtraElements} an expected array becomes a
   * subset test resolved by maximum matching.
   */
  ignoreArrayOrder?: boolean;
  /**
   * Forgives elements the expected document never accounted for, in **arrays as
   * well as objects**. Positionally those are the ones past the end, so expected
   * `[1,2]` accepts `[1,2,3]` and still refuses `[3,1,2]`, and an actual array
   * shorter than the expected one remains a mismatch.
   */
  ignoreExtraElements?: boolean;
}

/**
 * Structural JSON equality against an expected document.
 *
 * Numbers are compared by value, so `1` equals `1.0`. The document may be
 * written inline — the ordinary case, and the one a TypeScript caller wants —
 * or as an escaped JSON string, which is the spelling WireMock's own examples
 * use and which round-trips through this function unchanged.
 *
 * json-unit placeholders inside the document are interpreted as WireMock
 * interprets them; {@link JsonUnit} spells them so a typo is not a stub that
 * never matches. An **unrecognised** placeholder is refused at registration
 * rather than compared as literal text (deviation #5).
 *
 * The operand is read by Go's strict JSON reader, so a document with trailing
 * content, single-quoted names or comments registers on WireMock and is refused
 * here (deviation #35) — which only arises for the escaped-string spelling,
 * since anything `JSON.stringify` can carry is already strict.
 */
export function equalToJson(document: unknown, options: EqualToJsonOptions = {}): Matcher {
  return anywhere({
    equalToJson: definedOperand(document, 'equalToJson'),
    ...(options.ignoreArrayOrder === undefined
      ? {}
      : { ignoreArrayOrder: options.ignoreArrayOrder }),
    ...(options.ignoreExtraElements === undefined
      ? {}
      : { ignoreExtraElements: options.ignoreExtraElements }),
  });
}

/**
 * A JSONPath criterion, bare or with an inner matcher.
 *
 * The **bare form** — no `inner` — matches when the path selects a present,
 * non-empty result. Emptiness applies to collections only: an empty array and
 * an empty object do not match, but an empty *string* does, as do `false` and
 * `0`. A selected `null` never matches, and neither does a path that selects
 * nothing.
 *
 * The **object form** is any-of over the selected values, each rendered to text
 * first — `5` becomes `"5"` and `5.0` becomes `"5.0"`. A path selecting an
 * array *node* evaluates the inner matcher against the node rather than
 * element-wise, so `matchingJsonPath('$.tags', equalTo('red'))` does not match
 * `{"tags":["red"]}` (deviation #42).
 *
 * The inner matcher is a {@link Matcher} rather than a {@link BodyPattern}
 * because the nested position is declared over string patterns: a
 * `binaryEqualTo` there is refused on both servers.
 */
export function matchingJsonPath(expression: string, inner?: Matcher): Matcher {
  return anywhere({ matchesJsonPath: jsonPathCriterion(expression, inner) });
}

/**
 * The negation of {@link matchingJsonPath}, in either form.
 *
 * The whole criterion is negated, so a path that selects nothing satisfies
 * this — which is what makes it the right way to say "no element here looks
 * like that" and the wrong way to say "an element here exists and does not".
 */
export function notMatchingJsonPath(expression: string, inner?: Matcher): Matcher {
  return anywhere({ doesNotMatchJsonPath: jsonPathCriterion(expression, inner) });
}

/**
 * Builds the criterion both JSONPath matchers take.
 *
 * The bare string is emitted when there is no inner matcher rather than an
 * object carrying only an expression: the two mean the same thing to the
 * server, and the string is the spelling a WireMock corpus is written in.
 */
function jsonPathCriterion(
  expression: string,
  inner: Matcher | undefined,
): NonNullable<ContentMatcher['matchesJsonPath']> {
  if (inner === undefined) return expression;
  return { ...inner, expression };
}

/** How {@link matchingJsonSchema} compiles its operand. */
export interface JsonSchemaOptions {
  /**
   * The draft to compile the schema under, defaulting to `V202012`. The set is
   * exact and case-sensitive, as it is in WireMock.
   *
   * **`format` is asserted only under `V4`, `V6` and `V7`.** 2019-09 and
   * 2020-12 treat `format` as an annotation, which is those drafts' own
   * vocabulary boundary and means the default asserts nothing about it — a
   * schema written to constrain an email address or a date matches every string
   * until this is set to one of the three.
   */
  schemaVersion?: JsonSchemaVersion;
}

/**
 * Validates the subject against an embedded JSON Schema.
 *
 * The draft comes from {@link JsonSchemaOptions.schemaVersion}, and the
 * document's own `$schema` overrides that field in both directions — a schema
 * declaring 2020-12 is compiled as 2020-12 whatever this call says, and one
 * declaring draft-07 is compiled as draft-07 even under the default.
 *
 * `$ref` resolves **within the document only**. A schema that does not compile,
 * a `schemaVersion` outside the five spellings, an unrecognised `$schema` URI
 * and a `$ref` pointing outside the document are all refused 422 with code
 * 1006; WireMock validates only that the operand is JSON, so `{"type":"banana"}`
 * registers there and then matches nothing ever (deviation #56).
 *
 * A subject that is not a JSON document is a plain non-match. WireMock falls
 * back to validating the raw request text as a JSON *string*, which makes a
 * schema and its own negation both hold for the body `4` (deviation #55).
 *
 * `schemaVersion` is a parameter here rather than a matcher of its own because
 * the server refuses it beside anything but this matcher, and WireMock drops it
 * silently — before validating it, so `{"equalTo":"x","schemaVersion":"BANANA"}`
 * registers cleanly there.
 */
export function matchingJsonSchema(schema: unknown, options: JsonSchemaOptions = {}): Matcher {
  return anywhere({
    matchesJsonSchema: definedOperand(schema, 'matchesJsonSchema'),
    ...(options.schemaVersion === undefined ? {} : { schemaVersion: options.schemaVersion }),
  });
}

/**
 * The subject parses as a date-time strictly earlier than this operand.
 *
 * **The operand's type selects the comparison, and this is the behaviour that
 * surprises people.** An operand carrying a zone compares *instants*, honouring
 * the request value's offset and resolving a zoneless request value in the pod's
 * zone. An operand with **no** zone compares *wall-clock fields* and discards
 * the request value's offset rather than converting it — so
 * `before('2021-06-14T12:00:00')` reports `2021-06-14T13:00:00+03:00` as later,
 * though that instant is an hour earlier (SPEC §5.2). Writing the zone into the
 * operand is what makes the comparison mean what a reader assumes it means.
 *
 * Accepted operands: ISO-8601 with a colon in any offset, a bare date, RFC 1123,
 * and the now-relative forms of {@link NowRelative}. A spelling that could never
 * match is refused 422 rather than registering and failing every request with no
 * diagnostic, which is what WireMock does with thirteen of them (deviation #49).
 *
 * The comparison is strict: an operand equal to the request's value does not
 * match.
 */
export function before<Expected extends string>(
  expected: Expected,
  options?: DateTimeOptionsFor<Expected>,
): Matcher {
  return anywhere({ before: expected, ...temporalModifiers(options) });
}

/** The subject parses as a date-time strictly later than this operand. Strict, as {@link before} is. */
export function after<Expected extends string>(
  expected: Expected,
  options?: DateTimeOptionsFor<Expected>,
): Matcher {
  return anywhere({ after: expected, ...temporalModifiers(options) });
}

/**
 * The subject parses as a date-time equal to this operand.
 *
 * Equality is instant-valued and exact to the nanosecond, so `12:13:14Z` equals
 * `12:13:14.000Z` and not `12:13:14.001Z`.
 *
 * A **bare date matches that whole day**, which is a deliberate widening.
 * WireMock reads a date-only operand as midnight, so `'2021-06-14'` excludes
 * almost every moment of the day it names — an answer nobody writing that
 * criterion means. The widening is confined to equality: widening
 * {@link before} or {@link after} would refuse requests WireMock accepts
 * (deviation #51).
 */
export function equalToDateTime<Expected extends string>(
  expected: Expected,
  options?: DateTimeOptionsFor<Expected>,
): Matcher {
  return anywhere({ equalToDateTime: expected, ...temporalModifiers(options) });
}

/**
 * The four modifier fields, flattened.
 *
 * The public options types are unions that say which combinations are legal,
 * which is what a caller needs and what a function reading the values does not:
 * this shape exists so {@link temporalModifiers} can ask for each field once
 * rather than narrow a union it has already been told is valid.
 */
interface TemporalModifiers {
  truncateExpected?: DateTimeTruncation;
  truncateActual?: DateTimeTruncation;
  applyTruncationLast?: boolean;
  actualFormat?: string;
}

/** The modifier fields a date-time matcher carries, with the absent ones left out. */
function temporalModifiers(options: unknown): ContentMatcher {
  if (options === undefined) return {};
  // The parameter arrives as an unresolved conditional type, which the compiler
  // cannot relate to either branch from inside the generic function. The
  // assertion narrows it to the union of the two branches' members, every one
  // of which the caller has already been type-checked against.
  const given = options as TemporalModifiers;
  const document: ContentMatcher = {};
  // Written key by key rather than by spreading `options`, because under
  // `exactOptionalPropertyTypes` an explicitly-undefined member is not the same
  // as an absent one — and on the wire it is the difference between a document
  // the server reads as silent about truncation and one carrying `null`, which
  // it refuses.
  if (given.truncateExpected !== undefined) document.truncateExpected = given.truncateExpected;
  if (given.truncateActual !== undefined) document.truncateActual = given.truncateActual;
  if (given.applyTruncationLast !== undefined) {
    document.applyTruncationLast = given.applyTruncationLast;
  }
  if (given.actualFormat !== undefined) document.actualFormat = given.actualFormat;
  return document;
}

/**
 * Refuses an operand that is `undefined`.
 *
 * A matcher whose operand is `undefined` disappears when the document is
 * serialised, and what reaches the server is an empty matcher object — refused
 * 422 with a message about the document rather than about the call that made
 * it. The two positions that take an arbitrary JSON value are the only ones
 * where the type system cannot say this, because the contract's own field type
 * is `unknown` and `unknown` includes `undefined`.
 */
function definedOperand(operand: unknown, field: string): unknown {
  if (operand === undefined) {
    throw new TypeError(`${field} needs a JSON document as its operand, and was given undefined`);
  }
  return operand;
}

/**
 * Builds a now-relative operand in the exact spelling the server parses.
 *
 * WireMock's Java DSL spells this `expectedOffset(amount, unit)` on the matcher
 * builder, and it writes the offset into the expected value exactly as this
 * does. There is **no** offset field in the JSON: `expectedOffset`,
 * `truncateExpectedTo` and `truncateActualTo` are three names that reached this
 * project's own spec and shipped code without existing anywhere in WireMock,
 * and the server now answers each of them with the real parameter's name.
 *
 * The value of having it as a function rather than only as a literal is the
 * computed case. `` `now ${n} days` `` is a `string` to the compiler, which
 * loses the fact that it is now-relative and with it the right to say
 * `truncateExpected`; this returns {@link NowRelative}, so it does not.
 */
export function nowOffset(amount: number, unit: TemporalUnit): NowRelative {
  if (!Number.isInteger(amount)) {
    throw new TypeError(`a now-relative offset must be a whole number of ${unit}, got ${amount}`);
  }
  // The sign is always written. WireMock accepts an unsigned count as a
  // positive offset, but a document that says `+3` reads unambiguously to a
  // person, and the two register identically.
  const signed = amount < 0 ? `${amount}` : `+${amount}`;
  return `now ${signed} ${unit}` as NowRelative;
}

/**
 * The key is not present.
 *
 * A key-level criterion, so it means something as the value of a header,
 * cookie, query parameter, form parameter or path parameter, and nothing on a
 * body — a body is not a key that can be missing.
 *
 * There is no `present()` and no argument: `{"absent": false}` is refused 422
 * rather than coerced, because WireMock deserializes the field as a presence
 * flag and stores `absent: true` whatever value it was given, so a criterion
 * written to mean "this header must be present" silently becomes its exact
 * opposite (deviation #23). `not(absent())` is how presence is stated.
 */
export function absent(): Matcher {
  return anywhere({ absent: true });
}

/**
 * Every operand must hold.
 *
 * Two operands are the minimum, in the signature because they are the minimum
 * on the server and on WireMock, which answers 422 for a one-operand form
 * (deviation #27).
 */
export function and(first: Matcher, second: Matcher, ...rest: Matcher[]): Matcher {
  return anywhere({ and: [first, second, ...rest] });
}

/** At least one operand must hold. Two operands minimum, as with {@link and}. */
export function or(first: Matcher, second: Matcher, ...rest: Matcher[]): Matcher {
  return anywhere({ or: [first, second, ...rest] });
}

/** The operand must not hold. */
export function not(matcher: Matcher): Matcher {
  return anywhere({ not: matcher });
}

/**
 * The json-unit placeholders {@link equalToJson} understands, spelled out.
 *
 * These are constants where the rest of this module leaves string-literal
 * unions to autocomplete, and the difference is that a placeholder is a *value
 * inside the caller's own document* rather than the value of a typed field:
 * nothing can suggest `${json-unit.any-string}` while a JSON literal is being
 * written, and a misspelling is not a compile error anywhere. On WireMock it is
 * not an error at all — the text is compared literally and the stub silently
 * never matches — and mockulus refuses it at registration instead (deviation
 * #5), which is better and still later than here.
 *
 * A placeholder occupies a slot in an array rather than excusing one, so
 * `[JsonUnit.AnyNumber]` matches a one-element array and not an empty one.
 */
export const JsonUnit = {
  /** Accepts any value in this position, including a missing object member. */
  Ignore: '${json-unit.ignore}',
  /** Accepts any value, and also the member not being there at all. */
  IgnoreElement: '${json-unit.ignore-element}',
  /** Accepts any JSON string. */
  AnyString: '${json-unit.any-string}',
  /** Accepts any JSON number. */
  AnyNumber: '${json-unit.any-number}',
  /** Accepts any JSON boolean. */
  AnyBoolean: '${json-unit.any-boolean}',
} as const;

/**
 * The json-unit regex placeholder, applied as a full match.
 *
 * Separate from {@link JsonUnit} because it carries an operand, and it takes the
 * pattern rather than being written by hand so the `${json-unit.regex}` prefix
 * cannot be misspelled into a literal string comparison.
 */
export function jsonUnitRegex(pattern: string): string {
  return `\${json-unit.regex}${pattern}`;
}
