import { X } from "lucide-react";
import type { GetUsageResponse } from "../../gen/podium/agent/v1/agent_pb";
import {
  credentialLabel,
  isFiltered,
  isUnrecorded,
  NO_FILTERS,
  originLabel,
  personLabel,
  routineNames,
  type Filters,
} from "../../lib/usage";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import { Select, SelectContent, SelectItem, SelectSeparator, SelectTrigger, SelectValue } from "../ui/select";

/** ALL is a Select's "every one", which a Select cannot hold as "". */
const ALL = "*";

type Option = { value: string; label: string };

/**
 * UsageFilters narrows the whole screen (totals, trend, groupings and table) to one source,
 * person, model or credential.
 *
 * The choices are the range's groupings read without any filter, never the filtered result:
 * picking a source must not take the other filters' choices away with it, or the bar would
 * rebuild itself under the pointer on every pick. For the same reason every filter and the
 * clear button are always drawn, in the same place, whether or not the data has arrived and
 * whether or not anything is set. A set filter says what it is set to, beside its own name,
 * and carries its own clear button.
 */
export function UsageFilters({
  usage,
  filters,
  onChange,
}: {
  /** usage is the range read without filters: the source of every filter's choices. */
  usage: GetUsageResponse | undefined;
  filters: Filters;
  onChange: (f: Filters) => void;
}) {
  const routines = routineNames(usage?.routines ?? []);
  const sources: Option[] = (usage?.sources ?? []).map((g) => ({ value: g.key, label: originLabel(g.key) }));
  const people: Option[] = (usage?.people ?? []).map((p) => ({
    value: p.startedBy || "-",
    label: personLabel(p.startedBy, routines),
  }));
  const models: Option[] = [
    ...new Set((usage?.backends ?? []).filter((b) => !isUnrecorded(b)).map((b) => b.model)),
  ].map((m) => ({ value: m, label: m }));
  const credentials: Option[] = (usage?.credentials ?? []).map((g) => ({ value: g.key, label: credentialLabel(g) }));

  const set = (k: keyof Filters) => (v: string) => onChange({ ...filters, [k]: v === ALL ? "" : v });
  const filtered = isFiltered(filters);

  return (
    <div
      role="group"
      aria-label="Filters"
      data-testid="usage-filters"
      className="grid grid-cols-2 gap-2 sm:flex sm:flex-wrap sm:items-center"
    >
      <Filter label="Source" every="Every source" value={filters.origin} options={sources} onChange={set("origin")} />
      <Filter label="Person" every="Everyone" value={filters.person} options={people} onChange={set("person")} />
      <Filter label="Model" every="Every model" value={filters.model} options={models} onChange={set("model")} mono />
      <Filter
        label="Paid with"
        every="Any credential"
        value={filters.credential}
        options={credentials}
        onChange={set("credential")}
        wide
      />
      {/* Always drawn, so setting the first filter or clearing the last never moves anything. */}
      <Button
        variant="ghost"
        size="sm"
        className="col-span-2 justify-self-end sm:ml-auto"
        disabled={!filtered}
        onClick={() => onChange(NO_FILTERS)}
      >
        <X />
        Clear filters
      </Button>
    </div>
  );
}

function Filter({
  label,
  every,
  value,
  options,
  onChange,
  mono,
  wide,
}: {
  label: string;
  every: string;
  value: string;
  options: Option[];
  onChange: (v: string) => void;
  mono?: boolean;
  wide?: boolean;
}) {
  // A value the range does not have (a link from another range) is still shown, by its raw
  // name, so the filter says what is narrowing the screen rather than going blank.
  const shown = value !== "" && !options.some((o) => o.value === value) ? [...options, { value, label: value }] : options;
  const current = shown.find((o) => o.value === value)?.label ?? value;
  const set = value !== "";
  const slug = label.toLowerCase().replace(/\s+/g, "-");
  return (
    <div className={cn("relative min-w-0", wide ? "sm:w-[16rem]" : "sm:w-[12rem]")}>
      <Select value={value || ALL} onValueChange={onChange}>
        <SelectTrigger
          size="sm"
          aria-label={label}
          data-testid={`usage-filter-${slug}`}
          className={cn(
            "h-9 min-h-0 w-full rounded-lg border-border px-2.5 text-xs font-medium",
            set && "border-accent",
          )}
        >
          {/* The trigger names its filter, so a set one still says what it is filtering by. */}
          <span className="flex min-w-0 items-baseline gap-1.5">
            <span className="shrink-0 font-normal text-faint">{label}</span>
            <SelectValue>
              <span className={cn("truncate", set ? "text-fg" : "text-muted", set && mono && "font-mono")}>
                {set ? current : "All"}
              </span>
            </SelectValue>
          </span>
        </SelectTrigger>
        <SelectContent>
          <SelectItem value={ALL}>{every}</SelectItem>
          {shown.length > 0 ? <SelectSeparator /> : null}
          {shown.map((o) => (
            <SelectItem key={o.value} value={o.value} className={mono ? "font-mono" : undefined}>
              {o.label}
            </SelectItem>
          ))}
        </SelectContent>
      </Select>
      {set ? (
        // Laid over the trigger's chevron rather than inside it: a button cannot hold a button.
        <button
          type="button"
          aria-label={`Clear ${label.toLowerCase()} filter`}
          onClick={() => onChange(ALL)}
          className="absolute top-1/2 right-1 flex size-7 -translate-y-1/2 items-center justify-center rounded-md bg-panel text-muted outline-none transition-colors hover:bg-hover hover:text-fg focus-visible:ring-2 focus-visible:ring-ring/35"
        >
          <X aria-hidden className="size-3.5" />
        </button>
      ) : null}
    </div>
  );
}
