import { useId, useState } from "react";
import { Repeat as RepeatIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import {
  ALL_DAYS,
  checkCron,
  DEFAULT_REPEAT,
  describe,
  fromCron,
  HOUR_STEPS,
  hhmm,
  MINUTE_STEPS,
  parseMinute,
  parseTime,
  switchKind,
  toCron,
  WEEKDAYS,
  type Day,
  type Kind,
  type Repeat,
} from "../../lib/schedule";
import { Input } from "../ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { ToggleGroup, ToggleGroupItem } from "../ui/toggle-group";
import { TypePicker } from "./TypePicker";

const KINDS: { kind: Kind; label: string }[] = [
  { kind: "minutes", label: "Minutes" },
  { kind: "hourly", label: "Hourly" },
  { kind: "daily", label: "Daily" },
  { kind: "monthly", label: "Monthly" },
];

/** Sunday first, as a calendar's week row reads. */
const DAY_CHIPS: { day: Day; short: string; name: string }[] = [
  { day: 0, short: "S", name: "Sunday" },
  { day: 1, short: "M", name: "Monday" },
  { day: 2, short: "T", name: "Tuesday" },
  { day: 3, short: "W", name: "Wednesday" },
  { day: 4, short: "T", name: "Thursday" },
  { day: 5, short: "F", name: "Friday" },
  { day: 6, short: "S", name: "Saturday" },
];

/** SEGMENT is a toggle group as a segmented control: a recessed track, the chosen segment raised out of it. */
const SEGMENT = {
  track: "border-border bg-raised",
  item: "text-muted data-[state=on]:bg-panel data-[state=on]:font-semibold data-[state=on]:text-fg data-[state=on]:shadow-xs",
};

/** SLOTS are the times listed, every 15 minutes, as a calendar's time list offers them; any other is typed. */
const SLOTS: string[] = Array.from({ length: 96 }, (_, i) => hhmm(Math.floor(i / 4), (i % 4) * 15));

/**
 * ScheduleField is when a routine runs. Repeat builds the schedule the way a calendar's
 * recurrence does — how often, on which days, at what time — and says it in a sentence;
 * Cron takes an expression for anything Repeat cannot say. Its value is always the cron
 * expression the server stores.
 */
export function ScheduleField({ value, onChange }: { value: string; onChange: (cron: string) => void }) {
  const labelId = useId();
  const parsed = fromCron(value);
  const [mode, setMode] = useState<"repeat" | "cron">(parsed || !value.trim() ? "repeat" : "cron");
  // The last schedule Repeat built, kept so a trip through Cron and back does not lose it.
  const [repeat, setRepeat] = useState<Repeat>(parsed ?? DEFAULT_REPEAT);

  const update = (r: Repeat) => {
    setRepeat(r);
    onChange(toCron(r));
  };

  const cronProblem = mode === "cron" ? checkCron(value) : "";
  const cronReads = mode === "cron" ? fromCron(value) : null;

  return (
    <div className="grid gap-1.5" role="group" aria-labelledby={labelId}>
      <div className="flex items-center justify-between gap-3">
        <span id={labelId} className="text-sm font-medium">
          When
        </span>
        <ToggleGroup
          type="single"
          value={mode}
          aria-label="How to write the schedule"
          className={SEGMENT.track}
          onValueChange={(m) => {
            if (m === "cron") {
              setMode("cron");
            } else if (m === "repeat") {
              // What was written as cron comes back into Repeat when Repeat can say it.
              const r = fromCron(value) ?? repeat;
              setMode("repeat");
              update(r);
            }
          }}
        >
          <ToggleGroupItem value="repeat" data-testid="schedule-mode-repeat" className={SEGMENT.item}>
            Repeat
          </ToggleGroupItem>
          <ToggleGroupItem value="cron" data-testid="schedule-mode-cron" className={SEGMENT.item}>
            Cron
          </ToggleGroupItem>
        </ToggleGroup>
      </div>

      <div className="grid gap-3 rounded-lg border border-border bg-panel p-3">
        {mode === "repeat" ? (
          <>
            <ToggleGroup
              type="single"
              value={repeat.kind}
              aria-label="How often"
              onValueChange={(k) => k && update(switchKind(repeat, k as Kind))}
              className={cn("grid w-full grid-cols-4", SEGMENT.track)}
            >
              {KINDS.map((k) => (
                <ToggleGroupItem key={k.kind} value={k.kind} className={cn("h-8 text-sm", SEGMENT.item)}>
                  {k.label}
                </ToggleGroupItem>
              ))}
            </ToggleGroup>

            <Sentence repeat={repeat} onChange={update} />

            {repeat.kind !== "monthly" ? (
              <DayPicker days={repeat.days} onChange={(days) => update({ ...repeat, days })} />
            ) : null}
          </>
        ) : (
          <div className="grid gap-1.5">
            <Input
              data-testid="routine-cron"
              aria-label="Cron expression"
              value={value}
              onChange={(e) => onChange(e.target.value)}
              placeholder="0 9 * * 1-5"
              spellCheck={false}
              autoComplete="off"
              className="font-mono"
            />
            <p className="text-xs text-muted">
              Minute, hour, day of month, month, day of week — in the timezone below. At most every 15 minutes.
            </p>
          </div>
        )}

        <p
          data-testid="schedule-summary"
          aria-live="polite"
          className="flex items-center gap-2 border-t border-border pt-2.5 text-sm"
        >
          <RepeatIcon className="size-3.5 shrink-0 text-muted" aria-hidden />
          {mode === "repeat" ? (
            <span className="min-w-0 flex-1 font-medium text-fg">{describe(repeat)}</span>
          ) : cronProblem ? (
            <span className="min-w-0 flex-1 text-warn">{cronProblem}</span>
          ) : (
            <span className="min-w-0 flex-1 font-medium text-fg">
              {cronReads ? describe(cronReads) : "Custom schedule"}
            </span>
          )}
          {mode === "repeat" ? (
            <code className="shrink-0 font-mono text-xs text-faint">{toCron(repeat)}</code>
          ) : null}
        </p>
      </div>
    </div>
  );
}

/** Sentence is the frequency's own controls, read as a sentence: "Every [2 hours] at [:30]". */
function Sentence({ repeat: r, onChange }: { repeat: Repeat; onChange: (r: Repeat) => void }) {
  const words = "text-sm text-muted";
  switch (r.kind) {
    case "minutes":
      return (
        <div className="flex flex-wrap items-center gap-2">
          <span className={words}>Every</span>
          <Pick
            label="Minutes between runs"
            value={String(r.every)}
            options={MINUTE_STEPS.map((n) => ({ value: String(n), label: `${n} minutes` }))}
            onChange={(v) => onChange({ ...r, every: Number(v) })}
          />
        </div>
      );
    case "hourly":
      return (
        <div className="flex flex-wrap items-center gap-2">
          <span className={words}>Every</span>
          <Pick
            label="Hours between runs"
            value={String(r.every)}
            options={HOUR_STEPS.map((n) => ({ value: String(n), label: n === 1 ? "hour" : `${n} hours` }))}
            onChange={(v) => onChange({ ...r, every: Number(v) })}
          />
          <span className={words}>at minute</span>
          <TypePicker
            label="Minute past the hour"
            value={`:${String(r.minute).padStart(2, "0")}`}
            options={Array.from({ length: 12 }, (_, k) => `:${String(k * 5).padStart(2, "0")}`)}
            parse={(t) => {
              const m = parseMinute(t);
              return m === null ? null : `:${String(m).padStart(2, "0")}`;
            }}
            onChange={(v) => onChange({ ...r, minute: Number(v.slice(1)) })}
            placeholder="Any minute, e.g. 7"
            hint="Type a minute from 0 to 59."
            className="min-w-20"
          />
        </div>
      );
    case "daily":
      return (
        <div className="flex flex-wrap items-center gap-2">
          <span className={words}>At</span>
          <TimePick hour={r.hour} minute={r.minute} onChange={(hour, minute) => onChange({ ...r, hour, minute })} />
        </div>
      );
    case "monthly":
      return (
        <div className="flex flex-wrap items-center gap-2">
          <span className={words}>On day</span>
          <Pick
            label="Day of the month"
            value={String(r.day)}
            options={Array.from({ length: 31 }, (_, i) => ({ value: String(i + 1), label: String(i + 1) }))}
            onChange={(v) => onChange({ ...r, day: Number(v) })}
          />
          <span className={words}>at</span>
          <TimePick hour={r.hour} minute={r.minute} onChange={(hour, minute) => onChange({ ...r, hour, minute })} />
          {r.day > 28 ? (
            <span className="basis-full text-xs text-muted">Months without a {r.day}th are skipped.</span>
          ) : null}
        </div>
      );
  }
}

function Pick({
  label,
  value,
  options,
  onChange,
}: {
  label: string;
  value: string;
  options: { value: string; label: string }[];
  onChange: (v: string) => void;
}) {
  return (
    <Select value={value} onValueChange={onChange}>
      <SelectTrigger size="sm" aria-label={label} className="w-auto min-w-20 bg-panel">
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

function TimePick({
  hour,
  minute,
  onChange,
}: {
  hour: number;
  minute: number;
  onChange: (hour: number, minute: number) => void;
}) {
  return (
    <TypePicker
      label="Time"
      data-testid="schedule-time"
      value={hhmm(hour, minute)}
      options={SLOTS}
      parse={(t) => {
        const p = parseTime(t);
        return p ? hhmm(p.hour, p.minute) : null;
      }}
      onChange={(v) => {
        const [h, m] = v.split(":").map(Number);
        onChange(h ?? 0, m ?? 0);
      }}
      placeholder="Any time, e.g. 9:07 or 5pm"
      hint="Try 9:05, 1730 or 5:30pm."
      className="min-w-24"
    />
  );
}

/** DayPicker is the week as a row of days, each one on or off, with the two common choices beside it. */
function DayPicker({ days, onChange }: { days: Day[]; onChange: (days: Day[]) => void }) {
  const key = [...days].sort().join(",");
  const quick = (label: string, set: Day[]) => {
    const on = key === [...set].sort().join(",");
    return (
      <button
        type="button"
        aria-pressed={on}
        onClick={() => onChange(set)}
        className={cn(
          "rounded-sm px-1.5 py-0.5 text-xs outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          on ? "font-medium text-fg" : "text-muted hover:text-fg",
        )}
      >
        {label}
      </button>
    );
  };
  return (
    <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
      <span className="text-sm text-muted">On</span>
      <ToggleGroup
        type="multiple"
        aria-label="Days of the week"
        value={days.map(String)}
        // A schedule runs on at least one day: the last one cannot be switched off.
        onValueChange={(v) => v.length > 0 && onChange(v.map(Number).sort((a, b) => a - b) as Day[])}
        className="gap-1 border-0 bg-transparent p-0"
      >
        {DAY_CHIPS.map((d) => (
          <ToggleGroupItem
            key={d.day}
            value={String(d.day)}
            aria-label={d.name}
            title={d.name}
            className={cn(
              "size-8 min-w-8 rounded-full border border-line-strong bg-panel p-0 text-xs font-semibold text-muted",
              "hover:border-muted hover:text-fg",
              "data-[state=on]:border-accent data-[state=on]:bg-accent data-[state=on]:text-primary-foreground",
            )}
          >
            {d.short}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
      <span className="flex items-center gap-0.5">
        {quick("Every day", ALL_DAYS)}
        {quick("Weekdays", WEEKDAYS)}
      </span>
    </div>
  );
}
