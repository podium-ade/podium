import { useEffect, useState } from "react";
import type { MouseEvent } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link, useNavigate } from "react-router";
import {
  ChevronDown,
  ChevronLeft,
  ChevronRight,
  Copy,
  ListFilter,
  Plus,
  Search,
  SearchX,
  X,
} from "lucide-react";
import { Badge, type Tone } from "../components/Badge";
import { Empty } from "../components/Empty";
import { PageHeader } from "../components/PageHeader";
import { Skeleton, TableSkeleton } from "../components/Skeleton";
import { TaskRowActions } from "../components/tasks/TaskRowActions";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button, buttonVariants } from "../components/ui/button";
import {
  DropdownMenu,
  DropdownMenuCheckboxItem,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuLabel,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "../components/ui/dropdown-menu";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
import { Tooltip } from "../components/ui/tooltip";
import { TaskStatus } from "../gen/podium/v1/common_pb";
import { errorMessage, tasks } from "../lib/client";
import {
  TASK_STATUS_FILTERS,
  absolute,
  queuedExplanation,
  relative,
  taskDuration,
  taskOutcome,
  taskStatusLabel,
  taskStatusTone,
} from "../lib/format";
import { cn } from "../lib/utils";

const PAGE_SIZES = [25, 50, 100];
const POLL_MS = 5000;
const SEARCH_DEBOUNCE_MS = 250;

/** The three questions an operator opens this screen with, in the order they ask them. */
const SUMMARY: TaskStatus[] = [TaskStatus.RUNNING, TaskStatus.QUEUED, TaskStatus.FAILED];

const COUNT_TONE: Record<Tone, string> = {
  ok: "text-ok",
  err: "text-err",
  warn: "text-warn",
  run: "text-run",
  idle: "text-fg",
  lost: "text-lost",
};

export function TasksPage() {
  const [statuses, setStatuses] = useState<TaskStatus[]>([]);
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState("");
  const [pageSize, setPageSize] = useState(50);
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
  const navigate = useNavigate();
  const toast = useToast();

  // Search is a server-side filter (TaskFilter.search: an ID prefix or an image substring).
  // Filtering the 50 rows already on screen would have been cheaper and a lie — it would find
  // nothing on page 1 for a task that exists on page 4.
  useEffect(() => {
    const t = setTimeout(() => {
      setSearch(searchInput.trim());
      setCursors([""]);
    }, SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [searchInput]);

  const query = useQuery({
    queryKey: ["tasks", statuses, search, pageSize, cursor],
    queryFn: () =>
      tasks.listTasks({
        filter: { status: statuses, search },
        page: { limit: pageSize, cursor },
      }),
    refetchInterval: POLL_MS,
    placeholderData: (prev) => prev,
  });

  useEffect(() => {
    if (query.error) toast(`ListTasks: ${errorMessage(query.error)}`);
  }, [query.error, toast]);

  const toggle = (s: TaskStatus) => {
    setCursors([""]);
    setStatuses((prev) => (prev.includes(s) ? prev.filter((x) => x !== s) : [...prev, s]));
  };

  const only = (s?: TaskStatus) => {
    setCursors([""]);
    setStatuses(s === undefined ? [] : [s]);
  };

  const clearFilters = () => {
    setCursors([""]);
    setStatuses([]);
    setSearchInput("");
  };

  const rows = query.data?.tasks ?? [];
  const filtering = statuses.length > 0 || search !== "";
  // With no list at all, the counters and the standing footnote are furniture around an empty
  // room: the empty state already says the one useful thing.
  const hasList = query.isPending || rows.length > 0 || filtering;
  const statusLabel =
    statuses.length === 0
      ? "Any status"
      : statuses.length === 1
        ? taskStatusLabel(statuses[0])
        : `${statuses.length} statuses`;

  const openRow = (e: MouseEvent<HTMLTableRowElement>, id: string) => {
    // The row is a shortcut, not the only way in: the image cell holds the real link, so
    // anything that is already a control keeps its own click.
    if ((e.target as HTMLElement).closest("a,button,[role=menuitem]")) return;
    void navigate(`/tasks/${id}`);
  };

  const copyId = (id: string) => {
    void navigator.clipboard?.writeText(id);
    toast("Task ID copied.", "ok");
  };

  return (
    <div className="space-y-5">
      <PageHeader
        title="Tasks"
        description="Work that ran, is running, or is waiting for a node."
        actions={
          <Link to="/submit" className={buttonVariants({ size: "sm" })}>
            <Plus />
            New task
          </Link>
        }
      />

      {hasList ? (
        <div className="grid max-w-2xl grid-cols-2 gap-2 sm:grid-cols-4">
          {query.isPending ? (
            Array.from({ length: 4 }, (_, i) => (
              <div key={i} className="rounded-xl border border-border bg-card px-3.5 py-2.5">
                <Skeleton className="h-3 w-16" />
                <Skeleton className="mt-2 h-5 w-8" />
              </div>
            ))
          ) : (
            <>
              <Counter
                label="On this page"
                count={rows.length}
                tone="idle"
                active={statuses.length === 0}
                onClick={() => only(undefined)}
              />
              {SUMMARY.map((s) => (
                <Counter
                  key={s}
                  label={taskStatusLabel(s)}
                  count={rows.filter((t) => t.status === s).length}
                  tone={taskStatusTone(s)}
                  active={statuses.length === 1 && statuses[0] === s}
                  onClick={() => only(statuses.length === 1 && statuses[0] === s ? undefined : s)}
                />
              ))}
            </>
          )}
        </div>
      ) : null}

      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <DropdownMenu>
          <DropdownMenuTrigger asChild>
            <Button variant="outline" size="sm" className="min-w-36 justify-between">
              <span className="flex items-center gap-1.5">
                <ListFilter />
                {statusLabel}
              </span>
              <ChevronDown className="text-muted" />
            </Button>
          </DropdownMenuTrigger>
          <DropdownMenuContent align="start" className="min-w-44">
            <DropdownMenuLabel>Status</DropdownMenuLabel>
            {TASK_STATUS_FILTERS.map((s) => (
              <DropdownMenuCheckboxItem
                key={s}
                checked={statuses.includes(s)}
                // Picking several statuses in one visit is the point, so the menu stays open.
                onSelect={(e) => e.preventDefault()}
                onCheckedChange={() => toggle(s)}
              >
                {taskStatusLabel(s)}
              </DropdownMenuCheckboxItem>
            ))}
            <DropdownMenuSeparator />
            <DropdownMenuItem disabled={statuses.length === 0} onSelect={() => only(undefined)}>
              Any status
            </DropdownMenuItem>
          </DropdownMenuContent>
        </DropdownMenu>

        <div className="flex items-center gap-2">
          <Label htmlFor="task-search" className="text-2xs tracking-wide text-faint uppercase">
            Search
          </Label>
          <div className="relative">
            <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-faint" />
            <Input
              id="task-search"
              type="search"
              placeholder="id prefix or image"
              value={searchInput}
              onChange={(e) => setSearchInput(e.target.value)}
              className="h-8 w-60 pl-8 font-mono text-xs"
            />
          </div>
        </div>

        {filtering ? (
          <div className="ml-auto flex items-center gap-2">
            <span className="text-xs text-muted">
              <span className="tabular">{rows.length}</span> matching
            </span>
            <Button variant="ghost" size="sm" onClick={clearFilters}>
              <X />
              Clear filters
            </Button>
          </div>
        ) : null}
      </div>

      {query.isPending ? (
        <TableSkeleton cols={8} />
      ) : query.isError && rows.length === 0 ? (
        <Alert variant="destructive" title="Could not list tasks">
          {errorMessage(query.error)} — check that podium-server is reachable and that your token
          is still accepted. The list keeps retrying every {POLL_MS / 1000} seconds.
        </Alert>
      ) : rows.length === 0 ? (
        filtering ? (
          <Empty
            icon={SearchX}
            title="Nothing matches these filters"
            hint={
              search === "" ? (
                <>No task on this page has one of the statuses you picked.</>
              ) : (
                <>
                  Search matches a task ID <em>prefix</em> or part of an image name; nothing here
                  matches <span className="font-mono text-fg">{search}</span>.
                </>
              )
            }
            action={
              <Button variant="outline" size="sm" onClick={clearFilters}>
                <X />
                Clear filters
              </Button>
            }
          />
        ) : (
          <Empty
            title="No tasks yet"
            hint={
              <>
                A task appears here the moment the server accepts it, queued or not. Submit one
                with “New task”, or run{" "}
                <span className="font-mono text-fg">podium run --image alpine:3 -- echo hello</span>
                .
              </>
            }
            action={
              <Link to="/submit" className={buttonVariants({ size: "sm" })}>
                <Plus />
                New task
              </Link>
            }
          />
        )
      ) : (
        <Table className="[&_td]:px-2.5 [&_th]:px-2.5">
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className="w-60">Status</TableHead>
              <TableHead>Image</TableHead>
              <TableHead>Task ID</TableHead>
              <TableHead>Requester</TableHead>
              <TableHead>Node</TableHead>
              <TableHead>Created</TableHead>
              <TableHead className="text-right">Duration</TableHead>
              <TableHead className="text-right">Exit</TableHead>
              <TableHead pinned className="w-0">
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((t) => {
              const queued = queuedExplanation(t);
              const outcome = t.status === TaskStatus.LOST ? "the node running it went away" : "";
              const image = t.spec?.image ?? "";
              // Every row is the same height whether or not it carries a second line, so the
              // 5s poll cannot make the table jump under the pointer.
              return (
                <TableRow key={t.id} className="h-[4.5rem] cursor-pointer" onClick={(e) => openRow(e, t.id)}>
                  <TableCell>
                    <Badge tone={taskStatusTone(t.status)}>{taskStatusLabel(t.status)}</Badge>
                    {/* Why a queued task is still queued is the only thing worth knowing
                        about it, so it goes in the list and not just the detail page. */}
                    {/* Clamped rather than truncated: one line cut this short reads
                        "no online node carries the l…", which hides the label that is the
                        entire answer. Two lines fit the sentence and still keep rows even. */}
                    {queued ? (
                      <div
                        data-testid="queued-reason"
                        title={queued}
                        className="mt-1 line-clamp-2 max-w-[13.5rem] text-2xs leading-snug text-muted"
                      >
                        {queued}
                      </div>
                    ) : null}
                    {outcome ? (
                      <div
                        title={outcome}
                        className="mt-1 line-clamp-2 max-w-[13.5rem] text-2xs leading-snug text-lost"
                      >
                        {outcome}
                      </div>
                    ) : null}
                  </TableCell>
                  <TableCell className="max-w-80">
                    <Link
                      to={`/tasks/${t.id}`}
                      title={image}
                      className="block truncate rounded-sm font-mono text-xs text-fg outline-none hover:text-accent focus-visible:ring-2 focus-visible:ring-ring/50"
                    >
                      {image || "—"}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <span className="flex items-center gap-1">
                      <span title={t.id} className="max-w-36 truncate font-mono text-2xs text-faint">
                        {t.id}
                      </span>
                      <button
                        type="button"
                        aria-label={`Copy ID of task ${t.id}`}
                        onClick={() => copyId(t.id)}
                        className="rounded-sm p-0.5 text-faint opacity-0 outline-none transition-opacity hover:text-fg group-hover/row:opacity-100 focus-visible:opacity-100 focus-visible:ring-2 focus-visible:ring-ring/50"
                      >
                        <Copy className="size-3" />
                      </button>
                    </span>
                  </TableCell>
                  <TableCell
                    className="max-w-32 truncate text-xs text-muted"
                    title={t.requestedBy || undefined}
                  >
                    {t.requestedBy || "—"}
                  </TableCell>
                  <TableCell className="font-mono text-2xs text-muted">{t.nodeId || "—"}</TableCell>
                  <TableCell
                    className="tabular text-xs whitespace-nowrap text-muted"
                    title={absolute(t.createdAt)}
                  >
                    {relative(t.createdAt)}
                  </TableCell>
                  <TableCell className="tabular text-right text-xs whitespace-nowrap text-muted">
                    {taskDuration(t.startedAt, t.finishedAt)}
                  </TableCell>
                  <TableCell className="text-right">
                    <Tooltip label={taskOutcome(t)}>
                      <span
                        className={cn(
                          "tabular font-mono text-xs",
                          t.exitCode ? "text-fg" : "text-muted",
                        )}
                      >
                        {t.exitCode ?? "—"}
                      </span>
                    </Tooltip>
                  </TableCell>
                  <TableCell pinned className="w-0 pr-2 text-right">
                    <TaskRowActions task={t} />
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}

      {rows.length > 0 ? (
        <div className="flex flex-wrap items-center justify-between gap-x-4 gap-y-3">
          <p className="text-xs text-muted">
            <span className="tabular text-fg">{rows.length}</span>{" "}
            {rows.length === 1 ? "task" : "tasks"} on page{" "}
            <span className="tabular text-fg">{cursors.length}</span>
          </p>
          <div className="flex items-center gap-2">
            <Label htmlFor="page-size" className="text-2xs tracking-wide text-faint uppercase">
              Rows
            </Label>
            <Select
              value={String(pageSize)}
              onValueChange={(v) => {
                setCursors([""]);
                setPageSize(Number(v));
              }}
            >
              <SelectTrigger id="page-size" size="sm" className="w-20">
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {PAGE_SIZES.map((n) => (
                  <SelectItem key={n} value={String(n)}>
                    {n}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
            <Button
              variant="outline"
              size="sm"
              disabled={cursors.length === 1}
              onClick={() => setCursors((prev) => prev.slice(0, -1))}
            >
              <ChevronLeft />
              Previous
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={!query.data?.nextCursor}
              onClick={() => setCursors((prev) => [...prev, query.data?.nextCursor ?? ""])}
            >
              Next
              <ChevronRight />
            </Button>
          </div>
        </div>
      ) : null}

      {hasList ? (
        <p className="max-w-3xl text-xs leading-relaxed text-muted">
          The list refreshes every {POLL_MS / 1000} seconds, and the counters above describe the page
          you are looking at rather than the whole history. Paging walks forward from a cursor, so
          a task that starts while you are on page 2 turns up at the top of page 1.
        </p>
      ) : null}
    </div>
  );
}

function Counter({
  label,
  count,
  tone,
  active,
  onClick,
}: {
  label: string;
  count: number;
  tone: Tone;
  active: boolean;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onClick}
      className={cn(
        "rounded-xl border px-3.5 py-2.5 text-left transition-colors duration-150",
        "outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
        active ? "border-accent/45 bg-accent/8" : "border-border bg-card hover:bg-raised/50",
      )}
    >
      <span className="block text-2xs tracking-wide text-faint uppercase">{label}</span>
      <span className={cn("tabular mt-1 block text-lg leading-none font-semibold", COUNT_TONE[tone])}>
        {count}
      </span>
    </button>
  );
}
