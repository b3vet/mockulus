// SPDX-License-Identifier: Apache-2.0

import type { Health, VersionInfo } from '../types.js';
import type { RequestOptions, Transport } from './transport.js';

/**
 * Deployment-wide operations: health, version, the combined reset and the
 * drain.
 */
export class SystemApi {
  constructor(private readonly transport: Transport) {}

  /**
   * Reports this replica's health: WireMock 3.2+'s shape plus the store driver,
   * the number of compiled stubs and the snapshot epoch.
   *
   * Not the Kubernetes probe. Liveness and readiness are `/healthz` and
   * `/readyz` on the ops listener, outside `/__admin` and outside this client.
   * The status here reads `healthy` whenever the handler answers at all — a
   * replica that cannot answer is not reporting a degraded status, it is not
   * answering.
   */
  async health(options?: RequestOptions): Promise<Health> {
    return this.transport.send<Health>({
      method: 'GET',
      path: '/__admin/health',
      ...options,
    });
  }

  /**
   * Reports the server version and the WireMock surface it mirrors.
   *
   * Worth calling first in a suite that did not start the server itself.
   * `guessedWireMockVersion` is a surface claim — it reads `3.x-subset` — and
   * asserting on it is what tells a mockulus apart from whatever else happened
   * to be listening on the port, which is a mistake this repository has paid
   * for once already.
   */
  async version(options?: RequestOptions): Promise<VersionInfo> {
    return this.transport.send<VersionInfo>({
      method: 'GET',
      path: '/__admin/version',
      ...options,
    });
  }

  /**
   * Sweeps the non-persistent stubs, empties the journal and returns every
   * scenario to `Started`, in one call.
   *
   * Deployment-wide, which makes it the call SPEC §1 tells runners sharing an
   * instance never to press: it destroys every other suite's stubs, and the
   * damage arrives in their results looking like a defect. Namespace by URL
   * prefix and clean up through `mappings.removeByMetadata` instead.
   */
  async resetAll(options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'POST',
      path: '/__admin/reset',
      accept: 'none',
      ...options,
    });
  }

  /**
   * Begins the graceful drain.
   *
   * **Disabled by default.** When `admin_shutdown_enabled` is false the route is
   * not registered at all, so this throws the ordinary unsupported-endpoint 404
   * with code 1001 — the endpoint does not exist rather than existing and
   * refusing.
   *
   * When it is enabled the response is written before the drain starts, so this
   * resolves promptly and the listener stops accepting shortly afterwards; the
   * next call on this client will fail to connect, and that is the drain working
   * rather than an error worth reporting.
   */
  async shutdown(options?: RequestOptions): Promise<void> {
    await this.transport.send<void>({
      method: 'POST',
      path: '/__admin/shutdown',
      accept: 'none',
      ...options,
    });
  }
}
