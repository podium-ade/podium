import type { Snapshot } from "../gen/podium/v1/snapshot_pb";
import { toDate } from "./format";

export const DAY_SECONDS = 86_400;
export const GIB = 1024 ** 3;

/** A retention policy as the form edits it. Zero turns a rule off, as on the server. */
export type Policy = { maxAgeSeconds: number; maxTotalBytes: number };

/**
 * sweepPlan is the server's Sweep, run in the browser so the form can say what saving will
 * delete before it does. Newest first: older than the age goes, and once the running total
 * passes the cap everything older goes too. A snapshot in use is kept and still counted.
 */
export function sweepPlan(rows: Snapshot[], policy: Policy, now = Date.now()): Set<string> {
  const out = new Set<string>();
  if (policy.maxAgeSeconds <= 0 && policy.maxTotalBytes <= 0) return out;
  const sorted = [...rows].sort((a, b) => updatedMs(b) - updatedMs(a));
  const cutoff = now - policy.maxAgeSeconds * 1000;
  let total = 0;
  let over = false;
  for (const row of sorted) {
    const size = Number(row.sizeBytes);
    const expired = policy.maxAgeSeconds > 0 && updatedMs(row) < cutoff;
    if (!expired && !over && policy.maxTotalBytes > 0 && total + size > policy.maxTotalBytes) over = true;
    if (row.inUse || (!expired && !over)) {
      total += size;
      continue;
    }
    out.add(row.sessionId);
  }
  return out;
}

function updatedMs(s: Snapshot): number {
  return toDate(s.updatedAt)?.getTime() ?? 0;
}

/** totalBytes sums the sizes, for the summary line. */
export function totalBytes(rows: Snapshot[]): number {
  return rows.reduce((sum, r) => sum + Number(r.sizeBytes), 0);
}
