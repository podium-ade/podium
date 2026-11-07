import { useLayoutEffect, useRef, type RefObject } from "react";
import { cn } from "../../lib/utils";
import { PanelResizer } from "../task/PanelResizer";
import { DesktopPanel } from "./DesktopPanel";

/**
 * How long the pane takes to open and to close, and how it moves. Opening eases out: it
 * arrives quickly and settles. Closing eases in and out over longer: on an ease-out curve the
 * pane had lost most of its width in the first frames, and closing read as a snap.
 */
const OPEN_MS = 280;
const CLOSE_MS = 340;
/** The design system's --ease-out-quint, which the Web Animations API cannot read from CSS. */
const EASE_OUT = "cubic-bezier(0.2, 0.8, 0.2, 1)";
const EASE_IN_OUT = "cubic-bezier(0.65, 0, 0.35, 1)";

/** wide is the side-by-side layout (lg), where the pane is a column whose width can animate. */
const wide = () => window.matchMedia?.("(min-width: 1024px)").matches ?? false;
const still = () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;

/**
 * DesktopPane is the desktop's half of the chat: the resizer and the panel, opening and closing
 * with a motion of its own. Side by side, the pane's column grows from nothing to its width and
 * back, so the conversation narrows and widens with it instead of jumping; stacked, it fades
 * and rises.
 *
 * It closes however the desktop goes — the Hide button, or the session ending: the chat keeps
 * it mounted with shown false, it animates out, and onClosed is when the chat lets it go.
 *
 * While the column animates, the panel inside keeps the width it ends (or started) at and the
 * column clips it. The stream inside fits the desktop to its frame, so a frame that changed
 * width on every animation step would resize the agent's screen as many times.
 *
 * The animations are the Web Animations API's, so nothing waits on a render to move. With
 * reduced motion, or where the API is missing, the pane simply appears and goes.
 */
export function DesktopPane({
  taskId,
  shown,
  width,
  onWidth,
  onReset,
  container,
  onHide,
  onClosed,
}: {
  taskId: string;
  shown: boolean;
  width: number | undefined;
  onWidth: (w: number) => void;
  onReset: () => void;
  container: RefObject<HTMLDivElement | null>;
  onHide: () => void;
  onClosed: () => void;
}) {
  const column = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  // The latest onClosed, for an animation that finishes after a render replaced it.
  const closed = useRef(onClosed);
  useLayoutEffect(() => {
    closed.current = onClosed;
  });

  useLayoutEffect(() => {
    const col = column.current;
    const body = inner.current;
    const animates = !!col && !!body && typeof col.animate === "function" && !still();
    if (!shown && !animates) {
      closed.current();
      return;
    }
    if (!col || !body || !animates) return;

    const side = wide();
    // Whatever the column was doing, it starts from where it is now: a pane hidden while it
    // was still opening closes from its width at that moment, not from its full width.
    const now = col.getBoundingClientRect().width;
    col.getAnimations().forEach((a) => a.cancel());
    const full = col.getBoundingClientRect().width;
    const release = () => {
      body.style.width = "";
    };

    let a: Animation;
    if (shown) {
      if (side) {
        body.style.width = `${full}px`;
        a = col.animate(
          [
            { width: `${Math.min(now, full) === full ? 0 : now}px`, opacity: 0 },
            { width: `${full}px`, opacity: 1 },
          ],
          { duration: OPEN_MS, easing: EASE_OUT },
        );
      } else {
        a = col.animate(
          [
            { opacity: 0, transform: "translateY(12px)" },
            { opacity: 1, transform: "none" },
          ],
          { duration: OPEN_MS, easing: EASE_OUT },
        );
      }
      a.onfinish = release;
      a.oncancel = release;
    } else {
      if (side) {
        body.style.width = `${full}px`;
        a = col.animate(
          [
            { width: `${now}px`, opacity: 1 },
            { width: "0px", opacity: 0 },
          ],
          { duration: CLOSE_MS, easing: EASE_IN_OUT, fill: "forwards" },
        );
      } else {
        a = col.animate(
          [
            { opacity: 1, transform: "none" },
            { opacity: 0, transform: "translateY(12px)" },
          ],
          { duration: CLOSE_MS, easing: EASE_IN_OUT, fill: "forwards" },
        );
      }
      a.onfinish = () => closed.current();
    }
    return () => {
      a.onfinish = null;
      a.cancel();
    };
  }, [shown]);

  return (
    <>
      <div
        className={cn(
          "contents transition-opacity duration-300 motion-reduce:transition-none",
          !shown && "pointer-events-none [&>*]:opacity-0",
        )}
      >
        <PanelResizer
          width={width}
          onWidth={onWidth}
          onReset={onReset}
          container={container}
          label="Resize the desktop"
        />
      </div>
      <div
        ref={column}
        data-testid="desktop-pane"
        aria-hidden={!shown || undefined}
        className={cn(
          "flex min-h-[60vh] min-w-0 flex-col overflow-hidden lg:min-h-0 lg:w-[var(--desktop-width,55%)]",
          !shown && "pointer-events-none",
        )}
      >
        <div ref={inner} className="flex min-h-0 flex-1 flex-col">
          <DesktopPanel taskId={taskId} onHide={onHide} />
        </div>
      </div>
    </>
  );
}
