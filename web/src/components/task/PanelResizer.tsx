import { useRef, type KeyboardEvent, type PointerEvent } from "react";
import { GripVertical } from "lucide-react";
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
  label = "Resize the entry panel",
  minRest = MIN_SIDE_PX,
}: {
  /** width is the panel's current width in px, or undefined while it is the default. */
  width: number | undefined;
  onWidth: (px: number) => void;
  onReset: () => void;
  /** container is the split the handle divides. */
  container: React.RefObject<HTMLElement | null>;
  /** label names the handle for assistive technology: what it resizes. */
  label?: string;
  /** minRest is the narrowest the other side may get, when it needs more than MIN_SIDE_PX. */
  minRest?: number;
}) {
  const dragging = useRef(false);

  const clamp = (px: number) => {
    const total = container.current?.getBoundingClientRect().width ?? 0;
    const max = Math.max(MIN_SIDE_PX, total - minRest);
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
      aria-label={label}
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
        // The handle takes no width of its own: the seam between the two sides is one line,
        // drawn on the panel's own edge, not a second hairline beside it with a gap between.
        // What a pointer can grab is wider than what shows, laid over the seam and mostly on
        // the panel's side, so it does not steal the list's scroll bar on the other.
        "group/resize relative z-10 hidden w-0 shrink-0 cursor-col-resize touch-none outline-none select-none lg:block",
        "after:absolute after:inset-y-0 after:-left-0.5 after:w-3",
        "before:absolute before:inset-y-0 before:left-0 before:w-0.5 before:bg-hairline before:transition-[width,background-color] before:duration-150",
        "hover:before:w-[3px] hover:before:bg-accent focus-visible:before:w-[3px] focus-visible:before:bg-accent",
      )}
    >
      {/* A bordered tab with a grip, not a bare bar: a bar the scroll thumb's shape and
          colour, beside the scroll thumb, read as a second scroll bar. */}
      <span
        aria-hidden
        className="absolute top-1/2 left-px flex h-7 w-4 -translate-x-1/2 -translate-y-1/2 items-center justify-center rounded-md border border-border bg-panel text-faint shadow-sm transition-colors duration-150 group-hover/resize:border-accent group-hover/resize:text-accent group-focus-visible/resize:border-accent group-focus-visible/resize:text-accent"
      >
        <GripVertical className="size-3" />
      </span>
    </div>
  );
}
