import { RotateCw, TriangleAlert } from "lucide-react";
import { useMutation } from "@tanstack/react-query";
import { agent, errorMessage } from "../../../lib/client";
import type { Undelivered } from "../../../lib/chatThread";
import { surfaceName } from "../../../lib/surfaces";
import { useToast } from "../../Toast";
import { Button } from "../../ui/button";

/**
 * DeliveryNotice says that an answer the web chat has did not reach the thread or pull
 * request it was owed to. A copy still being retried says so and needs nothing; one the
 * conductor gave up on offers Retry, which puts it back in the queue and tries it at once.
 * The stream re-sends the message when a delivery changes, so the notice goes away on its
 * own once the copy lands.
 */
export function DeliveryNotice({ items }: { items?: Undelivered[] }) {
  const toast = useToast();
  const retry = useMutation({
    mutationFn: (u: Undelivered) => agent.retryChatDelivery({ chatId: u.chatId, seq: u.seq }),
    onError: (err) => toast(errorMessage(err)),
  });
  if (!items || items.length === 0) return null;

  // One line per message and surface: a pull request's answer can be owed to the PR and to
  // a Slack thread, and each fails on its own.
  const seen = new Set<string>();
  const rows = items.filter((u) => {
    const key = `${u.seq}:${u.surface}`;
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });

  return (
    <div className="space-y-1">
      {rows.map((u) => {
        const where = surfaceName(u.surface);
        const failed = u.status === "failed";
        return (
          <div
            key={`${u.seq}:${u.surface}`}
            data-testid="chat-undelivered"
            data-status={u.status}
            className="flex flex-wrap items-center gap-2 text-2xs text-muted"
            title={u.error || undefined}
          >
            <TriangleAlert aria-hidden className={failed ? "size-3.5 text-err" : "size-3.5 text-warn"} />
            <span>{failed ? `Not delivered to ${where}` : `Delivering to ${where} — retrying`}</span>
            {failed ? (
              <>
                <span aria-hidden>·</span>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  className="h-6 px-1.5 text-2xs"
                  disabled={retry.isPending}
                  onClick={() => {
                    retry.mutate(u);
                  }}
                >
                  <RotateCw className="size-3" />
                  Retry
                </Button>
              </>
            ) : null}
          </div>
        );
      })}
    </div>
  );
}
