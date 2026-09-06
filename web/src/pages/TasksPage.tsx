import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { Badge } from "../components/Badge";
import { Empty } from "../components/Empty";
import { PageHeader } from "../components/PageHeader";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import { buttonVariants } from "../components/ui/button";
import { Input } from "../components/ui/input";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
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

const PAGE_SIZE = 50;
const POLL_MS = 5000;
const SEARCH_DEBOUNCE_MS = 250;

export function TasksPage() {
  const [statuses, setStatuses] = useState<TaskStatus[]>([]);
  const [searchInput, setSearchInput] = useState("");
  const [search, setSearch] = useState("");
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
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
    queryKey: ["tasks", statuses, search, cursor],
    queryFn: () =>
      tasks.listTasks({
        filter: { status: statuses, search },
        page: { limit: PAGE_SIZE, cursor },
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

  const rows = query.data?.tasks ?? [];

  return (
    <div className="space-y-5">
      <PageHeader
        title="Tasks"
        description="Work that ran, is running, or is waiting for a node."
        actions={
          <Link to="/submit" className={buttonVariants({ size: "sm" })}>
            New task
          </Link>
        }
      />

      <div className="flex flex-wrap items-center gap-2">
        {TASK_STATUS_FILTERS.map((s) => (
          <button
            key={s}
            type="button"
            aria-pressed={statuses.includes(s)}
            onClick={() => toggle(s)}
            className={cn(
              "rounded-md border px-2.5 py-1 text-xs transition-colors",
              statuses.includes(s)
                ? "border-accent text-accent bg-accent/10"
                : "border-border text-muted hover:text-fg",
            )}
          >
            {taskStatusLabel(s)}
          </button>
        ))}
        <Input
          type="search"
          aria-label="Search tasks"
          placeholder="id prefix or image"
          value={searchInput}
          onChange={(e) => setSearchInput(e.target.value)}
          className="h-8 w-52 font-mono text-xs"
        />
      </div>

      {query.isPending ? (
        <TableSkeleton cols={8} />
      ) : rows.length === 0 ? (
        <Empty
          title={search === "" ? "No tasks" : `Nothing matches “${search}”`}
          hint={
            search === ""
              ? "Submit one with “New task”, or podium run --image alpine:3 -- echo hello"
              : "Search matches a task ID prefix or part of an image name."
          }
        />
      ) : (
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Task</TableHead>
              <TableHead>Status</TableHead>
              <TableHead>Image</TableHead>
              <TableHead>Requester</TableHead>
              <TableHead>Node</TableHead>
              <TableHead>Created</TableHead>
              <TableHead>Duration</TableHead>
              <TableHead>Exit</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((t) => {
              const queued = queuedExplanation(t);
              const outcome = t.status === TaskStatus.LOST ? "the node running it went away" : "";
              return (
                <TableRow key={t.id}>
                  <TableCell className="font-mono text-xs">
                    <Link to={`/tasks/${t.id}`} className="text-accent hover:underline">
                      {t.id}
                    </Link>
                  </TableCell>
                  <TableCell>
                    <Badge tone={taskStatusTone(t.status)}>{taskStatusLabel(t.status)}</Badge>
                    {/* Why a queued task is still queued is the only thing worth knowing
                        about it, so it goes in the list and not just the detail page. */}
                    {queued ? (
                      <div
                        data-testid="queued-reason"
                        className="mt-0.5 max-w-xs text-xs text-muted"
                      >
                        {queued}
                      </div>
                    ) : null}
                    {outcome ? (
                      <div className="mt-0.5 text-xs text-lost">{outcome}</div>
                    ) : null}
                  </TableCell>
                  <TableCell className="font-mono text-xs">{t.spec?.image ?? "—"}</TableCell>
                  <TableCell className="text-xs">{t.requestedBy || "—"}</TableCell>
                  <TableCell className="font-mono text-xs">{t.nodeId || "—"}</TableCell>
                  <TableCell className="text-xs whitespace-nowrap" title={absolute(t.createdAt)}>
                    {relative(t.createdAt)}
                  </TableCell>
                  <TableCell className="text-xs whitespace-nowrap">
                    {taskDuration(t.startedAt, t.finishedAt)}
                  </TableCell>
                  <TableCell className="font-mono text-xs" title={taskOutcome(t)}>
                    {t.exitCode ?? "—"}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      )}

      <div className="flex items-center gap-2 text-xs">
        <button
          type="button"
          disabled={cursors.length === 1}
          onClick={() => setCursors((prev) => prev.slice(0, -1))}
          className="rounded-md border border-border px-2 py-1 disabled:opacity-40"
        >
          Previous
        </button>
        <button
          type="button"
          disabled={!query.data?.nextCursor}
          onClick={() => setCursors((prev) => [...prev, query.data?.nextCursor ?? ""])}
          className="rounded-md border border-border px-2 py-1 disabled:opacity-40"
        >
          Next
        </button>
        <span className="text-muted">page {cursors.length}</span>
      </div>
    </div>
  );
}
