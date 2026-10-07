import { Bell } from "lucide-react";
import { Link } from "react-router";
import { Empty } from "./Empty";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import {
  NOTIFICATION_LEVELS,
  ago,
  readNotifications,
  writeNotifications,
  type AppNotification,
  type NotificationLevel,
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
        <p>{row.body}</p>
        <p className="mt-1 text-2xs">
          {LEVEL_NAME[row.level]}
          {" · "}
          {ago(row.createdAt)}
          {resolved && row.resolvedAt ? ` · resolved ${ago(row.resolvedAt)}` : ""}
        </p>
        {resolved || !row.href || !row.action ? null : (
          <Button asChild size="sm" className="mt-2">
            <Link to={row.href}>{row.action}</Link>
          </Button>
        )}
      </Alert>
    </article>
  );
}
