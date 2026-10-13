import { Bell } from "lucide-react";
import { NotificationInbox } from "./NotificationInbox";
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from "./ui/dropdown-menu";
import { useNotificationList } from "../hooks/useNotifications";
import { readNotifications, writeNotifications } from "../lib/notifications";
import { cn } from "../lib/utils";

/** NotificationBell is the inbox. It is the last control on the page toolbar. The count is unread open rows. */
export function NotificationBell() {
  const rows = useNotificationList();
  const unread = rows.filter((row) => row.resolvedAt == null && row.readAt == null).length;

  return (
    <DropdownMenu
      onOpenChange={(open) => {
        if (!open) return;
        const pending = readNotifications().filter((row) => row.resolvedAt == null && row.readAt == null);
        if (pending.length === 0) return;
        const now = Date.now();
        const ids = new Set(pending.map((row) => row.id));
        writeNotifications(
          readNotifications().map((row) => (ids.has(row.id) ? { ...row, readAt: now } : row)),
        );
      }}
    >
      <DropdownMenuTrigger
        aria-label={unread > 0 ? `Notifications, ${unread} unread` : "Notifications"}
        className={cn(
          "relative grid size-11 shrink-0 place-items-center rounded-md border border-border bg-panel text-fg",
          "hover:bg-raised",
          "data-[state=open]:bg-raised",
        )}
      >
        <Bell className="size-[18px] stroke-[1.8]" />
        {unread > 0 ? (
          <span className="absolute top-1 right-1 size-1.5 rounded-full bg-warn" />
        ) : null}
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end" className="w-96 p-3">
        <p className="mb-3 text-sm font-medium text-fg">Notifications</p>
        <NotificationInbox rows={rows} />
      </DropdownMenuContent>
    </DropdownMenu>
  );
}
