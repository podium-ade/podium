import { useEffect, useRef, useState, type KeyboardEvent } from "react";
import { ChevronLeft, ChevronRight, ExternalLink, X } from "lucide-react";
import type { ChatAttachment } from "../../gen/podium/agent/v1/agent_pb";
import { humanBytes } from "../../lib/format";
import { cn } from "../../lib/utils";
import { Dialog, DialogClose, DialogContent, DialogDescription, DialogTitle } from "../ui/dialog";

/**
 * GalleryImage is one picture the gallery can show. `url` is unset while the bytes are still
 * being fetched: the slot keeps its place so the counter and the thumbnails do not shuffle
 * as a message's images arrive in whatever order the network returns them.
 */
export type GalleryImage = { attachment: ChatAttachment; url?: string };

const navButton =
  "grid size-9 shrink-0 place-items-center rounded-md border border-border bg-panel text-fg " +
  "transition-colors duration-150 outline-none hover:bg-raised focus-visible:ring-2 " +
  "focus-visible:ring-ring/50 aria-disabled:cursor-default aria-disabled:opacity-40 aria-disabled:hover:bg-panel";

/**
 * ChatImageGallery shows a message's images one at a time in a box of fixed height, so five
 * charts cost the same vertical space in the thread as one. A single image is the same box
 * without the controls; opening it shows it at full size in a lightbox.
 *
 * The box height is fixed rather than capped on purpose: a capped box resizes as you step
 * between a wide screenshot and a tall one, and the thread jumps under the reader's cursor.
 */
export function ChatImageGallery({ images }: { images: GalleryImage[] }) {
  const [requested, setIndex] = useState(0);
  const [open, setOpen] = useState(false);
  const stage = useRef<HTMLButtonElement>(null);
  const strip = useRef<HTMLDivElement>(null);

  const total = images.length;
  // A failed image leaves the list while the reader is looking at a later one, so the stored
  // position can run past the end.
  const index = Math.min(requested, Math.max(total - 1, 0));
  const current = images[index];
  const many = total > 1;

  // Keep the active thumbnail in view. scrollTo with an explicit "auto" because the strip
  // is a few pixels of travel and smooth scrolling there only reads as lag; guarded because
  // jsdom has no layout to scroll.
  useEffect(() => {
    const el = strip.current?.querySelector<HTMLElement>('[aria-current="true"]');
    if (!el || !strip.current || typeof strip.current.scrollTo !== "function") return;
    const box = strip.current;
    const left = el.offsetLeft - (box.clientWidth - el.clientWidth) / 2;
    box.scrollTo({ left, behavior: "auto" });
  }, [index]);

  if (!current) return null;

  const go = (to: number) => setIndex(Math.min(Math.max(to, 0), total - 1));
  const onKeyDown = (e: KeyboardEvent) => {
    if (!many || e.altKey || e.ctrlKey || e.metaKey) return;
    if (e.key === "ArrowLeft") go(index - 1);
    else if (e.key === "ArrowRight") go(index + 1);
    else return;
    e.preventDefault();
  };

  const { attachment, url } = current;
  const detail = `${attachment.name} · ${humanBytes(attachment.sizeBytes)}`;

  return (
    <>
      <div
        role="group"
        aria-roledescription="carousel"
        aria-label={many ? `${total} images` : "Image"}
        data-testid="chat-gallery"
        onKeyDown={onKeyDown}
        className="w-full max-w-120 space-y-1.5"
      >
        <button
          ref={stage}
          type="button"
          data-testid="chat-attachment"
          title={detail}
          aria-label={`Open ${attachment.name} full size`}
          aria-haspopup="dialog"
          onClick={() => setOpen(true)}
          className="grid h-56 w-full cursor-zoom-in place-items-center overflow-hidden rounded-lg border border-border bg-raised outline-none focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          {url ? (
            <img src={url} alt={attachment.name} className="max-h-full max-w-full object-contain" />
          ) : (
            <span aria-hidden className="size-full animate-shimmer" />
          )}
        </button>

        <div className="flex items-center gap-2">
          {many ? (
            <button
              type="button"
              aria-label="Previous image"
              aria-disabled={index === 0}
              onClick={() => go(index - 1)}
              className={navButton}
            >
              <ChevronLeft aria-hidden className="size-4" />
            </button>
          ) : null}
          <div className="min-w-0 flex-1">
            <span className="block truncate font-mono text-xs text-fg">{attachment.name}</span>
            <span className="tabular block text-2xs text-faint">
              {many ? <span aria-live="polite">{`${index + 1} / ${total}`} · </span> : null}
              {humanBytes(attachment.sizeBytes)}
            </span>
          </div>
          {many ? (
            <button
              type="button"
              aria-label="Next image"
              aria-disabled={index === total - 1}
              onClick={() => go(index + 1)}
              className={navButton}
            >
              <ChevronRight aria-hidden className="size-4" />
            </button>
          ) : null}
        </div>

        {many ? (
          <div
            ref={strip}
            role="group"
            aria-label="Thumbnails"
            className="relative flex gap-1.5 overflow-x-auto pb-1"
          >
            {images.map((img, i) => (
              <button
                key={img.attachment.artifactId}
                type="button"
                aria-label={`Show image ${i + 1} of ${total}: ${img.attachment.name}`}
                aria-current={i === index ? "true" : undefined}
                onClick={() => go(i)}
                className={cn(
                  "grid size-10 shrink-0 place-items-center overflow-hidden rounded-md border bg-raised outline-none",
                  "focus-visible:ring-2 focus-visible:ring-ring/50",
                  i === index
                    ? "border-accent ring-1 ring-accent"
                    : "border-border opacity-70 hover:opacity-100",
                )}
              >
                {img.url ? (
                  // The button's label names the image; a second alt would be read twice.
                  <img src={img.url} alt="" className="size-full object-cover" />
                ) : (
                  <span aria-hidden className="size-full animate-shimmer" />
                )}
              </button>
            ))}
          </div>
        ) : null}
      </div>
      <Lightbox
        images={images}
        index={index}
        open={open}
        onOpenChange={setOpen}
        onIndex={go}
        // Safari does not focus a button on click, so Radix has nothing to hand focus back
        // to; name the stage explicitly.
        onClosed={() => stage.current?.focus()}
      />
    </>
  );
}

const lightboxButton =
  "grid size-10 place-items-center rounded-full bg-white/10 text-white transition-colors " +
  "duration-150 outline-none hover:bg-white/20 focus-visible:ring-2 focus-visible:ring-white/70 " +
  "aria-disabled:cursor-default aria-disabled:opacity-30 aria-disabled:hover:bg-white/10";

/**
 * Lightbox is the full-size view. Radix's Dialog gives the parts that are easy to get
 * subtly wrong: focus is trapped inside while it is open, Esc closes it, and the page behind
 * is inert to assistive technology.
 */
function Lightbox({
  images,
  index,
  open,
  onOpenChange,
  onIndex,
  onClosed,
}: {
  images: GalleryImage[];
  index: number;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onIndex: (to: number) => void;
  onClosed: () => void;
}) {
  const total = images.length;
  const many = total > 1;
  const current = images[index];
  if (!current) return null;
  const { attachment, url } = current;

  const onKeyDown = (e: KeyboardEvent) => {
    if (!many || e.altKey || e.ctrlKey || e.metaKey) return;
    if (e.key === "ArrowLeft") onIndex(index - 1);
    else if (e.key === "ArrowRight") onIndex(index + 1);
    else return;
    e.preventDefault();
  };

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent
        showClose={false}
        data-testid="chat-lightbox"
        onKeyDown={onKeyDown}
        onCloseAutoFocus={(e) => {
          e.preventDefault();
          onClosed();
        }}
        className="top-0 left-0 flex h-dvh w-screen max-w-none translate-x-0 translate-y-0 flex-col gap-0 rounded-none border-0 bg-[#070f18]/92 p-0 text-white shadow-none"
      >
        <DialogTitle className="sr-only">{attachment.name}</DialogTitle>
        <DialogDescription className="sr-only">
          {many
            ? `Image ${index + 1} of ${total}. Left and right arrow keys move between images; Escape closes.`
            : "Escape closes."}
        </DialogDescription>

        <div className="flex items-center gap-3 px-4 py-3">
          <div className="min-w-0 flex-1">
            <span className="block truncate font-mono text-xs">{attachment.name}</span>
            <span className="tabular block text-2xs text-white/70">
              {many ? `${index + 1} / ${total} · ` : ""}
              {humanBytes(attachment.sizeBytes)}
            </span>
          </div>
          {url ? (
            <a
              href={url}
              target="_blank"
              rel="noreferrer noopener"
              aria-label={`Open ${attachment.name} in a new tab`}
              className={lightboxButton}
            >
              <ExternalLink aria-hidden className="size-4" />
            </a>
          ) : null}
          <DialogClose aria-label="Close" className={lightboxButton}>
            <X aria-hidden className="size-4" />
          </DialogClose>
        </div>

        {/* A click on the empty space around the picture dismisses, which is what a lightbox
            teaches people to expect; the controls and the image are not that space. */}
        <div
          className="relative flex min-h-0 flex-1 items-center justify-center px-14 pb-4 sm:px-20"
          onClick={(e) => {
            if (e.target === e.currentTarget) onOpenChange(false);
          }}
        >
          {url ? (
            <img
              src={url}
              alt={attachment.name}
              className="max-h-full max-w-full rounded-md object-contain"
            />
          ) : (
            <span aria-hidden className="h-1/2 w-2/3 animate-shimmer rounded-md" />
          )}
          {many ? (
            <>
              <button
                type="button"
                aria-label="Previous image"
                aria-disabled={index === 0}
                onClick={() => onIndex(index - 1)}
                className={cn(lightboxButton, "absolute top-1/2 left-2 -translate-y-1/2 sm:left-5")}
              >
                <ChevronLeft aria-hidden className="size-5" />
              </button>
              <button
                type="button"
                aria-label="Next image"
                aria-disabled={index === total - 1}
                onClick={() => onIndex(index + 1)}
                className={cn(
                  lightboxButton,
                  "absolute top-1/2 right-2 -translate-y-1/2 sm:right-5",
                )}
              >
                <ChevronRight aria-hidden className="size-5" />
              </button>
            </>
          ) : null}
        </div>
      </DialogContent>
    </Dialog>
  );
}
