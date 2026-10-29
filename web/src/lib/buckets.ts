import type { Entry } from "./entries";

export const BUCKETS = 60;

export interface TimeRange {
  from: number;
  to: number;
}

interface Bucket {
  from: number;
  to: number;
  info: number;
  warn: number;
  error: number;
}

/** bucketize spreads entries over equal slices of [from, to], counted by severity. */
export function bucketize(entries: readonly Entry[], from: number, to: number, n = BUCKETS): Bucket[] {
  const width = Math.max(1, (to - from) / n);
  const out: Bucket[] = Array.from({ length: n }, (_, i) => ({
    from: from + i * width,
    to: from + (i + 1) * width,
    info: 0,
    warn: 0,
    error: 0,
  }));
  for (const e of entries) {
    if (e.at === 0) continue;
    const i = Math.min(n - 1, Math.max(0, Math.floor((e.at - from) / width)));
    out[i][e.severity] += 1;
  }
  return out;
}

