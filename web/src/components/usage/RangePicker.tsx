import { useState } from "react";
import { CalendarRange, Check } from "lucide-react";
import {
  PRESETS,
  customRange,
  dayKey,
  lastDay,
  parseDayKey,
  rangeFor,
  type Range,
} from "../../lib/usage";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "../ui/popover";

/**
 * RangePicker is the screen's window in time: a row of trailing-day and calendar
 * -month presets, plus a custom pair of dates for anything they do not cover.
 *
 * The presets are buttons rather than a select because there are seven of them and they are
 * the thing an operator changes most; a select would hide the current answer behind a click.
 *
 * It sits in the top bar beside the theme switch and the bell, so both of its controls take
 * their 44px outer height, 3px radius and border: the segment row is a 1px border, 3px of
 * padding and 36px segments.
 */
export function RangePicker({
  range,
  onChange,
  max,
}: {
  range: Range;
  onChange: (r: Range) => void;
  /** The latest day that can be picked: today unless given. There is no spend in the future. */
  max?: Date;
}) {
  return (
    <div className="flex flex-wrap items-center gap-1.5">
      <div className="inline-flex min-h-11 flex-wrap items-center gap-0.5 rounded-md border border-border bg-panel p-[3px]">
        {PRESETS.map((p) => (
          <button
            key={p.id}
            type="button"
            aria-pressed={range.id === p.id}
            onClick={() => onChange(rangeFor(p.id))}
            className={cn(
              "h-9 rounded-sm px-2.5 text-xs font-medium whitespace-nowrap transition-colors",
              "outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
              range.id === p.id ? "bg-raised text-fg shadow-xs" : "text-muted hover:text-fg",
            )}
          >
            {p.short}
          </button>
        ))}
      </div>
      <CustomRange range={range} onChange={onChange} max={max} />
    </div>
  );
}

function CustomRange({
  range,
  onChange,
  max,
}: {
  range: Range;
  onChange: (r: Range) => void;
  max?: Date;
}) {
  const [open, setOpen] = useState(false);
  // Today is read when the popover opens rather than on every render, so a screen left open
  // past midnight still offers the new day.
  const [today, setToday] = useState(() => new Date());
  const latest = dayKey(max ?? today);
  // Seeded from the active range so opening the popover on a preset offers that preset's
  // dates to adjust rather than two empty fields.
  const [from, setFrom] = useState(() => dayKey(range.from));
  const [to, setTo] = useState(() => dayKey(lastDay(range)));

  const custom = range.id === "custom";
  const valid = from !== "" && to !== "";

  return (
    <Popover
      open={open}
      onOpenChange={(o) => {
        // Re-seed on every open: the active range may have changed via a preset since.
        if (o) {
          setToday(new Date());
          setFrom(dayKey(range.from));
          setTo(dayKey(lastDay(range)));
        }
        setOpen(o);
      }}
    >
      <PopoverTrigger asChild>
        <Button
          variant="outline"
          size="sm"
          aria-pressed={custom}
          className={cn("min-h-11 border-border", custom && "bg-raised")}
        >
          <CalendarRange />
          {custom ? range.label : "Custom"}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-auto p-3">
        <form
          className="space-y-3"
          onSubmit={(e) => {
            e.preventDefault();
            if (!valid) return;
            onChange(customRange(parseDayKey(from), parseDayKey(to)));
            setOpen(false);
          }}
        >
          <div className="flex items-end gap-2">
            <Field label="From" value={from} max={latest} onChange={setFrom} />
            <span aria-hidden className="pb-2 text-xs text-faint">
              –
            </span>
            <Field label="To" value={to} max={latest} onChange={setTo} />
          </div>
          <div className="flex items-center justify-between gap-3">
            {/* Both ends are inclusive whole days, which is not the obvious reading of two
                date fields and is worth one line rather than a surprise in the totals. */}
            <p className="text-2xs text-faint">Both days included</p>
            <Button type="submit" size="sm" disabled={!valid}>
              <Check />
              Apply
            </Button>
          </div>
        </form>
      </PopoverContent>
    </Popover>
  );
}

function Field({
  label,
  value,
  max,
  onChange,
}: {
  label: string;
  value: string;
  max: string;
  onChange: (v: string) => void;
}) {
  return (
    <label className="flex flex-col gap-1">
      <span className="text-2xs font-medium tracking-wide text-faint uppercase">{label}</span>
      <input
        type="date"
        value={value}
        max={max}
        onChange={(e) => onChange(e.target.value)}
        aria-label={label}
        className={cn(
          "h-8 rounded-md border border-border bg-panel px-2 text-xs text-fg tabular",
          "outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
        )}
      />
    </label>
  );
}
