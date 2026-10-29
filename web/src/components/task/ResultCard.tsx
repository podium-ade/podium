import { useMemo } from "react";
import { TextMessagePartProvider } from "@assistant-ui/react";
import { CircleAlert, CircleHelp, ScrollText } from "lucide-react";
import { TaskStatus } from "../../gen/podium/v1/common_pb";
import type { Task } from "../../gen/podium/v1/task_pb";
import { firstError, type Entry } from "../../lib/entries";
import { isTerminal } from "../../lib/format";
import { MarkdownText } from "../agent/chat/MarkdownText";
import { Chip } from "../Badge";
import { Button } from "../ui/button";
import { Card } from "../ui/card";

/**
 * ResultCard is what a reader came to the page for, before anything else: the task's
 * answer, the question it is waiting on, or — when it failed — the first thing that went
 * wrong, one click from the logs around it.
 */
export function ResultCard({
  task,
  entries,
  onJump,
}: {
  task: Task;
  entries: readonly Entry[];
  /** onJump opens the log explorer on an entry. */
  onJump: (key: string) => void;
}) {
  const { answer, question, problem } = useMemo(() => {
    let answer: Entry | undefined;
    let question: Entry | undefined;
    for (const e of entries) {
      if (e.kind !== "message") continue;
      if (e.message?.type === "final") {
        answer = e;
        question = undefined;
      } else if (e.message?.type === "question") {
        question = e;
      }
    }
    return { answer, question, problem: firstError(entries) };
  }, [entries]);

  const failed = task.status === TaskStatus.FAILED;
  const waiting = question && !isTerminal(task.status) ? question : undefined;
  if (!answer && !waiting && !(failed && problem)) return null;

  return (
    <Card data-testid="task-result" className="overflow-hidden">
      {waiting?.message ? (
        <div className="flex items-start gap-2.5 border-b border-hairline bg-warn-bg px-5 py-3 last:border-b-0">
          <CircleHelp className="mt-0.5 size-4 shrink-0 text-warn" />
          <div className="min-w-0">
            <p className="text-sm font-semibold text-fg">Waiting on an answer</p>
            <p className="mt-0.5 text-sm whitespace-pre-wrap text-muted">{waiting.message.text}</p>
          </div>
        </div>
      ) : null}
      {failed && problem ? (
        <div className="flex flex-wrap items-center gap-x-3 gap-y-2 border-b border-hairline px-5 py-3 last:border-b-0">
          <CircleAlert className="size-4 shrink-0 text-err" />
          <div className="min-w-0 flex-1">
            <p className="text-2xs font-medium tracking-wide text-faint uppercase">
              First error · {problem.source}
            </p>
            <p className="truncate font-mono text-xs text-err" title={problem.summary}>
              {problem.summary}
            </p>
          </div>
          <Button variant="outline" size="sm" onClick={() => onJump(problem.key)}>
            <ScrollText />
            Jump to it in the logs
          </Button>
        </div>
      ) : null}
      {answer?.message ? (
        <div className="px-5 py-4">
          <p className="mb-2 text-2xs font-medium tracking-wide text-faint uppercase">Result</p>
          <TextMessagePartProvider text={answer.message.text}>
            <MarkdownText className="text-sm leading-6" />
          </TextMessagePartProvider>
          {answer.message.attachments.length > 0 ? (
            <span className="mt-2 flex flex-wrap gap-1">
              {answer.message.attachments.map((name) => (
                <Chip key={name}>{name}</Chip>
              ))}
            </span>
          ) : null}
        </div>
      ) : null}
    </Card>
  );
}
