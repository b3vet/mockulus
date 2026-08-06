// SPDX-License-Identifier: Apache-2.0

/**
 * Turning the two numbers in the health document that are not already readable
 * into text a person can act on.
 */

/**
 * An uptime in seconds, as a duration.
 *
 * Coarse on purpose: the question this answers is "has this replica just
 * restarted", and to a minute is enough to answer it. Seconds are kept only
 * below a minute, which is exactly the window where the answer is yes.
 */
export function formatUptime(seconds: number): string {
  if (!Number.isFinite(seconds) || seconds < 0) {
    return 'unknown';
  }
  const whole = Math.floor(seconds);
  if (whole < 60) {
    return `${whole}s`;
  }
  const minutes = Math.floor(whole / 60) % 60;
  const hours = Math.floor(whole / 3600) % 24;
  const days = Math.floor(whole / 86400);

  const parts: string[] = [];
  if (days > 0) parts.push(`${days}d`);
  if (hours > 0) parts.push(`${hours}h`);
  if (minutes > 0 || parts.length === 0) parts.push(`${minutes}m`);
  return parts.join(' ');
}

/**
 * The server's timestamp, in the reader's own zone.
 *
 * The document carries RFC 3339 UTC, which is right on the wire and wrong on a
 * screen: an operator comparing it against a log line they are reading in local
 * time should not have to do the arithmetic. An unparseable value is returned
 * as it came rather than rendered as `Invalid Date`, because the raw string is
 * at least evidence of what the server sent.
 */
export function formatTimestamp(timestamp: string): string {
  const parsed = new Date(timestamp);
  return Number.isNaN(parsed.getTime()) ? timestamp : parsed.toLocaleString();
}
