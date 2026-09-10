import { useEffect, useState } from "react";
import { Download, FileText, Loader2 } from "lucide-react";
import type { ChatAttachment } from "../../gen/podium/agent/v1/agent_pb";
import { cachedImageURL, fetchArtifact } from "../../lib/artifactCache";
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
 * under the local transport it would be a 401 and a broken-image icon.
 */
export function ChatAttachments({ attachments }: { attachments: ChatAttachment[] }) {
  if (attachments.length === 0) return null;
  return (
    <div className="mt-2.5 flex flex-wrap items-start gap-2">
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

function InlineImage({ attachment }: { attachment: ChatAttachment }) {
  const [url, setUrl] = useState<string>();
  const [error, setError] = useState<string>();
  // The two things the fetch needs, pulled out so the effect depends on values rather than
  // on a message object that is replaced whenever the turn republishes it.
  const { artifactId } = attachment;
  const mime = imageMIME(attachment);

  useEffect(() => {
    let cancelled = false;
    void (async () => {
      try {
        const objectURL = await cachedImageURL(artifactId, mime);
        if (!cancelled) setUrl(objectURL);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [artifactId, mime]);

  if (error) return <FileChip attachment={attachment} />;
  if (!url) {
    return (
      <div
        data-testid="chat-attachment"
        className="h-24 w-full max-w-120 animate-shimmer rounded-lg border border-border bg-raised"
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
      className="block rounded-lg outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
    >
      <img
        src={url}
        alt={attachment.name}
        style={{ maxWidth: MAX_IMAGE_PX }}
        className="rounded-lg border border-border"
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
      aria-label={`Download ${attachment.name}`}
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
      className="flex max-w-full items-center gap-2.5 rounded-lg border border-border bg-panel py-1.5 pr-2.5 pl-2 text-left shadow-xs transition-colors duration-150 outline-none hover:border-muted/45 hover:bg-raised/60 disabled:opacity-60 focus-visible:ring-2 focus-visible:ring-ring/50"
    >
      <span className="grid size-7 shrink-0 place-items-center rounded-md border border-hairline bg-raised text-muted">
        {busy ? (
          <Loader2 className="size-3.5 animate-spin" />
        ) : (
          <FileText className="size-3.5" />
        )}
      </span>
      <span className="min-w-0">
        <span className="block truncate font-mono text-xs text-fg">{attachment.name}</span>
        <span className="tabular block text-2xs text-faint">
          {busy ? "downloading…" : `${humanBytes(attachment.sizeBytes)} · download`}
        </span>
      </span>
      <Download aria-hidden className="size-3.5 shrink-0 text-faint" />
    </button>
  );
}
