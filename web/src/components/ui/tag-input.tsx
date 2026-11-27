import { useEffect, useId, useRef, useState, type KeyboardEvent } from "react";
import { CornerDownLeft, Plus, X } from "lucide-react";
import { cn } from "@/lib/utils";
import { Popover, PopoverAnchor, PopoverContent } from "./popover";

// How long typing pauses before the dropdown says what Enter will do.
const SUGGEST_DELAY_MS = 200;
const MAX_SUGGESTIONS = 6;

/** One row of the dropdown: a value Enter or a click adds, or why it cannot be. */
type Row = { value: string; label: string; typed: boolean; blocked?: string };

/**
 * TagInput is a list of short values typed into one field: each value a tag inside it, the
 * caret after the last. Enter adds what was typed, and so does a comma unless commaSeparates is
 * false (a branch pattern may hold one); leaving the field adds it too, so nothing typed is lost
 * on save. A pasted list adds each entry.
 *
 * A pause in typing opens a dropdown under the field: what was typed and what Enter does with it
 * (adds it to the list, or why it cannot), then the options that match. Arrow keys move through
 * it, and Enter or a click takes a row.
 *
 * Backspace in the empty field selects the last tag, and a second press removes it, so one
 * keystroke too many does not silently drop a value. A value already there is not added twice;
 * its tag is pointed out instead. validate refuses a value with the reason, which stays in the
 * field to be fixed. With offer, the options not yet chosen are also buttons under the field,
 * which suits a short list such as a workspace's teams.
 */
export function TagInput({
  id,
  testId,
  values,
  onChange,
  placeholder,
  mono,
  clean = (v) => v,
  validate,
  commaSeparates = true,
  options = [],
  offer,
  describedBy,
  noun = "value",
}: {
  id: string;
  testId?: string;
  values: string[];
  onChange: (values: string[]) => void;
  /** Shown while there is no tag. */
  placeholder?: string;
  mono?: boolean;
  /** Tidies a value before it is checked and added; "" drops it. */
  clean?: (value: string) => string;
  /** Returns why a value cannot be added, or nothing. */
  validate?: (value: string) => string | undefined;
  commaSeparates?: boolean;
  options?: { value: string; label: string }[];
  offer?: boolean;
  describedBy?: string;
  /** Names one value in what a screen reader hears: "alice added", "Remove login alice". */
  noun?: string;
}) {
  const [text, setText] = useState("");
  const [error, setError] = useState("");
  // The tag Backspace selected, which the next Backspace removes.
  const [armed, setArmed] = useState(false);
  // The tag a duplicate pointed at, for a moment.
  const [flash, setFlash] = useState("");
  const [announce, setAnnounce] = useState("");
  // The dropdown, opened once typing pauses, and its highlighted row.
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);
  const input = useRef<HTMLInputElement>(null);
  const errorId = useId();
  const listId = useId();
  const same = (a: string, b: string) => a.toLowerCase() === b.toLowerCase();

  // Once open, the dropdown follows the typing until a value is added or the field empties.
  useEffect(() => {
    if (text.trim() === "" || open) return;
    const t = setTimeout(() => {
      setOpen(true);
      setActive(0);
    }, SUGGEST_DELAY_MS);
    return () => clearTimeout(t);
  }, [text, open]);

  useEffect(() => {
    if (!flash) return;
    const t = setTimeout(() => setFlash(""), 900);
    return () => clearTimeout(t);
  }, [flash]);

  const add = (raw: string) => {
    const next = [...values];
    const added: string[] = [];
    let rest = "";
    let why = "";
    let dup = "";
    for (const part of raw.split(commaSeparates ? /[,\n]/ : /\n/)) {
      const v = clean(part.trim()).trim();
      if (v === "") continue;
      const existing = next.find((x) => same(x, v));
      if (existing) {
        dup = existing;
        continue;
      }
      const bad = validate?.(v);
      if (bad) {
        // The first value refused stays in the field, with why, to be fixed.
        if (!why) {
          why = bad;
          rest = part.trim();
        }
        continue;
      }
      next.push(v);
      added.push(v);
    }
    setText(rest);
    setError(why);
    setArmed(false);
    setOpen(false);
    if (dup && added.length === 0) {
      setFlash(dup);
      setAnnounce(`${dup} is already in the list`);
    }
    if (added.length > 0) {
      onChange(next);
      setAnnounce(`${added.join(", ")} added`);
    }
  };

  const remove = (v: string, refocus: boolean) => {
    onChange(values.filter((x) => x !== v));
    setArmed(false);
    setAnnounce(`${v} removed`);
    if (refocus) input.current?.focus();
  };

  // The dropdown's rows: what was typed (or the option it names exactly), then the options
  // that contain it.
  const typed = clean(text.trim()).trim();
  const rows: Row[] = [];
  if (typed !== "") {
    const there = values.find((x) => same(x, typed));
    const exact = options.find((o) => same(o.value, typed));
    if (exact && !there) rows.push({ value: exact.value, label: exact.label, typed: false });
    else rows.push({ value: typed, label: typed, typed: true, blocked: there ? "Already in the list" : validate?.(typed) });
    const q = typed.toLowerCase();
    for (const o of options) {
      if (rows.length > MAX_SUGGESTIONS) break;
      if (same(o.value, typed) || values.some((x) => same(x, o.value))) continue;
      if (o.value.toLowerCase().includes(q) || o.label.toLowerCase().includes(q)) {
        rows.push({ value: o.value, label: o.label, typed: false });
      }
    }
  }
  const showList = open && rows.length > 0;
  const activeIndex = Math.min(active, rows.length - 1);
  const current = showList ? rows[activeIndex] : undefined;
  const rowId = (i: number) => `${listId}-${i}`;

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (showList && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      const step = e.key === "ArrowDown" ? 1 : -1;
      setActive((activeIndex + step + rows.length) % rows.length);
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault();
      add(current && !current.typed ? current.value : text);
      return;
    }
    if (e.key === "Backspace" && text === "" && values.length > 0) {
      e.preventDefault();
      if (armed) remove(values[values.length - 1], false);
      else setArmed(true);
      return;
    }
    if (e.key === "Escape" && (showList || armed || error)) {
      // The dropdown, the selection or the error goes first, not the dialog around the field.
      e.stopPropagation();
      setOpen(false);
      setArmed(false);
      setError("");
      return;
    }
    if (armed) setArmed(false);
  };

  const remaining = options.filter((o) => !values.some((x) => same(x, o.value)));
  const last = values[values.length - 1];
  return (
    <div data-testid={testId} className="grid gap-2">
      <Popover open={showList} onOpenChange={(o) => !o && setOpen(false)}>
        <PopoverAnchor asChild>
          <div
            // A click on the field's empty space puts the caret in it, as a plain input would.
            onMouseDown={(e) => {
              if (e.target === e.currentTarget) {
                e.preventDefault();
                input.current?.focus();
              }
            }}
            aria-invalid={error ? true : undefined}
            className={cn(
              "flex min-h-11 w-full min-w-0 cursor-text flex-wrap items-center gap-1.5 rounded-md border border-line-strong bg-panel px-2 py-1.5",
              "transition-[border-color] duration-150 ease-out hover:border-muted",
              "has-[input:focus-visible]:outline-3 has-[input:focus-visible]:outline-offset-3 has-[input:focus-visible]:outline-ring",
              "aria-invalid:border-err",
            )}
          >
            {values.map((v) => {
              const selected = armed && v === last;
              return (
                <span
                  key={v}
                  data-selected={selected || undefined}
                  data-flash={flash === v || undefined}
                  className={cn(
                    "inline-flex h-7 max-w-full items-center gap-1 rounded-sm border border-transparent bg-raised pr-1 pl-2 text-sm text-fg",
                    "transition-[background-color,border-color,color] duration-200 ease-out",
                    "data-[flash]:border-accent data-[flash]:bg-panel",
                    "data-[selected]:border-err data-[selected]:bg-err-bg data-[selected]:text-err",
                  )}
                >
                  <span className={cn("truncate", mono && "font-mono text-xs")}>{v}</span>
                  <button
                    type="button"
                    aria-label={`Remove ${noun} ${v}`}
                    onClick={() => remove(v, true)}
                    // The hover is a small square; the invisible after: keeps the hit area larger.
                    className="relative grid size-4 shrink-0 place-items-center rounded-xs text-muted outline-offset-1 after:absolute after:-inset-1 after:content-[''] hover:bg-fg/10 hover:text-fg"
                  >
                    <X className="size-3" aria-hidden />
                  </button>
                </span>
              );
            })}
            <input
              ref={input}
              id={id}
              role="combobox"
              aria-expanded={showList}
              aria-controls={showList ? listId : undefined}
              aria-autocomplete="list"
              aria-activedescendant={current ? rowId(activeIndex) : undefined}
              value={text}
              placeholder={values.length === 0 ? placeholder : undefined}
              aria-describedby={[describedBy, error ? errorId : ""].filter(Boolean).join(" ") || undefined}
              aria-invalid={error ? true : undefined}
              autoComplete="off"
              spellCheck={false}
              className={cn(
                "h-7 min-w-24 flex-1 bg-transparent px-1 text-base text-fg outline-none placeholder:text-muted sm:text-sm",
                mono && "font-mono sm:text-xs",
              )}
              onChange={(e) => {
                const v = e.target.value;
                setError("");
                setArmed(false);
                setActive(0);
                if (v.trim() === "") setOpen(false);
                if (v.includes("\n") || (commaSeparates && v.includes(","))) add(v);
                else setText(v);
              }}
              onPaste={(e) => {
                const pasted = e.clipboardData.getData("text");
                if (pasted.includes("\n") || (commaSeparates && pasted.includes(","))) {
                  e.preventDefault();
                  add(text + pasted);
                }
              }}
              onKeyDown={onKeyDown}
              onBlur={() => {
                setArmed(false);
                setOpen(false);
                if (text.trim() !== "") add(text);
              }}
            />
          </div>
        </PopoverAnchor>
        <PopoverContent
          // The caret stays in the field: the list is read, and chosen from, there.
          onOpenAutoFocus={(e) => e.preventDefault()}
          onCloseAutoFocus={(e) => e.preventDefault()}
          onInteractOutside={(e) => {
            // A click in the field itself is not outside it.
            if (e.target instanceof Node && input.current?.parentElement?.contains(e.target)) e.preventDefault();
          }}
          // Clear of the field's focus outline (3px, offset 3px).
          sideOffset={8}
          className="w-(--radix-popover-trigger-width) p-1"
        >
          <ul id={listId} role="listbox" aria-label={`Add a ${noun}`} className="grid gap-0.5">
            {rows.map((row, i) => {
              const on = i === activeIndex;
              return (
                <li
                  key={`${row.typed ? "typed" : "option"}:${row.value}`}
                  id={rowId(i)}
                  role="option"
                  aria-selected={on}
                  aria-disabled={row.blocked ? true : undefined}
                  data-active={on || undefined}
                  // A mousedown would take the focus from the field, and its blur add the text.
                  onMouseDown={(e) => e.preventDefault()}
                  onMouseMove={() => setActive(i)}
                  onClick={() => {
                    if (!row.blocked) add(row.value);
                  }}
                  className={cn(
                    "flex min-h-9 cursor-default items-center gap-2 rounded-sm px-2 py-1.5 text-sm select-none",
                    "data-[active]:bg-raised",
                    row.blocked ? "text-muted" : "text-fg",
                  )}
                >
                  {row.typed ? (
                    <Plus className={cn("size-3.5 shrink-0", row.blocked ? "text-faint" : "text-accent")} aria-hidden />
                  ) : (
                    <span className="size-3.5 shrink-0" aria-hidden />
                  )}
                  <span className={cn("min-w-0 flex-1 truncate", mono && "font-mono text-xs")}>{row.label}</span>
                  {row.typed ? (
                    <span className={cn("shrink-0 text-xs", row.blocked ? "text-err" : "text-muted")}>
                      {row.blocked ?? "Add to list"}
                    </span>
                  ) : null}
                  {on && !row.blocked ? <CornerDownLeft className="size-3 shrink-0 text-muted" aria-hidden /> : null}
                </li>
              );
            })}
          </ul>
        </PopoverContent>
      </Popover>
      {error ? (
        <p id={errorId} className="text-xs text-err">
          {error}
        </p>
      ) : null}
      <span className="sr-only" aria-live="polite">
        {announce}
      </span>
      {offer && remaining.length > 0 ? (
        <div className="flex flex-wrap gap-1.5">
          {remaining.map((o) => (
            <button
              key={o.value}
              type="button"
              onClick={() => {
                onChange([...values, o.value]);
                setAnnounce(`${o.value} added`);
              }}
              className="inline-flex h-7 items-center gap-1 rounded-sm border border-dashed border-line-strong px-2 text-sm text-muted transition-[border-color,color] duration-150 ease-out hover:border-fg/50 hover:text-fg"
            >
              <Plus className="size-3.5" aria-hidden />
              {o.label}
            </button>
          ))}
        </div>
      ) : null}
    </div>
  );
}
