import { useCallback, useEffect, useState } from "react";
import { Download, FileText, Loader2 } from "lucide-react";
import type { ChatAttachment } from "../../gen/podium/agent/v1/agent_pb";
import { cachedImageURL, fetchArtifact } from "../../lib/artifactCache";
import { humanBytes } from "../../lib/format";
import { useToast } from "../Toast";
import { ChatImageGallery } from "./ChatImageGallery";

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
 * ChatAttachments renders the files a turn produced: images in one compact gallery,
 * everything else as a download chip.
 *
 * `GET /artifacts/{id}` sits behind the same identity middleware as every RPC, and an
 * `<img src>` cannot carry an Authorization header — so an image is fetched with the bearer
 * and shown from a blob URL. That is also why the src is never the artifact route directly:
 * under the local transport it would be a 401 and a broken-image icon.
 */
export function ChatAttachments({ attachments }: { attachments: ChatAttachment[] }) {
  const images = attachments.filter(inlineImage);
  const loaded = useImageURLs(images);
  // Fetching the bytes can succeed and decoding still fail: a file named .png that is not one.
  // The browser reports that on the <img>, and by then the gallery has counted it.
  const [undecodable, setUndecodable] = useState<ReadonlySet<string>>(new Set());
  const markUndecodable = useCallback(
    (id: string) => setUndecodable((prev) => (prev.has(id) ? prev : new Set(prev).add(id))),
    [],
  );
  if (attachments.length === 0) return null;

  // An image whose bytes could not be read or decoded is a chip: a broken-image icon says nothing,
  // while the chip names the file and can retry the download.
  const failed = (a: ChatAttachment) =>
    loaded[a.artifactId]?.error || undecodable.has(a.artifactId);
  const shown = images.filter((a) => !failed(a));
  const chips = attachments.filter((a) => !inlineImage(a) || failed(a));

  return (
    <div className="mt-2.5 space-y-2">
      {shown.length > 0 ? (
        <ChatImageGallery
          images={shown.map((attachment) => ({
            attachment,
            url: loaded[attachment.artifactId]?.url,
          }))}
          onBroken={markUndecodable}
        />
      ) : null}
      {chips.length > 0 ? (
        <div className="flex flex-wrap items-start gap-2">
          {chips.map((a) => (
            <FileChip key={a.artifactId} attachment={a} />
          ))}
        </div>
      ) : null}
    </div>
  );
}

type Loaded = Record<string, { url?: string; error?: string }>;

/**
 * useImageURLs fetches every image of a message up front, so the gallery can show the
 * counter and thumbnails for all of them and a failed one can drop out as a chip.
 */
function useImageURLs(images: ChatAttachment[]): Loaded {
  const [loaded, setLoaded] = useState<Loaded>({});
  // The effect depends on this string rather than on the attachments: a message object is
  // replaced whenever the turn republishes it, and that must not refetch.
  const key = JSON.stringify(images.map((a) => [a.artifactId, imageMIME(a)]));

  useEffect(() => {
    let cancelled = false;
    const wanted = JSON.parse(key) as [string, string][];
    for (const [artifactId, mime] of wanted) {
      void (async () => {
        let next: Loaded[string];
        try {
          next = { url: await cachedImageURL(artifactId, mime) };
        } catch (err) {
          next = { error: err instanceof Error ? err.message : String(err) };
        }
        if (!cancelled) setLoaded((prev) => ({ ...prev, [artifactId]: next }));
      })();
    }
    return () => {
      cancelled = true;
    };
  }, [key]);

  return loaded;
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
      className="flex max-w-full items-center gap-2.5 rounded-lg border border-border bg-panel py-1.5 pr-2.5 pl-2 text-left shadow-xs transition-colors duration-150 outline-none hover:border-line-strong hover:bg-raised disabled:opacity-60 focus-visible:ring-2 focus-visible:ring-ring/50"
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
