import { useId, useState } from "react";
import { Bell } from "lucide-react";
import { Link } from "react-router";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Empty } from "./Empty";
import { useToast } from "./Toast";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import { Textarea } from "./ui/textarea";
import { agent, errorMessage } from "../lib/client";
import {
  NOTIFICATION_LEVELS,
  ago,
  readNotifications,
  writeNotifications,
  type AppNotification,
  type NotificationLevel,
  type NotificationReply,
} from "../lib/notifications";
import { cn } from "../lib/utils";

const LEVEL_GROUP: Record<NotificationLevel, string> = {
  error: "System errors",
  alert: "Alerts",
  standard: "Standard",
};

const LEVEL_NAME: Record<NotificationLevel, string> = {
  error: "System error",
  alert: "Alert",
  standard: "Standard",
};

const LEVEL_VARIANT = {
  error: "destructive",
  alert: "warn",
  standard: "info",
} as const;

/** NotificationInbox is the one list the bell and the page both render.
 * Open rows are grouped by level. History keeps the same row, marked resolved. */
export function NotificationInbox({ rows }: { rows: readonly AppNotification[] }) {
  const active = rows.filter((row) => row.resolvedAt == null);
  const history = rows.filter((row) => row.resolvedAt != null);

  return (
    <div className="space-y-5">
      {active.length === 0 ? (
        <Empty
          icon={Bell}
          title="Nothing needs you right now"
          hint="An expired subscription sign-in shows up here as an alert, and stays in the history after you renew it."
          className="px-4 py-8"
        />
      ) : (
        NOTIFICATION_LEVELS.map((level) => {
          const group = active.filter((row) => row.level === level);
          if (group.length === 0) return null;
          return (
            <section key={level} className="space-y-2" aria-label={LEVEL_GROUP[level]}>
              <h2 className="text-2xs font-medium tracking-wider text-faint uppercase">
                {LEVEL_GROUP[level]}
              </h2>
              <ul className="space-y-2">
                {group.map((row) => (
                  <li key={row.id}>
                    <NotificationRow row={row} />
                  </li>
                ))}
              </ul>
            </section>
          );
        })
      )}

      <section className="space-y-2" aria-label="History">
        <div className="flex items-center justify-between gap-2">
          <h2 className="text-2xs font-medium tracking-wider text-faint uppercase">History</h2>
          {history.length > 0 ? (
            <Button
              variant="ghost"
              size="xs"
              data-testid="notifications-clear-history"
              onClick={() =>
                writeNotifications(readNotifications().filter((row) => row.resolvedAt == null))
              }
            >
              Clear history
            </Button>
          ) : null}
        </div>
        {history.length === 0 ? (
          <p className="text-xs text-muted" data-testid="notifications-history-empty">
            No past notifications yet.
          </p>
        ) : (
          <ul className="space-y-2">
            {history.map((row) => (
              <li key={row.id}>
                <NotificationRow row={row} />
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}

function NotificationRow({ row }: { row: AppNotification }) {
  const resolved = row.resolvedAt != null;
  return (
    <article
      data-testid="notification"
      data-level={row.level}
      data-state={resolved ? "resolved" : "open"}
      className={cn(resolved && "opacity-70")}
    >
      <Alert
        variant={resolved ? "default" : LEVEL_VARIANT[row.level]}
        role={resolved ? "status" : "alert"}
        title={row.title}
      >
        <p className={cn(row.reply && "whitespace-pre-wrap")}>{row.body}</p>
        <p className="mt-1 text-2xs">
          {LEVEL_NAME[row.level]}
          {" · "}
          {ago(row.createdAt)}
          {resolved && row.resolvedAt ? ` · resolved ${ago(row.resolvedAt)}` : ""}
        </p>
        {!resolved && row.reply ? (
          <QuestionReply reply={row.reply} href={row.href} action={row.action} />
        ) : resolved || !row.href || !row.action ? null : (
          <Button asChild size="sm" className="mt-2">
            <Link to={row.href}>{row.action}</Link>
          </Button>
        )}
      </Alert>
    </article>
  );
}

/**
 * QuestionReply answers a question the assistant asked in a Linear ticket's conversation without
 * leaving the inbox. The answer is sent to that conversation exactly as the chat's own box sends
 * it: it is posted to the issue as the viewer's comment and goes to the task waiting on it. The
 * row closes when the conversation hears the answer, from here or anywhere else; until the next
 * poll says so, it says the answer went.
 */
function QuestionReply({ reply, href, action }: { reply: NotificationReply; href: string; action: string }) {
  const id = useId();
  const qc = useQueryClient();
  const toast = useToast();
  const [text, setText] = useState("");
  const [sent, setSent] = useState(false);
  const refresh = () => qc.invalidateQueries({ queryKey: ["agent", "notifications"] });

  const send = useMutation({
    mutationFn: (answer: string) => agent.sendChatMessage({ chatId: reply.chatId, text: answer }),
    onSuccess: () => {
      setSent(true);
      setText("");
      toast("Answer sent. It is posted to the issue as yours.", "ok");
      void refresh();
    },
    onError: (err) => toast(errorMessage(err)),
  });
  const dismiss = useMutation({
    mutationFn: () => agent.markNotificationsRead({ ids: [BigInt(reply.notificationId)] }),
    onSuccess: () => void refresh(),
    onError: (err) => toast(errorMessage(err)),
  });

  if (sent) {
    return (
      <p data-testid="question-sent" className="mt-2 text-xs">
        Answer sent.{" "}
        <Link to={href} className="underline">
          {action}
        </Link>
      </p>
    );
  }

  const submit = () => {
    const answer = text.trim();
    if (answer === "" || send.isPending) return;
    send.mutate(answer);
  };

  return (
    <form
      data-testid="question-reply"
      className="mt-2 space-y-2"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <label htmlFor={id} className="sr-only">
        Your answer
      </label>
      <Textarea
        id={id}
        value={text}
        onChange={(e) => setText(e.target.value)}
        // The bell is a menu, whose type-ahead would take the keystrokes for itself.
        onKeyDown={(e) => {
          e.stopPropagation();
          if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
            e.preventDefault();
            submit();
          }
        }}
        placeholder="Answer here, or in the conversation"
        className="min-h-[64px] text-sm"
      />
      <div className="flex flex-wrap items-center gap-2">
        <Button type="submit" size="sm" disabled={text.trim() === "" || send.isPending}>
          {send.isPending ? "Sending…" : "Send answer"}
        </Button>
        <Button asChild variant="outline" size="sm">
          <Link to={href}>{action}</Link>
        </Button>
        <Button
          type="button"
          variant="ghost"
          size="sm"
          disabled={dismiss.isPending}
          onClick={() => dismiss.mutate()}
        >
          Dismiss
        </Button>
      </div>
    </form>
  );
}
