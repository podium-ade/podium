import { useQuery } from "@tanstack/react-query";
import type { ChatMessage } from "../gen/podium/agent/v1/agent_pb";
import { TaskStatus } from "../gen/podium/v1/common_pb";
import type { Task } from "../gen/podium/v1/task_pb";
import { tasks } from "./client";

/** DESKTOP_PORT is the exposed port a desktop playbook's turn names voxl's port by. */
export const DESKTOP_PORT = "desktop";

/** How often a candidate task is re-read while the chat shows it. */
const TASK_POLL_MS = 5_000;

/**
 * desktopCandidate is the task whose desktop the chat may show: the running turn's, when the
 * stream said so, else the newest message's. A desktop turn keeps running after it answers —
 * it waits for the next message with its desktop up — and every row its task posts carries its
 * id, so the newest one still names it after the turn's status has gone back to finished, and
 * after a reload.
 */
export function desktopCandidate(
  messages: ChatMessage[],
  running?: string,
): string | undefined {
  if (running) return running;
  for (let i = messages.length - 1; i >= 0; i--) {
    const id = messages[i]?.taskId;
    if (id) return id;
  }
  return undefined;
}

/** hasDesktop reports whether a task is a desktop playbook's and is running now. */
export function hasDesktop(task: Task | undefined): boolean {
  if (!task) return false;
  return (
    task.status === TaskStatus.RUNNING &&
    task.spec?.expose?.ports[DESKTOP_PORT] !== undefined
  );
}

/**
 * useDesktopTask reads the candidate task and says whether it has a desktop to show. It shares
 * the task page's cache key, so opening one after the other costs nothing.
 */
export function useDesktopTask(taskId: string | undefined) {
  const q = useQuery({
    queryKey: ["task", taskId],
    queryFn: () => tasks.getTask({ taskId: taskId ?? "" }),
    enabled: !!taskId,
    // A task that has finished stays finished: stop asking once it has.
    refetchInterval: (query) => {
      const t = query.state.data?.task;
      return t && t.status > TaskStatus.RUNNING ? false : TASK_POLL_MS;
    },
  });
  return { task: q.data?.task, desktop: hasDesktop(q.data?.task) };
}
