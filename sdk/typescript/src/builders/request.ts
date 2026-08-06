// SPDX-License-Identifier: Apache-2.0

/**
 * The request half of a stub, and the mapping the two halves make together —
 * under the names WireMock's Java DSL gives them.
 *
 * A request matches when **every** stated criterion matches, and an absent
 * criterion is a wildcard, so a stub built from `get(anyUrl())` alone covers
 * every GET. That is the server's rule and WireMock's, and it is why nothing
 * here has a default: a criterion that was not written is not a criterion.
 *
 * Three refusals the server would answer with a 422 are unwritable here
 * instead, and each one is a shape WireMock accepts and then behaves oddly
 * around:
 *
 * - **Two URL criteria.** The verb takes exactly one, so there is no second to
 *   give. WireMock resolves a pair by a fixed field precedence and its echo
 *   silently omits the criteria it discarded, so a stub matching on a field its
 *   author did not intend reads back as though the others were never written
 *   (deviation #47).
 * - **A path-parameter criterion with no template to bind against, or naming a
 *   variable the template does not bind.** {@link urlPathTemplate} carries its
 *   variable names in its type, so `withPathParam` accepts those names and
 *   nothing else, and does not exist at all on a stub whose URL criterion is
 *   not a template. WireMock accepts the first case and drops the whole block,
 *   so an unsatisfiable criterion registers and the stub then matches *every*
 *   request — the widest possible failure, arrived at silently (deviation #54).
 * - **A scenario state with no scenario.** `whenScenarioStateIs` and
 *   `willSetStateTo` appear only after `inScenario`, which is the shape
 *   WireMock's own Java builder has.
 */

import type { RequestPattern, StubMapping } from '../types.js';
import type { BodyPattern, Matcher } from './matchers.js';
import type { ResponseBuilder } from './response.js';

/**
 * The variable names a path template binds.
 *
 * A variable is a whole segment, so `/orders/{id}` binds `id` and `/a{b}c` is
 * refused by {@link urlPathTemplate} before this type is ever consulted — which
 * is why the extraction below can be the simple one.
 */
export type PathVariables<Template extends string> =
  Template extends `${string}{${infer Name}}${infer Rest}` ? Name | PathVariables<Rest> : never;

/** Whether a mapping has named a scenario, and so whether it may name a state. */
type ScenarioState = 'none' | 'named';

/** The five ways a request pattern may state a URL criterion. */
type UrlField = keyof Pick<
  RequestPattern,
  'url' | 'urlPattern' | 'urlPath' | 'urlPathPattern' | 'urlPathTemplate'
>;

/**
 * One URL criterion, and the path variables it binds.
 *
 * It is a class rather than a document fragment so that a caller cannot write a
 * second URL field into it: the only way to make one is through the six
 * functions below, and each of them writes exactly one field. That is the
 * mutual exclusion the contract declines to express — "a mutually exclusive
 * union of five variants would make the shared type unusable for the SDK" — put
 * where a builder can carry it.
 */
export class UrlCriterion<Variables extends string = never> {
  /**
   * Present so `UrlCriterion<'id'>` and `UrlCriterion<never>` are different
   * types. `declare` because the names live in {@link PathVariables} and are
   * never needed at runtime.
   */
  declare private readonly variables: Variables;

  /** @param field The one field this criterion writes; `undefined` for {@link anyUrl}. */
  constructor(
    readonly field: UrlField | undefined,
    readonly value: string,
  ) {}

  /** The fragment this criterion contributes to a request pattern. */
  toFragment(): RequestPattern {
    return this.field === undefined ? {} : { [this.field]: this.value };
  }
}

/**
 * **Byte-exact** match on path and query as received, so query parameter order
 * matters: `?a=1&b=2` and `?b=2&a=1` are different criteria and only one of them
 * matches a given request.
 */
export function urlEqualTo(url: string): UrlCriterion {
  return new UrlCriterion('url', absolutePath(url, 'urlEqualTo'));
}

/** A regular expression matched in full against path and query together. */
export function urlMatching(pattern: string): UrlCriterion {
  return new UrlCriterion('urlPattern', pattern);
}

/** Exact match on the path alone, ignoring the query. */
export function urlPathEqualTo(path: string): UrlCriterion {
  return new UrlCriterion('urlPath', absolutePath(path, 'urlPathEqualTo'));
}

/** A regular expression matched in full against the path alone. */
export function urlPathMatching(pattern: string): UrlCriterion {
  return new UrlCriterion('urlPathPattern', pattern);
}

/**
 * A WireMock 3 path template such as `/orders/{id}`, matched against the path
 * and binding each variable for {@link MappingBuilder.withPathParam}.
 *
 * The template is checked here, by the same rules the server applies, because
 * the type below is derived from it: a template the server would refuse would
 * otherwise produce a set of variable names that is not the set the stub ends up
 * binding, and the compile-time guarantee would be over a template that never
 * registers.
 */
export function urlPathTemplate<Template extends string>(
  template: Template,
): UrlCriterion<PathVariables<Template>> {
  return new UrlCriterion<PathVariables<Template>>('urlPathTemplate', checkedTemplate(template));
}

/**
 * No URL criterion at all, which is a wildcard.
 *
 * WireMock spells it `anyUrl()` and means the same thing: the field is absent
 * from the document rather than present and empty.
 */
export function anyUrl(): UrlCriterion {
  return new UrlCriterion(undefined, '');
}

/**
 * A stub under construction.
 *
 * The builder is immutable — every call answers a new one — so a partly-built
 * mapping can be shared between cases and specialised two ways without the
 * second inheriting the first's criteria. WireMock's Java builder mutates in
 * place, and this is the one place these builders deliberately differ from it.
 *
 * `Variables` is the set of names the URL template binds, and `Scenario`
 * records whether a scenario has been named. Both exist only in the type
 * system.
 */
export class MappingBuilder<
  Variables extends string = never,
  Scenario extends ScenarioState = 'none',
> {
  /** Present so two instantiations are different types — see {@link UrlCriterion}. */
  declare private readonly variables: Variables;
  /** Present for the same reason, so the scenario methods can require a scenario. */
  declare private readonly scenario: Scenario;

  private constructor(private readonly document: StubMapping) {}

  /** Starts a mapping from a method and a URL criterion. Not the entry point; the verbs are. */
  static begin<Variables extends string>(
    method: string,
    url: UrlCriterion<Variables>,
  ): MappingBuilder<Variables> {
    return new MappingBuilder<Variables>({ request: { method, ...url.toFragment() } });
  }

  /** The mapping as it stands. {@link stubFor} is the spelling a caller uses. */
  toDocument(): StubMapping {
    return { ...this.document };
  }

  /**
   * A criterion over one request header.
   *
   * Header names are case-insensitive in both directions, so a criterion on
   * `Accept` matches a request that sent `accept`. Values are case-sensitive
   * unless the matcher folds case.
   *
   * A repeated header matches when **any** of its values satisfies the matcher.
   * WireMock instead picks the value at minimum edit distance and matches that
   * one, so mockulus matches strictly more here and no suite that passes on
   * WireMock can fail on this (deviation #29).
   */
  withHeader(name: string, matcher: Matcher): MappingBuilder<Variables, Scenario> {
    return this.withKeyCriterion('headers', name, matcher);
  }

  /**
   * A criterion over one query parameter.
   *
   * A repeated parameter matches when any of its values satisfies the matcher.
   * `?x=` and a bare `?x` are both present-with-empty-string, never absent, so
   * `absent()` is the only way to say "must not be present" and `equalTo('')` is
   * how the two empty spellings are matched.
   */
  withQueryParam(name: string, matcher: Matcher): MappingBuilder<Variables, Scenario> {
    return this.withKeyCriterion('queryParameters', name, matcher);
  }

  /** A criterion over one cookie. */
  withCookie(name: string, matcher: Matcher): MappingBuilder<Variables, Scenario> {
    return this.withKeyCriterion('cookies', name, matcher);
  }

  /**
   * A criterion over one field of an `application/x-www-form-urlencoded` body.
   *
   * The body is parsed lazily, so a request that fails on URL or method never
   * pays for the parse.
   */
  withFormParam(name: string, matcher: Matcher): MappingBuilder<Variables, Scenario> {
    return this.withKeyCriterion('formParameters', name, matcher);
  }

  /**
   * A criterion over one variable of the URL template.
   *
   * The name is one of the template's own variables — the type says so — which
   * is what makes the two refusals around this field unwritable rather than
   * merely documented. On a stub whose URL criterion is not a template,
   * `Variables` is `never` and there is no name that can be passed at all.
   */
  withPathParam(name: Variables, matcher: Matcher): MappingBuilder<Variables, Scenario> {
    return this.withKeyCriterion('pathParameters', name, matcher);
  }

  /**
   * A criterion over the request body.
   *
   * Several of these are an AND: **all** of them must match. They are evaluated
   * cheapest first — equality before regex before anything that has to parse the
   * body — which is an ordering the server chooses and no stub needs to think
   * about, so the order they are written in is a matter of reading rather than
   * of cost.
   *
   * This is the one position that takes {@link BodyPattern} rather than
   * {@link Matcher}, because it is the one position where `binaryEqualTo` has
   * raw bytes to compare against.
   */
  withRequestBody(matcher: BodyPattern): MappingBuilder<Variables, Scenario> {
    return this.withRequest({
      bodyPatterns: [...(this.document.request?.bodyPatterns ?? []), matcher],
    });
  }

  /**
   * Sugar over an `Authorization` header criterion.
   *
   * Both halves are required because a half-written credential is a criterion
   * its author cannot have meant, and the server refuses one.
   */
  withBasicAuth(username: string, password: string): MappingBuilder<Variables, Scenario> {
    return this.withRequest({ basicAuthCredentials: { username, password } });
  }

  /** A human-readable label, stored, echoed and shown in near-miss output. */
  withName(name: string): MappingBuilder<Variables, Scenario> {
    return this.with({ name });
  }

  /**
   * The stub's identity, which the server otherwise generates.
   *
   * It must be the canonical 36-character spelling, which is checked here
   * because the alternative is a 422 on a value that was probably produced by
   * the wrong formatter. The dashless, `urn:uuid:` and brace-wrapped spellings
   * are refused by WireMock too; the 24-character base64 encoding of the raw
   * bytes is one WireMock accepts and silently rewrites, and refusing it is what
   * stops a client being handed back an id it did not choose (deviation #24).
   *
   * `id` is written rather than `uuid` — they are aliases and both are echoed —
   * because sending a single spelling reproduces WireMock exactly, and sending
   * both is only safe while they agree.
   */
  withId(id: string): MappingBuilder<Variables, Scenario> {
    if (!/^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$/.test(id)) {
      throw new TypeError(`withId needs a canonical 36-character UUID, got ${JSON.stringify(id)}`);
    }
    return this.with({ id });
  }

  /**
   * Selection order among matching stubs, lower first. Absent means 5.
   *
   * An arbitrary signed integer compared numerically — no clamping and no range
   * validation, which is what the pinned WireMock does, so the common "1 is the
   * highest" phrasing describes a convention rather than a constraint. The range
   * checked here is the one the field is declared over, and nothing narrower.
   *
   * Equal priorities break on insertion sequence, newest first, and that
   * sequence is a cluster-global counter, so "most recently added wins" holds
   * across replicas (SPEC §5.3, §7.3).
   */
  withPriority(priority: number): MappingBuilder<Variables, Scenario> {
    if (!Number.isInteger(priority) || priority < -2147483648 || priority > 2147483647) {
      throw new RangeError(`withPriority needs a 32-bit integer, got ${String(priority)}`);
    }
    return this.with({ priority });
  }

  /**
   * Arbitrary JSON, stored and echoed untouched, and searchable through
   * `find-by-metadata` and `remove-by-metadata`.
   *
   * Tagging every stub a run creates with a run id, and cleaning up through
   * `remove-by-metadata`, is the discipline SPEC §1 asks of suites sharing a
   * deployment — the alternative is a global reset that takes another team's
   * stubs with it.
   */
  withMetadata(metadata: Record<string, unknown>): MappingBuilder<Variables, Scenario> {
    return this.with({ metadata: { ...this.document.metadata, ...metadata } });
  }

  /**
   * Asks for a durable document rather than one with a TTL.
   *
   * Absent or `false` stores a document with `ephemeral_stub_ttl` — 24 hours by
   * default — which `POST /__admin/mappings/reset` also sweeps. WireMock keeps
   * non-persistent stubs until the process restarts; a TTL is the documented
   * equivalent for a long-running cluster that never restarts (deviation #3).
   */
  persistent(persistent = true): MappingBuilder<Variables, Scenario> {
    return this.with({ persistent });
  }

  /**
   * The scenario this stub belongs to.
   *
   * A scenario exists because stubs name it, so this call is what brings one
   * into being, and deleting the last stub that names it is what ends it. It is
   * also what unlocks the two state methods below, which the server refuses
   * without it.
   */
  inScenario(scenarioName: string): MappingBuilder<Variables, 'named'> {
    return new MappingBuilder<Variables, 'named'>({ ...this.document, scenarioName });
  }

  /**
   * The state the stub matches in.
   *
   * In any other state it is treated as non-matching and iteration continues, so
   * a scenario-gated stub does not shadow the stubs behind it.
   */
  whenScenarioStateIs(
    this: MappingBuilder<Variables, 'named'>,
    requiredScenarioState: string,
  ): MappingBuilder<Variables, 'named'> {
    return this.withScenario({ requiredScenarioState });
  }

  /**
   * The state the scenario moves to after this stub serves.
   *
   * The transition is a compare-and-swap, so two concurrent requests cannot both
   * drive the same step (SPEC §9.3).
   */
  willSetStateTo(
    this: MappingBuilder<Variables, 'named'>,
    newScenarioState: string,
  ): MappingBuilder<Variables, 'named'> {
    return this.withScenario({ newScenarioState });
  }

  /**
   * What a matching request is served.
   *
   * A stub with no response at all serves 200 with an empty body, so this is for
   * saying anything more than that.
   */
  willReturn(response: ResponseBuilder<'unset' | 'set'>): MappingBuilder<Variables, Scenario> {
    return this.with({ response: response.toDocument() });
  }

  /** A new builder over this document plus some top-level fields. */
  private with(fields: StubMapping): MappingBuilder<Variables, Scenario> {
    return new MappingBuilder<Variables, Scenario>({ ...this.document, ...fields });
  }

  /**
   * {@link with} for the scenario methods, whose `this` constraint has narrowed
   * the receiver to a scenario-carrying builder and thereby lost the ability to
   * return `Scenario` unchanged. It is `'named'` in both, by construction.
   */
  private withScenario(
    this: MappingBuilder<Variables, 'named'>,
    fields: StubMapping,
  ): MappingBuilder<Variables, 'named'> {
    return new MappingBuilder<Variables, 'named'>({ ...this.document, ...fields });
  }

  /** A new builder over this document plus some request-pattern fields. */
  private withRequest(fields: RequestPattern): MappingBuilder<Variables, Scenario> {
    return this.with({ request: { ...this.document.request, ...fields } });
  }

  /** A new builder with one more criterion in one of the five keyed blocks. */
  private withKeyCriterion(
    block: 'headers' | 'queryParameters' | 'cookies' | 'formParameters' | 'pathParameters',
    name: string,
    matcher: Matcher,
  ): MappingBuilder<Variables, Scenario> {
    return this.withRequest({ [block]: { ...this.document.request?.[block], [name]: matcher } });
  }
}

/**
 * A mapping builder whatever its URL template and scenario say.
 *
 * {@link stubFor} takes one of these because it does not care: by the time a
 * mapping is being turned into a document, the constraints the two parameters
 * carry have already been checked at every call that could have broken them.
 */
export type AnyMappingBuilder = MappingBuilder<string, 'none' | 'named'>;

/** A stub for GET requests. */
export function get<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin('GET', urlOf(url));
}

/** A stub for POST requests. */
export function post<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin('POST', urlOf(url));
}

/** A stub for PUT requests. */
export function put<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin('PUT', urlOf(url));
}

/** A stub for PATCH requests. */
export function patch<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin('PATCH', urlOf(url));
}

/**
 * A stub for DELETE requests.
 *
 * WireMock's Java DSL calls this `delete`, which JavaScript cannot: `delete` is
 * an operator and not a name a binding can take. `del` is the abbreviation the
 * ecosystem already uses for exactly this collision.
 */
export function del<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin('DELETE', urlOf(url));
}

/** A stub for HEAD requests. */
export function head<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin('HEAD', urlOf(url));
}

/** A stub for OPTIONS requests. */
export function options<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin('OPTIONS', urlOf(url));
}

/** A stub for TRACE requests. */
export function trace<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin('TRACE', urlOf(url));
}

/** A stub for requests of any method. */
export function any<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin('ANY', urlOf(url));
}

/**
 * A stub for a method the functions above do not name.
 *
 * Any method name is accepted, including one WireMock's own enumeration does not
 * define, because a mock server exists to stand in for services that have them.
 * The name is compared after trimming and upper-casing, so `any` and `ANY` are
 * one criterion.
 */
export function request<Variables extends string>(
  method: string,
  url: UrlCriterion<Variables> | string,
): MappingBuilder<Variables> {
  return MappingBuilder.begin(method, urlOf(url));
}

/**
 * Turns a mapping builder into the document to register.
 *
 * WireMock's `stubFor` registers with a server; this answers the mapping, and
 * `client.mappings.create` is what registers it. The split is deliberate: this
 * package has a client with a base URL, a token and a timeout, and a builder
 * that reached past it to a global would be a second, invisible way to configure
 * where a stub goes.
 */
export function stubFor(builder: AnyMappingBuilder): StubMapping {
  return builder.toDocument();
}

/** Reads the shorthand a verb accepts: a bare string is an exact-URL criterion, as in WireMock. */
function urlOf<Variables extends string>(
  url: UrlCriterion<Variables> | string,
): UrlCriterion<Variables> {
  return typeof url === 'string' ? (urlEqualTo(url) as UrlCriterion<Variables>) : url;
}

/**
 * Checks the two URL criteria the server requires to be absolute.
 *
 * `url` and `urlPath` are compared against the path as received, which always
 * starts with a slash, so one that does not could never match anything. The
 * pattern forms are not checked: a regular expression legitimately starts with
 * an anchor, a group or a character class.
 */
function absolutePath(path: string, caller: string): string {
  if (!path.startsWith('/')) {
    throw new TypeError(`${caller} needs a path starting with /, got ${JSON.stringify(path)}`);
  }
  return path;
}

/**
 * Checks a path template by the rules the server parses one with.
 *
 * A variable is a whole segment, no variable is bound twice, and the template
 * starts with a slash. Checking here rather than leaving it to registration is
 * what makes {@link PathVariables} trustworthy: the type reads the braces out of
 * the literal, and if the server would have read them differently the set of
 * names the compiler enforces would not be the set the stub binds.
 */
function checkedTemplate(template: string): string {
  if (!template.startsWith('/')) {
    throw new TypeError(
      `urlPathTemplate needs a template starting with /, got ${JSON.stringify(template)}`,
    );
  }
  const bound = new Set<string>();
  for (const segment of template.slice(1).split('/')) {
    if (segment.startsWith('{') && segment.endsWith('}')) {
      const name = segment.slice(1, -1);
      if (name === '' || /[{}/]/.test(name)) {
        throw new TypeError(`urlPathTemplate has a malformed variable ${JSON.stringify(segment)}`);
      }
      if (bound.has(name)) {
        throw new TypeError(`urlPathTemplate binds ${JSON.stringify(name)} more than once`);
      }
      bound.add(name);
      continue;
    }
    if (/[{}]/.test(segment)) {
      // A brace outside a whole-segment variable is a typo rather than a
      // template WireMock would accept, and saying so is better than matching it
      // literally and leaving the author puzzled about why nothing binds.
      throw new TypeError(
        `urlPathTemplate needs a variable to be a whole segment, as in /orders/{id}, ` +
          `got ${JSON.stringify(template)}`,
      );
    }
  }
  return template;
}
