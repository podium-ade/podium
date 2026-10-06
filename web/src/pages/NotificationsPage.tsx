import { useEffect } from "react";
import { Bell } from "lucide-react";
import { Empty } from "../components/Empty";
import { NotificationInbox } from "../components/NotificationInbox";
import { PageFrame } from "../components/PageHeader";
import { useNotificationList } from "../hooks/useNotifications";
import { useViewer } from "../lib/identity";
import { readNotifications, writeNotifications } from "../lib/notifications";

/**
 * NotificationsPage is the same inbox the bell opens, on its own route.
 * Opening it marks the open rows read. The bell count is the unread ones.
 */
export function NotificationsPage() {
  const viewer = useViewer();
  const rows = useNotificationList();

  useEffect(() => {
    const unread = rows.filter((row) => row.resolvedAt == null && row.readAt == null);
    if (unread.length === 0) return;
    const now = Date.now();
    const ids = new Set(unread.map((row) => row.id));
    writeNotifications(
      readNotifications().map((row) => (ids.has(row.id) ? { ...row, readAt: row.readAt ?? now } : row)),
    );
  }, [rows]);

  if (viewer && !viewer.agentEnabled) {
    return (
      <PageFrame title="Notifications">
        <Empty
          icon={Bell}
          title="The conductor is not configured on this control plane"
          hint="Set PODIUM_AGENT_URL and PODIUM_AGENT_TOKEN on podium-server and run podium-agent beside it."
        />
      </PageFrame>
    );
  }

  return (
    <PageFrame title="Notifications">
      <p className="max-w-2xl text-sm leading-relaxed text-muted">
        Alerts need a person. Standard notices are informational. System errors are failures of
        Podium itself. This browser keeps the history.
      </p>
      <NotificationInbox rows={rows} />
    </PageFrame>
  );
}
