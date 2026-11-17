import { useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { Check, ChevronDown } from "lucide-react";
import { cn } from "@/lib/utils";
import { browserZone } from "../../lib/routines";
import { commonZones, formatOffset, zoneLabel, type Zone } from "../../lib/timezones";
import { Popover, PopoverContent, PopoverTrigger } from "../ui/popover";

/**
 * TimezonePicker chooses a timezone from a searchable list sorted by UTC offset: one row per
 * timezone, named for the place most people know it by (lib/timezones.ts). It is a field of a
 * form, styled as the project's Select trigger.
 */
export function TimezonePicker({
  value,
  onChange,
  id,
  "data-testid": testId,
}: {
  value: string;
  onChange: (zone: string) => void;
  id?: string;
  "data-testid"?: string;
}) {
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // active is the row the arrow keys are on; -1 is none. The mouse highlights with CSS alone:
  // a state change per hovered row re-rendered every row and lagged behind the pointer.
  const [active, setActive] = useState(-1);
  const listId = useId();
  const listRef = useRef<HTMLUListElement>(null);

  // The list is worked out once per opening, so the person's own zone and the one chosen stand
  // in for their timezone's row.
  const zones = useMemo(() => commonZones([browserZone(), value]), [value]);
  const chosen = zones.find((z) => z.id === value);
  const rows = useMemo(() => {
    const q = query.trim().toLowerCase();
    if (!q) return zones;
    return zones.filter((z) =>
      [z.name, z.places, z.id.replaceAll("_", " "), formatOffset(z.minutes)].join(" ").toLowerCase().includes(q),
    );
  }, [query, zones]);

  /** reveal scrolls the list, and only the list, so row i is in view. */
  const reveal = (i: number, center = false) => {
    const list = listRef.current;
    const row = list?.children[i] as HTMLElement | undefined;
    if (!list || !row) return;
    if (center) {
      list.scrollTop = row.offsetTop - list.clientHeight / 2 + row.offsetHeight / 2;
    } else if (row.offsetTop < list.scrollTop) {
      list.scrollTop = row.offsetTop;
    } else if (row.offsetTop + row.offsetHeight > list.scrollTop + list.clientHeight) {
      list.scrollTop = row.offsetTop + row.offsetHeight - list.clientHeight;
    }
  };

  const choose = (zone: string) => {
    onChange(zone);
    setOpen(false);
    setQuery("");
  };

  const move = (i: number) => {
    setActive(i);
    reveal(i);
  };

  const onKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (rows.length === 0) return;
      const at = rows.findIndex((z) => z.id === value);
      const from = active < 0 ? Math.max(at, e.key === "ArrowDown" ? -1 : 0) : active;
      move((from + (e.key === "ArrowDown" ? 1 : -1) + rows.length) % rows.length);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const zone = rows[active < 0 ? 0 : active];
      if (zone) choose(zone.id);
    }
  };

  return (
    // modal: inside a dialog, the dialog's scroll lock swallows the wheel over anything that is
    // not its own; a modal popover takes the lock over, so its list scrolls.
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
          id={id}
          type="button"
          role="combobox"
          aria-expanded={open}
          aria-controls={listId}
          data-testid={testId}
          className={cn(
            "flex min-h-11 w-full items-center justify-between gap-2 rounded-md border border-line-strong bg-panel px-3 text-base text-fg",
            "outline-none transition-[border-color] duration-150 ease-out hover:border-muted",
            "focus-visible:border-accent focus-visible:ring-2 focus-visible:ring-ring/35",
          )}
        >
          <span className="min-w-0 truncate text-left">{value ? zoneLabel(chosen, value) : "Choose a timezone"}</span>
          <span className="flex shrink-0 items-center gap-2">
            {chosen && chosen.id !== "UTC" ? (
              <span className="text-xs text-faint tabular-nums">{formatOffset(chosen.minutes)}</span>
            ) : null}
            <ChevronDown className="size-4 text-muted" aria-hidden />
          </span>
        </button>
      </PopoverTrigger>
      <PopoverContent
        align="end"
        className="flex w-[max(var(--radix-popover-trigger-width),min(28rem,calc(100vw-24px)))] flex-col overflow-hidden p-0"
        onOpenAutoFocus={(e) => {
          // The search box takes focus, not the first row.
          e.preventDefault();
          (e.currentTarget as HTMLElement).querySelector("input")?.focus();
          // Open on the zone already chosen.
          requestAnimationFrame(() => reveal(zones.findIndex((z) => z.id === value), true));
        }}
      >
        <input
          value={query}
          role="searchbox"
          aria-label="Search timezones"
          placeholder="Search a city, country or UTC offset…"
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
        <ul
          ref={listRef}
          id={listId}
          role="listbox"
          aria-label="Timezones"
          className="relative min-h-0 flex-1 overflow-y-auto overscroll-contain p-1"
          // Once the pointer moves, it leads: the keyboard's row lets go.
          onMouseMove={() => active !== -1 && setActive(-1)}
        >
          {rows.length === 0 ? <li className="px-2 py-2 text-sm text-muted">No timezone matches.</li> : null}
          {rows.map((zone, i) => (
            <ZoneRow
              key={zone.id}
              zone={zone}
              selected={zone.id === value}
              active={i === active}
              onPick={() => choose(zone.id)}
            />
          ))}
        </ul>
      </PopoverContent>
    </Popover>
  );
}

function ZoneRow({
  zone,
  selected,
  active,
  onPick,
}: {
  zone: Zone;
  selected: boolean;
  active: boolean;
  onPick: () => void;
}) {
  return (
    <li
      role="option"
      aria-selected={selected}
      title={zone.id}
      onMouseDown={(e) => e.preventDefault()}
      onClick={onPick}
      className={cn(
        "relative flex cursor-default items-center gap-2 rounded-sm py-1.5 pr-2 pl-7 text-sm text-fg select-none hover:bg-raised",
        active && "bg-raised",
      )}
    >
      {selected ? <Check className="absolute left-2 size-3.5 text-accent" aria-hidden /> : null}
      <span className="min-w-0 flex-1 truncate">
        {zone.name}
        {zone.places !== zone.name && zone.id !== "UTC" ? <span className="text-faint"> · {zone.places}</span> : null}
      </span>
      <span className="w-16 shrink-0 text-right text-xs text-faint tabular-nums">{formatOffset(zone.minutes)}</span>
    </li>
  );
}
