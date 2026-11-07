import { useLayoutEffect, useRef, useState, type RefObject } from "react";
import { cn } from "../../lib/utils";
import { PanelResizer } from "../task/PanelResizer";
import { DesktopPanel } from "./DesktopPanel";

/** How long the pane takes to open and to close. */
const OPEN_MS = 260;
const CLOSE_MS = 200;
/** The design system's --ease-out-quint, which the Web Animations API cannot read from CSS. */
const EASE = "cubic-bezier(0.2, 0.8, 0.2, 1)";

/** wide is the side-by-side layout (lg), where the pane is a column whose width can animate. */
const wide = () => window.matchMedia?.("(min-width: 1024px)").matches ?? false;
const still = () => window.matchMedia?.("(prefers-reduced-motion: reduce)").matches ?? false;

/**
 * DesktopPane is the desktop's half of the chat: the resizer and the panel, opening and closing
 * with a motion of its own. Side by side, the pane's column grows from nothing to its width and
 * back, so the conversation narrows and widens with it instead of jumping; stacked, it fades
 * and rises.
 *
 * While the column animates, the panel inside keeps the width it ends (or started) at and the
 * column clips it. The stream inside fits the desktop to its frame, so a frame that changed
 * width on every animation step would resize the agent's screen as many times.
 *
 * The animations are the Web Animations API's, run where the motion starts — the mount for
 * opening, the Hide button for closing — so nothing waits on a render to animate. With
 * reduced motion, or where the API is missing, the pane simply appears and goes.
 */
export function DesktopPane({
  taskId,
  width,
  onWidth,
  onReset,
  container,
  onHide,
}: {
  taskId: string;
  width: number | undefined;
  onWidth: (w: number) => void;
  onReset: () => void;
  container: RefObject<HTMLDivElement | null>;
  onHide: () => void;
}) {
  const column = useRef<HTMLDivElement>(null);
  const inner = useRef<HTMLDivElement>(null);
  const [closing, setClosing] = useState(false);

  useLayoutEffect(() => {
    const col = column.current;
    const body = inner.current;
    if (!col || !body || typeof col.animate !== "function" || still()) return;
    if (!wide()) {
      col.animate(
        [
          { opacity: 0, transform: "translateY(12px)" },
          { opacity: 1, transform: "none" },
        ],
        { duration: OPEN_MS, easing: EASE },
      );
      return;
    }
    const to = col.getBoundingClientRect().width;
    body.style.width = `${to}px`;
    const a = col.animate(
      [
        { width: "0px", opacity: 0 },
        { width: `${to}px`, opacity: 1 },
      ],
      { duration: OPEN_MS, easing: EASE },
    );
    const release = () => {
      body.style.width = "";
    };
    a.onfinish = release;
    a.oncancel = release;
    return () => a.cancel();
  }, []);

  const close = () => {
    const col = column.current;
    const body = inner.current;
    if (closing) return;
    if (!col || !body || typeof col.animate !== "function" || still()) {
      onHide();
      return;
    }
    setClosing(true);
    const from = col.getBoundingClientRect().width;
    const frames = wide()
      ? [
          { width: `${from}px`, opacity: 1 },
          { width: "0px", opacity: 0 },
        ]
      : [
          { opacity: 1, transform: "none" },
          { opacity: 0, transform: "translateY(12px)" },
        ];
    if (wide()) body.style.width = `${from}px`;
    const a = col.animate(frames, { duration: CLOSE_MS, easing: EASE, fill: "forwards" });
    a.onfinish = onHide;
  };

  return (
    <>
      <div
        className={cn(
          "contents transition-opacity duration-200 motion-reduce:transition-none",
          closing && "[&>*]:opacity-0",
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
        className="flex min-h-[60vh] min-w-0 flex-col overflow-hidden lg:min-h-0 lg:w-[var(--desktop-width,55%)]"
      >
        <div ref={inner} className="flex min-h-0 flex-1 flex-col">
          <DesktopPanel taskId={taskId} onHide={close} />
        </div>
      </div>
    </>
  );
}
