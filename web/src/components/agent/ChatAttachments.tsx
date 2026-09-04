import { useEffect, useState } from "react";
import type { ChatAttachment } from "../../gen/podium/agent/v1/agent_pb";
import { downloadURL } from "../../lib/artifacts";
import { getToken, notifyRejected } from "../../lib/auth";
import { humanBytes } from "../../lib/format";
import { useToast } from "../Toast";

/** MAX_IMAGE_PX is how wide an inline image is allowed to render. */
const MAX_IMAGE_PX = 480;

/**
 * RASTER_IMAGE is the fallback for deciding what to show inline.
 *
 * It exists because **a Podium artifact usually has no content type at all.** The node
 * records one only when a task calls `podium-runner artifact add --content-type`; a file the
 * agent simply writes into `/workspace/.podium/artifacts/` is auto-collected with an empty
 * type and served as `application/octet-stream`. Deciding on the content type alone — which
 * is what a chart-producing turn would hit every time — would render every PNG as a
 * download chip.
 *
 * SVG is deliberately absent, and `image/svg+xml` is refused below even when the store does
 * name it. An SVG is a document with scripts in it, and while `<img src>` will not run them,
 * the "click to open" link hands the viewer a `blob:` URL — which inherits THIS origin, the
 * one holding the dev token in localStorage. A task-produced SVG opened in a tab would be
 * script execution on the app's origin. It is a download chip instead.
 */
const RASTER_IMAGE = /\.(png|jpe?g|gif|webp|avif|bmp|ico)$/i;

/**
 * IMAGE_MIME is the type a blob has to be GIVEN before a browser will decode it.
 *
 * Not a nicety: `GET /artifacts/{id}` answers `application/octet-stream` for an artifact the
 * store has no type for, `Response.blob()` carries that type through, and a `blob:` URL with
 * that type does not render in an `<img>` at all — no sniffing, `naturalWidth` stays 0 and
 * the page shows an empty box. So the bytes are re-wrapped with the type the extension
 * implies. Only these types are ever produced, which is also what keeps `image/svg+xml`
 * — scriptable, and a blob URL inherits this origin — out of an <img> and out of a new tab.
 */
const IMAGE_MIME: Record<string, string> = {
  png: "image/png",
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  gif: "image/gif",
  webp: "image/webp",
  avif: "image/avif",
  bmp: "image/bmp",
  ico: "image/x-icon",
};

/** inlineImage reports whether this file should be shown rather than offered. */
function inlineImage(a: ChatAttachment): boolean {
  if (a.contentType === "image/svg+xml") return false;
  if (a.contentType.startsWith("image/")) return true;
  return RASTER_IMAGE.test(a.name);
}

/** imageMIME is the type to give the blob: the store's when it has one, else the name's. */
function imageMIME(a: ChatAttachment): string {
  if (a.contentType.startsWith("image/")) return a.contentType;
  const ext = a.name.slice(a.name.lastIndexOf(".") + 1).toLowerCase();
  return IMAGE_MIME[ext] ?? "image/png";
}

/**
 * ChatAttachments renders the files a turn produced: images inline, everything else as a
 * download chip.
 *
 * `GET /artifacts/{id}` sits behind the same identity middleware as every RPC, and an
 * `<img src>` cannot carry an Authorization header — so an image is fetched with the bearer
 * and shown from a blob URL. That is also why the src is never the artifact route directly:
 * under the dev transport it would be a 401 and a broken-image icon.
 */
export function ChatAttachments({ attachments }: { attachments: ChatAttachment[] }) {
  if (attachments.length === 0) return null;
  return (
    <div className="mt-2 space-y-2">
      {attachments.map((a) =>
        inlineImage(a) ? (
          <InlineImage key={a.artifactId} attachment={a} />
        ) : (
          <FileChip key={a.artifactId} attachment={a} />
        ),
      )}
    </div>
  );
}

/** fetchArtifact reads one artifact's bytes through the control plane, with the bearer. */
async function fetchArtifact(artifactId: string): Promise<Blob> {
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

function InlineImage({ attachment }: { attachment: ChatAttachment }) {
  const [url, setUrl] = useState<string>();
  const [error, setError] = useState<string>();
  // The two things the fetch needs, pulled out so the effect depends on values rather than
  // on a message object that is replaced whenever the turn republishes it.
  const { artifactId } = attachment;
  const mime = imageMIME(attachment);

  useEffect(() => {
    let objectURL: string | undefined;
    let cancelled = false;
    void (async () => {
      try {
        const blob = await fetchArtifact(artifactId);
        if (cancelled) return;
        objectURL = URL.createObjectURL(
          blob.type.startsWith("image/") ? blob : new Blob([blob], { type: mime }),
        );
        setUrl(objectURL);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      }
    })();
    return () => {
      cancelled = true;
      if (objectURL) URL.revokeObjectURL(objectURL);
    };
  }, [artifactId, mime]);

  if (error) return <FileChip attachment={attachment} />;
  if (!url) {
    return (
      <div
        data-testid="chat-attachment"
        className="h-24 w-full max-w-120 animate-pulse rounded border border-border bg-raised"
      />
    );
  }
  return (
    <a
      data-testid="chat-attachment"
      href={url}
      target="_blank"
      rel="noreferrer noopener"
      title={`${attachment.name} · ${humanBytes(attachment.sizeBytes)}`}
      className="block"
    >
      <img
        src={url}
        alt={attachment.name}
        style={{ maxWidth: MAX_IMAGE_PX }}
        className="rounded border border-border"
      />
    </a>
  );
}

function FileChip({ attachment }: { attachment: ChatAttachment }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);

  return (
    <button
      type="button"
      data-testid="chat-attachment"
      disabled={busy}
      onClick={() => {
        setBusy(true);
        void (async () => {
          try {
            const blob = await fetchArtifact(attachment.artifactId);
            const url = URL.createObjectURL(blob);
            try {
              const a = document.createElement("a");
              a.href = url;
              a.download = attachment.name || attachment.artifactId;
              a.rel = "noopener";
              a.click();
            } finally {
              URL.revokeObjectURL(url);
            }
          } catch (err) {
            toast(err instanceof Error ? err.message : String(err));
          } finally {
            setBusy(false);
          }
        })();
      }}
      className="flex items-center gap-2 rounded border border-border bg-raised px-2 py-1 text-xs text-muted hover:text-fg disabled:opacity-60 focus-visible:ring-1 focus-visible:ring-accent"
    >
      <span className="font-mono text-fg">{attachment.name}</span>
      <span>·</span>
      <span>{humanBytes(attachment.sizeBytes)}</span>
      <span>{busy ? "downloading…" : "download"}</span>
    </button>
  );
}
