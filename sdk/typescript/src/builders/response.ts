// SPDX-License-Identifier: Apache-2.0

/**
 * The response half of a stub, under the names WireMock's Java DSL gives it.
 *
 * Every field a matching request is served is resolved at registration —
 * regexes compiled, templates parsed, base64 decoded, body files inlined — so
 * the request path only ever evaluates (SPEC §16.3). That is why the refusals
 * this builder is written around are registration-time refusals: by the time a
 * request arrives there is nothing left to refuse.
 *
 * The builder is immutable. Each call answers a new builder over a new
 * document, so a half-built response can be shared between cases and specialised
 * two different ways without the second one inheriting the first's body — which
 * is the bug the mutable WireMock-Java builder invites and the reason for the
 * one deviation from its shape.
 */

import type { ResponseDefinition } from '../types.js';
import { asBase64 } from './base64.js';

/**
 * Whether a response has said what it serves.
 *
 * Exactly one body form may be set — `body`, `jsonBody`, `base64Body` or
 * `bodyFileName` — and more than one is refused 422 naming both fields, where
 * WireMock accepts the combination and silently discards all but `body`
 * (deviation #19). A fault counts as a body form here because it *replaces* the
 * response rather than decorating it, and a stub asking for both is stating two
 * different intents.
 */
type BodyState = 'unset' | 'set';

/** The response transformers this build recognises, read off the contract. */
export type ResponseTransformer = NonNullable<ResponseDefinition['transformers']>[number];

/** The faults a stub can ask for, read off the contract. */
export type Fault = NonNullable<ResponseDefinition['fault']>;

/** The values a template sees under `parameters`. */
export type TransformerParameters = NonNullable<ResponseDefinition['transformerParameters']>;

/**
 * What a matching request is served.
 *
 * The `Body` parameter tracks whether a body form has been chosen. It exists
 * only in the type system — the phantom field below is declared and never
 * assigned — and it is what turns "exactly one body form" from a 422 into a
 * compile error.
 */
export class ResponseBuilder<Body extends BodyState = 'unset'> {
  /**
   * Present so two instantiations of this class are different types.
   *
   * Without a member mentioning `Body`, `ResponseBuilder<'set'>` and
   * `ResponseBuilder<'unset'>` are structurally identical and the `this`
   * constraints below would never bite. `declare` because there is nothing to
   * store: the compiler needs the field, the runtime does not.
   */
  declare private readonly bodyState: Body;

  /**
   * @param document The response so far. Held rather than rebuilt, so what the
   * builder emits is exactly the sequence of calls that were made — no defaults
   * filled in, matching the server, which stores and echoes the document it was
   * given.
   */
  private constructor(private readonly document: ResponseDefinition) {}

  /** Starts an empty response. Not exported; {@link aResponse} is the entry point. */
  static begin(): ResponseBuilder<'unset'> {
    return new ResponseBuilder<'unset'>({});
  }

  /** The document as it stands, for the mapping builder that owns this response. */
  toDocument(): ResponseDefinition {
    return { ...this.document };
  }

  /**
   * The HTTP status.
   *
   * Checked here rather than left to the server because the check is a
   * comparison against two numbers and the alternative is a 422 on registration.
   * WireMock writes a positive out-of-range status unvalidated, producing a
   * malformed status line on the wire, and treats a non-positive one as unset;
   * both are refused rather than reproduced.
   */
  withStatus(status: number): ResponseBuilder<Body> {
    if (!Number.isInteger(status) || status < 100 || status > 599) {
      throw new RangeError(`status must be an integer from 100 to 599, got ${String(status)}`);
    }
    return this.with({ status });
  }

  /**
   * The HTTP/1.1 reason phrase.
   *
   * Setting it has a cost worth knowing before you do: Go's `net/http` cannot
   * choose a reason phrase, so a stub that sets this is written over a hijacked
   * connection and **the connection closes after the response** (deviation #7).
   * A stub that does not set it is untouched, and HTTP/2 carries no reason
   * phrase at all.
   *
   * The phrase is encoded once at registration exactly as WireMock encodes it —
   * CR and LF each become `?`, and a rune outside Latin-1 becomes `?` — so it
   * can neither split the response nor be refused for something WireMock
   * accepts, and nothing needs checking here.
   */
  withStatusMessage(statusMessage: string): ResponseBuilder<Body> {
    return this.with({ statusMessage });
  }

  /**
   * One response header, with one or more values.
   *
   * At least one value is required by the signature because an empty array
   * names a header and gives it nothing to say, and is refused 422 as WireMock
   * refuses it. A single value is emitted as a string and several as an array,
   * which is the document WireMock would have written.
   *
   * Two spellings of one name are one header on the server, folded into the
   * first spelling the document used. A `Content-Type` declared more than
   * once — as several media types or under two spellings of the name — is
   * refused 422, because a response carries exactly one and there is no reading
   * of a pair more likely to be right than any other (deviation #48).
   */
  withHeader(name: string, value: string, ...more: string[]): ResponseBuilder<Body> {
    const headers = {
      ...this.document.headers,
      [name]: more.length === 0 ? value : [value, ...more],
    };
    return this.with({ headers });
  }

  /**
   * An inline text body.
   *
   * No `Content-Type` is emitted unless the stub sets one, so a JSON payload
   * written through this rather than through {@link withJsonBody} reaches the
   * client without a media type.
   */
  withBody(this: ResponseBuilder<'unset'>, body: string): ResponseBuilder<'set'> {
    return this.withBodyForm({ body });
  }

  /**
   * An inline JSON body, served as the tokens it was registered with.
   *
   * Insignificant whitespace is dropped and nothing else is rewritten. WireMock
   * re-serializes the document, so `1e2` becomes `100` and a `\u` escape becomes
   * the character it names; the documents are structurally equal either way and
   * only the bytes differ (deviation #38).
   */
  withJsonBody(this: ResponseBuilder<'unset'>, body: unknown): ResponseBuilder<'set'> {
    if (body === undefined) {
      // `undefined` is not JSON: it vanishes on serialisation and the stub
      // registers with no body at all, which is a different stub from the one
      // that was written and is not refused by anything downstream.
      throw new TypeError('withJsonBody needs a JSON document, and was given undefined');
    }
    return this.withBodyForm({ jsonBody: body });
  }

  /**
   * A binary body.
   *
   * Bytes are encoded here; a string is taken as already-base64 and checked, so
   * a mistyped fixture is a `TypeError` at the call site rather than a 422.
   *
   * It is decoded at registration and, when the stub asks for response
   * templating, templated **after** decoding — WireMock never templates a
   * `base64Body`, so a payload encoded to keep it out of a template renders here
   * and stays literal there (deviation #46).
   */
  withBase64Body(
    this: ResponseBuilder<'unset'>,
    body: Uint8Array | ArrayBuffer | string,
  ): ResponseBuilder<'set'> {
    return this.withBodyForm({ base64Body: asBase64(body, 'withBase64Body') });
  }

  /**
   * The name of a file in the response-body file store.
   *
   * Existence is deliberately not checked at registration, so registering a stub
   * before uploading its file is legal and the reference resolves when the file
   * appears. A stub whose file is missing when the snapshot is built serves 500
   * with code 1022 until then (SPEC §6.9).
   */
  withBodyFile(this: ResponseBuilder<'unset'>, fileName: string): ResponseBuilder<'set'> {
    if (fileName === '') {
      throw new TypeError('withBodyFile needs a file name, and was given an empty string');
    }
    return this.withBodyForm({ bodyFileName: fileName });
  }

  /**
   * Breaks the response deliberately, for testing a client's failure handling.
   *
   * A fault occupies the body slot: it replaces the response rather than
   * decorating it, so the server refuses one combined with a body and this
   * builder refuses one combined with any body form. That is marginally
   * stricter than the server, which does not police a fault beside a
   * `bodyFileName` — a combination in which the file is read at snapshot build
   * and then never written to the wire, so nothing is lost by making it
   * unsayable.
   *
   * Faults are byte-faithful on HTTP/1.1 only. Over HTTP/2 there is no
   * connection to hijack and the best available answer is a stream reset, which
   * is why h2c is off by default (deviation #15).
   */
  withFault(this: ResponseBuilder<'unset'>, fault: Fault): ResponseBuilder<'set'> {
    return this.withBodyForm({ fault });
  }

  /**
   * A fixed delay before the response is written.
   *
   * A stub's own delay overrides the global one from `POST /__admin/settings`,
   * and the fixed and distributed parts are summed rather than one winning.
   */
  withFixedDelay(milliseconds: number): ResponseBuilder<Body> {
    return this.with({ fixedDelayMilliseconds: wholeMilliseconds(milliseconds, 'withFixedDelay') });
  }

  /**
   * A delay drawn uniformly from a range, per matched response.
   *
   * The two bounds are separate arguments rather than a distribution document
   * because the document's `type` discriminator is the kind of field a caller
   * can only get wrong: there is exactly one spelling of a uniform distribution
   * and this writes it.
   */
  withUniformRandomDelay(
    lowerMilliseconds: number,
    upperMilliseconds: number,
  ): ResponseBuilder<Body> {
    const lower = wholeMilliseconds(lowerMilliseconds, 'withUniformRandomDelay');
    const upper = wholeMilliseconds(upperMilliseconds, 'withUniformRandomDelay');
    if (upper < lower) {
      throw new RangeError(
        `withUniformRandomDelay needs an upper bound at or above the lower one, got ${lower}..${upper}`,
      );
    }
    return this.with({ delayDistribution: { type: 'uniform', lower, upper } });
  }

  /**
   * A delay drawn from a log-normal distribution, which is the shape real
   * service latency has: a dense cluster near the median and a long right tail.
   *
   * `sigma` is the standard deviation of the underlying normal distribution and
   * is therefore not in milliseconds; it is a spread, and a fractional value is
   * the ordinary case rather than an error.
   */
  withLogNormalRandomDelay(medianMilliseconds: number, sigma: number): ResponseBuilder<Body> {
    const median = wholeMilliseconds(medianMilliseconds, 'withLogNormalRandomDelay');
    if (!Number.isFinite(sigma) || sigma < 0) {
      throw new RangeError(
        `withLogNormalRandomDelay needs a sigma of zero or more, got ${String(sigma)}`,
      );
    }
    return this.with({ delayDistribution: { type: 'lognormal', median, sigma } });
  }

  /**
   * Writes the body in pieces spread over a duration, which is how a client's
   * streaming and timeout behaviour gets exercised.
   */
  withChunkedDribbleDelay(
    numberOfChunks: number,
    totalDurationMilliseconds: number,
  ): ResponseBuilder<Body> {
    if (!Number.isInteger(numberOfChunks) || numberOfChunks < 1) {
      throw new RangeError(
        `withChunkedDribbleDelay needs at least one chunk, got ${String(numberOfChunks)}`,
      );
    }
    return this.with({
      chunkedDribbleDelay: {
        numberOfChunks,
        totalDuration: wholeMilliseconds(totalDurationMilliseconds, 'withChunkedDribbleDelay'),
      },
    });
  }

  /**
   * The response transformers to run.
   *
   * `response-template` is the only recognised name — any other is refused 422
   * with code 1004, because an unrecognised transformer would silently do
   * nothing — so the parameter type has exactly one member and there is nothing
   * to misspell.
   *
   * Template parse errors and unknown helpers are refused at registration rather
   * than deferred to serve time (deviation #13), so a stub that registers
   * renders.
   */
  withTransformers(...transformers: ResponseTransformer[]): ResponseBuilder<Body> {
    return this.with({ transformers: [...(this.document.transformers ?? []), ...transformers] });
  }

  /**
   * One value exposed to templates as `parameters`.
   *
   * The block is open by design: this is the stub author's own data and the
   * server stores it verbatim. The one name it reads is `disableBodyTemplating`,
   * which exempts the body from templating while leaving the headers templated —
   * a mockulus extension rather than parity, so a stub carrying it renders
   * differently on the two servers (deviation #31).
   */
  withTransformerParameter(name: string, value: unknown): ResponseBuilder<Body> {
    return this.with({
      transformerParameters: { ...this.document.transformerParameters, [name]: value },
    });
  }

  /** {@link withTransformerParameter} for a whole block at once. */
  withTransformerParameters(parameters: TransformerParameters): ResponseBuilder<Body> {
    return this.with({
      transformerParameters: { ...this.document.transformerParameters, ...parameters },
    });
  }

  /** A new builder over this document plus a field, with the body state unchanged. */
  private with(fields: ResponseDefinition): ResponseBuilder<Body> {
    return new ResponseBuilder<Body>({ ...this.document, ...fields });
  }

  /**
   * A new builder over this document plus the body form, which is the one
   * transition that changes the type.
   *
   * The assertion is the runtime counterpart of the `this` constraints on the
   * five callers: they have already established that no body form is set, and
   * the parameter tracking that is phantom, so there is no value to change.
   */
  private withBodyForm(fields: ResponseDefinition): ResponseBuilder<'set'> {
    return new ResponseBuilder<'set'>({ ...this.document, ...fields });
  }
}

/**
 * Starts a response definition.
 *
 * The name is WireMock's. A stub with no `willReturn` at all serves 200 with an
 * empty body, so this is for saying anything more than that.
 */
export function aResponse(): ResponseBuilder<'unset'> {
  return ResponseBuilder.begin();
}

/** Reads a delay, which the server takes as a whole non-negative number of milliseconds. */
function wholeMilliseconds(value: number, caller: string): number {
  if (!Number.isInteger(value) || value < 0) {
    // The server refuses a negative or fractional delay 422 rather than
    // normalising it (deviation #36), so rounding here would be this package
    // quietly serving a different stub than the one that was written.
    throw new RangeError(
      `${caller} needs a whole number of milliseconds, zero or more, got ${String(value)}`,
    );
  }
  return value;
}
