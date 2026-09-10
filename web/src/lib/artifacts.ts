import type { Artifact } from "../gen/podium/v1/artifact_pb";
import { getToken, notifyRejected } from "./auth";

/** The server's streaming artifact route. Not a Connect procedure: a large artifact must stream. */
export const DOWNLOAD_PREFIX = "/artifacts/";

export function downloadURL(artifactID: string): string {
  return DOWNLOAD_PREFIX + encodeURIComponent(artifactID);
}

/**
 * download fetches an artifact's bytes through the control plane and hands them to the browser.
 *
 * `GET /artifacts/{id}` is used rather than `GetArtifactURL`'s presigned link for two reasons.
 * The presigned URL points at the object store, and a browser on a tailnet usually has no route
 * to it — the control plane does, which is the whole reason the proxy route exists. And a
 * presigned URL expires in 15 minutes, so a page that rendered one as an href would hand out a
 * link that silently stops working while it is still on screen. `GetArtifactURL` is still the
 * right call for "copy a link to send to something that is not this browser", which is what the
 * copy-link affordance uses.
 *
 * The route sits behind the same identity middleware as every RPC, so a plain <a download> would
 * be answered with 401 under the local transport. Hence fetch with the header, then a blob URL.
 */
export async function download(artifact: Artifact): Promise<void> {
  const headers = new Headers();
  const token = getToken();
  if (token) headers.set("Authorization", `Bearer ${token}`);

  const res = await fetch(downloadURL(artifact.id), { headers });
  if (!res.ok) {
    if (res.status === 401) notifyRejected();
    const detail = (await res.text()).trim();
    throw new Error(detail === "" ? `download failed with HTTP ${res.status}` : detail);
  }
  const blob = await res.blob();
  const url = URL.createObjectURL(blob);
  try {
    const a = document.createElement("a");
    a.href = url;
    a.download = artifact.name || artifact.id;
    a.rel = "noopener";
    a.click();
  } finally {
    // Revoking immediately is safe: the click has already handed the blob to the download
    // manager, which holds its own reference.
    URL.revokeObjectURL(url);
  }
}
