import { X } from "lucide-react";
import type { GetUsageResponse } from "../../gen/podium/agent/v1/agent_pb";
import {
  credentialLabel,
  isFiltered,
  isUnrecorded,
  originLabel,
  personLabel,
  routineNames,
  type Filters,
} from "../../lib/usage";
import { cn } from "../../lib/utils";
import { Button } from "../ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";

/** ALL is a Select's "every one", which a Select cannot hold as "". */
const ALL = "*";

/** SELECT is a filter Select the height, border, corners and type the other filter bars use. */
const SELECT = "h-9 min-h-0 rounded-lg border-border px-2.5 text-xs font-medium";

type Option = { value: string; label: string };

/**
 * UsageFilters narrows the whole screen (totals, trend, groupings and table) to one source,
 * person, model or credential. The options are the range's own groupings, so every one of
 * them has something behind it. A filter appears only once there is a choice to make, or
 * while it is set, so that it can always be cleared.
 */
export function UsageFilters({
  usage,
  filters,
  onChange,
}: {
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

  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="usage-filters">
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
      {isFiltered(filters) ? (
        <Button
          variant="ghost"
          size="sm"
          onClick={() => onChange({ origin: "", person: "", model: "", credential: "" })}
        >
          <X />
          Clear filters
        </Button>
      ) : null}
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
  if (options.length < 2 && value === "") return null;
  // A value the range no longer has (a link from another range) is still shown, by its raw
  // name, so the Select says what is filtering the screen rather than going blank.
  const shown = value !== "" && !options.some((o) => o.value === value) ? [...options, { value, label: value }] : options;
  return (
    <Select value={value || ALL} onValueChange={onChange}>
      <SelectTrigger
        size="sm"
        aria-label={label}
        data-testid={`usage-filter-${label.toLowerCase().replace(/\s+/g, "-")}`}
        className={cn(SELECT, wide ? "w-[15rem]" : "w-[10.5rem]", value !== "" && "border-accent text-fg")}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent>
        <SelectItem value={ALL}>{every}</SelectItem>
        {shown.map((o) => (
          <SelectItem key={o.value} value={o.value} className={mono ? "font-mono" : undefined}>
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}
