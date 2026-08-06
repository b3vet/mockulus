// SPDX-License-Identifier: Apache-2.0

import type { Settings, SettingsEnvelope } from '../types.js';
import type { RequestOptions, Transport } from './transport.js';

/**
 * The deployment's global settings, which today is the deployment-wide response
 * delay and nothing else.
 *
 * WireMock's other settings are refused by name with code 1005 rather than
 * accepted and ignored, so a delay this SDK can express is a delay the
 * deployment will actually wait out.
 */
export class SettingsApi {
  constructor(private readonly transport: Transport) {}

  /**
   * Reads the settings, in the envelope WireMock wraps them in.
   *
   * The envelope is kept rather than unwrapped for the reason every other
   * envelope on this client is: what comes back is the document the server
   * sent, so the shape a caller logs or asserts on is the shape on the wire.
   *
   * The answer comes from the store rather than from one replica's snapshot, so
   * it cannot contradict a write the caller already got a 200 for. A deployment
   * nobody has configured answers `{ settings: {} }` rather than a 404 or an
   * invented default.
   */
  async get(options?: RequestOptions): Promise<SettingsEnvelope> {
    return this.transport.send<SettingsEnvelope>({
      method: 'GET',
      path: '/__admin/settings',
      ...options,
    });
  }

  /**
   * Replaces the settings — replace, not merge, because a merge would leave no
   * way to clear a key. Posting `{}` is how a suite that set a global delay puts
   * the deployment back the way it found it.
   *
   * The answer is **200 with no body**, so this resolves to nothing; read the
   * settings back if the stored document is wanted. The write is epoch-bumped
   * and therefore reaches every replica rather than the one that took the call.
   */
  async update(settings: Settings, options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'POST',
      path: '/__admin/settings',
      body: settings,
      accept: 'none',
      ...options,
    });
  }
}
