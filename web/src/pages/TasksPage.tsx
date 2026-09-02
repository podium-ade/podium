import { useEffect, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { Badge } from "../components/Badge";
import { Empty } from "../components/Empty";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import type { TaskStatus } from "../gen/podium/v1/common_pb";
import { errorMessage, tasks } from "../lib/client";
import {
  TASK_STATUS_FILTERS,
  absolute,
  taskDuration,
  taskStatusLabel,
  taskStatusTone,
} from "../lib/format";

const PAGE_SIZE = 50;
const POLL_MS = 5000;

export function TasksPage() {
  const [statuses, setStatuses] = useState<TaskStatus[]>([]);
  const [cursors, setCursors] = useState<string[]>([""]);
  const cursor = cursors[cursors.length - 1];
  const toast = useToast();

  const query = useQuery({
    queryKey: ["tasks", statuses, cursor],
    queryFn: () =>
      tasks.listTasks({
        filter: { status: statuses },
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
    <div className="space-y-4">
      <div className="flex flex-wrap items-center gap-2">
        <h1 className="mr-2 text-base font-semibold">Tasks</h1>
        {TASK_STATUS_FILTERS.map((s) => (
          <button
            key={s}
            type="button"
            aria-pressed={statuses.includes(s)}
            onClick={() => toggle(s)}
            className={`rounded border px-2 py-0.5 text-xs ${
              statuses.includes(s)
                ? "border-accent text-accent"
                : "border-border text-muted hover:text-fg"
            }`}
          >
            {taskStatusLabel(s)}
          </button>
        ))}
      </div>

      {query.isPending ? (
        <TableSkeleton cols={8} />
      ) : rows.length === 0 ? (
        <Empty
          title="No tasks"
          hint="podium run --image alpine:3 -- echo hello"
        />
      ) : (
        <div className="overflow-x-auto rounded border border-border">
          <table className="w-full text-left text-sm">
            <thead className="bg-panel text-xs text-muted">
              <tr>
                <th className="px-3 py-2 font-medium">Task</th>
                <th className="px-3 py-2 font-medium">Status</th>
                <th className="px-3 py-2 font-medium">Image</th>
                <th className="px-3 py-2 font-medium">Requester</th>
                <th className="px-3 py-2 font-medium">Node</th>
                <th className="px-3 py-2 font-medium">Created</th>
                <th className="px-3 py-2 font-medium">Duration</th>
                <th className="px-3 py-2 font-medium">Exit</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((t) => (
                <tr key={t.id} className="border-t border-border hover:bg-panel">
                  <td className="px-3 py-1.5 font-mono text-xs">
                    <Link to={`/tasks/${t.id}`} className="text-accent hover:underline">
                      {t.id}
                    </Link>
                  </td>
                  <td className="px-3 py-1.5">
                    <Badge tone={taskStatusTone(t.status)}>{taskStatusLabel(t.status)}</Badge>
                  </td>
                  <td className="px-3 py-1.5 font-mono text-xs">{t.spec?.image ?? "—"}</td>
                  <td className="px-3 py-1.5 text-xs">{t.requestedBy || "—"}</td>
                  <td className="px-3 py-1.5 font-mono text-xs">{t.nodeId || "—"}</td>
                  <td className="px-3 py-1.5 text-xs whitespace-nowrap">{absolute(t.createdAt)}</td>
                  <td className="px-3 py-1.5 text-xs whitespace-nowrap">
                    {taskDuration(t.startedAt, t.finishedAt)}
                  </td>
                  <td className="px-3 py-1.5 font-mono text-xs">{t.exitCode ?? "—"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      <div className="flex items-center gap-2 text-xs">
        <button
          type="button"
          disabled={cursors.length === 1}
          onClick={() => setCursors((prev) => prev.slice(0, -1))}
          className="rounded border border-border px-2 py-1 disabled:opacity-40"
        >
          Previous
        </button>
        <button
          type="button"
          disabled={!query.data?.nextCursor}
          onClick={() => setCursors((prev) => [...prev, query.data?.nextCursor ?? ""])}
          className="rounded border border-border px-2 py-1 disabled:opacity-40"
        >
          Next
        </button>
        <span className="text-muted">page {cursors.length}</span>
      </div>
    </div>
  );
}
