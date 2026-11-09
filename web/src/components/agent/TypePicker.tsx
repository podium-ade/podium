import { useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Check, ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";
import { Popover, PopoverContent, PopoverTrigger } from "../ui/popover";

/**
 * TypePicker is a value chosen from a short list or typed outright — a time, a minute past
 * the hour. The list is the common choices; what is typed is read by `parse`, so 09:07 is as
 * reachable as 09:00 without a list of 1,440 rows. It looks and behaves as the timezone
 * picker does: a trigger like the Select's, and a popover with a box to type in.
 */
export function TypePicker({
  value,
  options,
  parse,
  onChange,
  label,
  placeholder,
  hint,
  className,
  "data-testid": testId,
}: {
  /** value is the chosen option, as its label reads ("09:00", ":30"). */
  value: string;
  options: string[];
  /** parse reads what was typed as an option, or null when it is not one. */
  parse: (text: string) => string | null;
  onChange: (value: string) => void;
  label: string;
  placeholder: string;
  /** hint is what to type, said when what was typed reads as nothing. */
  hint: string;
  className?: string;
  "data-testid"?: string;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(-1);
  const listId = useId();
  const listRef = useRef<HTMLUListElement>(null);

  const typed = query.trim() ? parse(query) : null;
  const rows = useMemo(() => {
    const all = options.includes(value) ? options : [...options, value].sort();
    const q = query.trim();
    if (!q) return all;
    // What was typed, read as a value, first; then the common ones it is the start of.
    const near = all.filter((o) => o.replace(/^0/, "").startsWith(q.replace(/^0/, "")) || o.startsWith(q));
    return typed && !near.includes(typed) ? [typed, ...near] : near.length ? near : typed ? [typed] : [];
  }, [options, value, query, typed]);

  const choose = (v: string) => {
    onChange(v);
    setOpen(false);
    setQuery("");
  };

  const reveal = (i: number, center = false) => {
    const list = listRef.current;
    const row = list?.children[i] as HTMLElement | undefined;
    if (!list || !row) return;
    if (center) list.scrollTop = row.offsetTop - list.clientHeight / 2 + row.offsetHeight / 2;
    else if (row.offsetTop < list.scrollTop) list.scrollTop = row.offsetTop;
    else if (row.offsetTop + row.offsetHeight > list.scrollTop + list.clientHeight)
      list.scrollTop = row.offsetTop + row.offsetHeight - list.clientHeight;
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (rows.length === 0) return;
      const from = active < 0 ? Math.max(rows.indexOf(value), e.key === "ArrowDown" ? -1 : 0) : active;
      const next = (from + (e.key === "ArrowDown" ? 1 : -1) + rows.length) % rows.length;
      setActive(next);
      reveal(next);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const pick = active >= 0 ? rows[active] : (typed ?? rows[0]);
      if (pick) choose(pick);
    }
  };

  return (
    // modal: see TimezonePicker — inside a dialog, only a modal popover's list takes the wheel.
    <Popover
      modal
      open={open}
      onOpenChange={(o) => {
        setOpen(o);
        setActive(-1);
        if (!o) setQuery("");
      }}
    >
      <PopoverTrigger asChild>
        <button
          type="button"
          role="combobox"
          aria-label={label}
          aria-expanded={open}
          aria-controls={listId}
          data-testid={testId}
          className={cn(
            "flex min-h-9 items-center justify-between gap-2 rounded-md border border-line-strong bg-panel px-3 text-sm text-fg tabular-nums",
            "outline-none transition-[border-color] duration-150 ease-out hover:border-muted",
            "focus-visible:border-accent focus-visible:ring-2 focus-visible:ring-ring/35",
            className,
          )}
        >
          <span>{value}</span>
          <ChevronDown className="size-4 shrink-0 text-muted" aria-hidden />
        </button>
      </PopoverTrigger>
      <PopoverContent
        className="flex w-52 flex-col overflow-hidden p-0"
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          (e.currentTarget as HTMLElement).querySelector("input")?.focus();
          requestAnimationFrame(() => reveal(rows.indexOf(value), true));
        }}
      >
        <input
          value={query}
          role="searchbox"
          aria-label={`${label}: type one`}
          placeholder={placeholder}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(-1);
            if (listRef.current) listRef.current.scrollTop = 0;
          }}
          onKeyDown={onKey}
          className="w-full shrink-0 border-b border-hairline bg-transparent px-3 py-2.5 text-sm text-fg outline-none placeholder:text-faint"
        />
        {rows.length === 0 ? (
          <div role="status" className="grid gap-0.5 px-3 py-3 text-sm">
            <span className="text-fg">“{query.trim()}” isn’t one</span>
            <span className="text-xs text-muted">{hint}</span>
          </div>
        ) : (
          <ul
            ref={listRef}
            id={listId}
            role="listbox"
            aria-label={label}
            className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain p-1"
            onMouseMove={() => active !== -1 && setActive(-1)}
          >
            {rows.map((o, i) => (
              <li
                key={o}
                role="option"
                aria-selected={o === value}
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => choose(o)}
                className={cn(
                  "relative flex cursor-default items-center gap-2 rounded-sm py-1.5 pr-2 pl-7 text-sm text-fg tabular-nums select-none hover:bg-raised",
                  (i === active || (active < 0 && o === typed)) && "bg-raised",
                )}
              >
                {o === value ? <Check className="absolute left-2 size-3.5 text-accent" aria-hidden /> : null}
                <span className="flex-1">{o}</span>
                {o === typed && query.trim() ? <span className="text-xs text-faint">Enter</span> : null}
              </li>
            ))}
          </ul>
        )}
      </PopoverContent>
    </Popover>
  );
}
