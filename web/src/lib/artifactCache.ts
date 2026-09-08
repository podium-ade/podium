import { downloadURL } from "./artifacts";
import { getToken, notifyRejected } from "./auth";

/**
 * CACHE_NAME is the Cache Storage bucket for chat images. Artifact bytes are immutable
 * (keyed on id), so a hit is always current; the bucket is same-origin and never shared
 * with the HTTP cache, which will not keep `GET /artifacts/{id}` — that route sits behind
 * identity and sends no Cache-Control.
 */
const CACHE_NAME = "podium-chat-images";

type Entry = { url: string; blob: Blob };

/** memory holds object URLs for the life of the tab, so switching chats does not refetch. */
const memory = new Map<string, Entry>();

/** inflight de-dupes concurrent mounts of the same artifact (a replayed transcript). */
const inflight = new Map<string, Promise<string>>();

/** clearArtifactCache drops the in-memory entries. Tests call this; the tab never needs to. */
export function clearArtifactCache(): void {
  for (const e of memory.values()) URL.revokeObjectURL(e.url);
  memory.clear();
  inflight.clear();
}

async function disk(): Promise<Cache | undefined> {
  try {
    if (typeof caches === "undefined") return undefined;
    return await caches.open(CACHE_NAME);
  } catch {
    return undefined;
  }
}

async function readDisk(artifactId: string): Promise<Blob | undefined> {
  const c = await disk();
  if (!c) return undefined;
  try {
    const hit = await c.match(downloadURL(artifactId));
    if (!hit?.ok) return undefined;
    return await hit.blob();
  } catch {
    return undefined;
  }
}

async function writeDisk(artifactId: string, blob: Blob): Promise<void> {
  const c = await disk();
  if (!c) return;
  try {
    await c.put(
      downloadURL(artifactId),
      new Response(blob, {
        headers: { "Content-Type": blob.type || "application/octet-stream" },
      }),
    );
  } catch {
    /* quota, private mode — the in-memory hit still serves this tab */
  }
}

/**
 * fetchArtifact reads one artifact's bytes through the control plane, with the bearer.
 * Images that this tab (or Cache Storage) already has are not fetched again.
 */
export async function fetchArtifact(artifactId: string): Promise<Blob> {
  const mem = memory.get(artifactId);
  if (mem) return mem.blob;
  const cached = await readDisk(artifactId);
  if (cached) return cached;
  return download(artifactId);
}

async function download(artifactId: string): Promise<Blob> {
  const headers = new Headers();
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);
  const res = await fetch(downloadURL(artifactId), { headers });
  if (!res.ok) {
    if (res.status === 401) notifyRejected();
    const detail = (await res.text()).trim();
    throw new Error(detail === "" ? `download failed with HTTP ${res.status}` : detail);
  }
  return res.blob();
}

/**
 * cachedImageURL is the src for an inline chat image: a blob URL whose bytes come from
 * memory, then Cache Storage, then the network. The URL is not revoked on unmount — the
 * cache owns it — so reopening the conversation does not flash a shimmer and refetch.
 */
export function cachedImageURL(artifactId: string, mime: string): Promise<string> {
  const hit = memory.get(artifactId);
  if (hit) return Promise.resolve(hit.url);
  const pending = inflight.get(artifactId);
  if (pending) return pending;

  const job = (async () => {
    const cached = await readDisk(artifactId);
    const blob = cached ?? (await download(artifactId));
    const typed = blob.type.startsWith("image/") ? blob : new Blob([blob], { type: mime });
    const url = URL.createObjectURL(typed);
    memory.set(artifactId, { url, blob: typed });
    if (!cached) await writeDisk(artifactId, typed);
    return url;
  })().finally(() => inflight.delete(artifactId));

  inflight.set(artifactId, job);
  return job;
}
