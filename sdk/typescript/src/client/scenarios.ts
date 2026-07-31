// SPDX-License-Identifier: Apache-2.0

import type { ScenarioList } from '../types.js';
import { encodeSegment } from './shared.js';
import type { RequestOptions, Transport } from './transport.js';

/**
 * Stateful mocks.
 *
 * A scenario exists because some stub names it, and its possible states are the
 * states those stubs name — there is no lifecycle to manage and nothing to
 * create. Deleting the last stub that references a scenario is what ends it.
 */
export class ScenariosApi {
  constructor(private readonly transport: Transport) {}

  /**
   * Lists the scenarios the current stubs define, ordered by name.
   *
   * WireMock additionally embeds every member stub under a `mappings` key;
   * mockulus does not. A scenario holding a hundred stubs would repeat all
   * hundred inside a listing whose caller wants a state name, and the same
   * documents are one mappings listing away.
   */
  async list(options?: RequestOptions): Promise<ScenarioList> {
    return this.transport.send<ScenarioList>({
      method: 'GET',
      path: '/__admin/scenarios',
      ...options,
    });
  }

  /** Clears every stored state, so all scenarios read back as `Started`. */
  async reset(options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'POST',
      path: '/__admin/scenarios/reset',
      accept: 'none',
      ...options,
    });
  }

  /**
   * Drives a scenario into a named state.
   *
   * Both the scenario and the target state are validated against what the stubs
   * define, and an unknown one is refused with code 1031 rather than accepted:
   * a scenario driven somewhere no stub can match looks like a server defect
   * rather than the typo it is. `Started` is a possible state of every scenario,
   * so setting it is always accepted — WireMock refuses it when no stub names
   * it, even though it is the state the scenario is in until something moves it.
   *
   * Both refusals carry the error envelope, so there is no `…OrNull` here: an
   * unknown name is answered with a 404 that says which name and which states
   * exist, which is worth reading rather than flattening into `null`.
   */
  async setState(name: string, state: string, options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'PUT',
      path: `/__admin/scenarios/${encodeSegment(name)}/state`,
      body: { state },
      accept: 'none',
      ...options,
    });
  }
}
