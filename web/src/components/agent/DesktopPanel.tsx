import { useQuery } from "@tanstack/react-query";
import { ExternalLink, Monitor, RotateCw, X } from "lucide-react";
import { Code, connectCode, errorMessage, tasks } from "../../lib/client";
import { Button } from "../ui/button";

/**
 * DesktopPanel is a running desktop turn's desktop: the screen the agent works on, streamed by
 * podium-server from the desktop origin, where the person watches and can take the mouse. The
 * link is minted for this person and this task; it is an iframe of another origin on purpose,
 * since the page inside is voxl's JavaScript.
 */
export function DesktopPanel({
  taskId,
  onHide,
}: {
  taskId: string;
  onHide: () => void;
}) {
  const ticket = useQuery({
    queryKey: ["desktop-ticket", taskId],
    queryFn: () => tasks.createDesktopTicket({ taskId }),
    // The link is good for hours; a fresh one per mount would reload the desktop for nothing.
    staleTime: 60 * 60_000,
    retry: (count, err) =>
      connectCode(err) !== Code.FailedPrecondition && count < 2,
  });
  const url = ticket.data?.url;

  return (
    <section
      aria-label="Desktop"
      data-testid="desktop-panel"
      className="flex min-h-0 min-w-0 flex-col border-l border-border bg-panel"
    >
      <header className="flex h-10 shrink-0 items-center gap-2 border-b border-border px-3 text-sm">
        <Monitor className="size-4 text-muted" aria-hidden />
        <span className="font-medium text-fg">Desktop</span>
        <span className="truncate text-xs text-faint">
          the agent's screen · click in to take the mouse
        </span>
        <div className="ml-auto flex items-center gap-1">
          <Button
            size="icon-xs"
            variant="ghost"
            title="Reconnect"
            aria-label="Reconnect the desktop"
            onClick={() => void ticket.refetch()}
          >
            <RotateCw className="size-3.5" aria-hidden />
          </Button>
          {url ? (
            <Button
              size="icon-xs"
              variant="ghost"
              asChild
              title="Open in a new tab"
            >
              <a
                href={url}
                target="_blank"
                rel="noreferrer noopener"
                aria-label="Open the desktop in a new tab"
              >
                <ExternalLink className="size-3.5" aria-hidden />
              </a>
            </Button>
          ) : null}
          <Button
            size="icon-xs"
            variant="ghost"
            title="Hide the desktop"
            aria-label="Hide the desktop"
            onClick={onHide}
          >
            <X className="size-3.5" aria-hidden />
          </Button>
        </div>
      </header>
      <div className="relative min-h-0 flex-1 bg-black">
        {url ? (
          <iframe
            key={url}
            src={url}
            title="The agent's desktop"
            className="absolute inset-0 size-full border-0"
            allow="clipboard-read; clipboard-write; fullscreen"
            referrerPolicy="no-referrer"
          />
        ) : (
          <p
            role="status"
            className="absolute inset-0 grid place-items-center p-6 text-center text-sm text-muted"
          >
            {ticket.isError
              ? errorMessage(ticket.error)
              : "Connecting to the desktop…"}
          </p>
        )}
      </div>
    </section>
  );
}
