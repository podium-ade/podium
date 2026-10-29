import { useRef, type KeyboardEvent, type PointerEvent } from "react";
import { cn } from "../../lib/utils";

/** The narrowest either side of the split may get, list and panel alike. */
export const MIN_SIDE_PX = 320;
/** How far one arrow key moves the split. */
const KEY_STEP_PX = 32;

/**
 * PanelResizer is the handle between the log list and the open record. Dragging it, or the
 * arrow keys while it has focus, sets how wide the panel is; a double click gives the
 * width back to the default.
 *
 * The width is the panel's, measured from the right edge of the split, so the list is what
 * grows and shrinks with the window.
 */
export function PanelResizer({
  width,
  onWidth,
  onReset,
  container,
}: {
  /** width is the panel's current width in px, or undefined while it is the default. */
  width: number | undefined;
  onWidth: (px: number) => void;
  onReset: () => void;
  /** container is the split the handle divides. */
  container: React.RefObject<HTMLElement | null>;
}) {
  const dragging = useRef(false);

  const clamp = (px: number) => {
    const total = container.current?.getBoundingClientRect().width ?? 0;
    const max = Math.max(MIN_SIDE_PX, total - MIN_SIDE_PX);
    return Math.round(Math.min(max, Math.max(MIN_SIDE_PX, px)));
  };
  const current = () => {
    if (width !== undefined) return width;
    const panel = container.current?.lastElementChild as HTMLElement | null;
    return panel?.getBoundingClientRect().width ?? MIN_SIDE_PX;
  };

  const down = (e: PointerEvent<HTMLDivElement>) => {
    e.preventDefault();
    dragging.current = true;
    e.currentTarget.setPointerCapture?.(e.pointerId);
  };
  const move = (e: PointerEvent<HTMLDivElement>) => {
    if (!dragging.current) return;
    const right = container.current?.getBoundingClientRect().right ?? 0;
    onWidth(clamp(right - e.clientX));
  };
  const up = (e: PointerEvent<HTMLDivElement>) => {
    dragging.current = false;
    e.currentTarget.releasePointerCapture?.(e.pointerId);
  };
  const key = (e: KeyboardEvent<HTMLDivElement>) => {
    // Left widens the panel: the handle moves the way the arrow points.
    if (e.key === "ArrowLeft") onWidth(clamp(current() + KEY_STEP_PX));
    else if (e.key === "ArrowRight") onWidth(clamp(current() - KEY_STEP_PX));
    else if (e.key === "Home") onReset();
    else return;
    e.preventDefault();
  };

  return (
    <div
      role="separator"
      aria-orientation="vertical"
      aria-label="Resize the entry panel"
      aria-valuenow={width}
      tabIndex={0}
      onPointerDown={down}
      onPointerMove={move}
      onPointerUp={up}
      onPointerCancel={up}
      onDoubleClick={onReset}
      onKeyDown={key}
      title="Drag to resize · double-click to reset"
      className={cn(
        "group/resize relative hidden cursor-col-resize touch-none outline-none select-none lg:block",
        "before:absolute before:inset-y-0 before:left-1/2 before:w-px before:-translate-x-1/2 before:bg-hairline before:transition-all",
        "hover:before:w-0.5 hover:before:bg-accent focus-visible:before:w-0.5 focus-visible:before:bg-accent",
      )}
    >
      <span
        aria-hidden
        className="absolute top-1/2 left-1/2 h-8 w-1 -translate-x-1/2 -translate-y-1/2 rounded-full bg-border transition-colors group-hover/resize:bg-accent group-focus-visible/resize:bg-accent"
      />
    </div>
  );
}
