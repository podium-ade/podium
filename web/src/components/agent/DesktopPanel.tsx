import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ExternalLink, Monitor, RotateCw, SquareTerminal, X } from "lucide-react";
import { Code, connectCode, errorMessage, tasks } from "../../lib/client";
import { Button } from "../ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";

type Tab = "desktop" | "terminal";

const TABS: { id: Tab; label: string; icon: typeof Monitor; frameTitle: string }[] = [
  { id: "desktop", label: "Desktop", icon: Monitor, frameTitle: "The agent's desktop" },
  { id: "terminal", label: "Terminal", icon: SquareTerminal, frameTitle: "A terminal in the agent's workspace" },
];

/**
 * DesktopPanel is the half of a desktop turn's chat that is the turn's machine: its desktop —
 * the screen the agent works on, where the person watches and can take the mouse — and a
 * terminal in its workspace, one tab each. Both are streamed by podium-server from the desktop
 * origin, with links minted for this person and this task; each is an iframe of that other
 * origin on purpose, since the pages inside are voxl's and ttyd's JavaScript.
 *
 * A tab, once opened, stays mounted while the other is shown: switching back finds the same
 * stream and the same shell rather than reconnecting.
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
    // The links are good for hours; fresh ones per mount would reload both for nothing.
    staleTime: 60 * 60_000,
    retry: (count, err) =>
      connectCode(err) !== Code.FailedPrecondition && count < 2,
  });
  const urls: Record<Tab, string | undefined> = {
    desktop: ticket.data?.url || undefined,
    terminal: ticket.data?.terminalUrl || undefined,
  };
  // The terminal's tab comes with its link: a task that serves none has the desktop alone.
  const tabs = TABS.filter((t) => t.id === "desktop" || urls.terminal);
  const [tab, setTab] = useState<Tab>("desktop");
  const [opened, setOpened] = useState<ReadonlySet<Tab>>(() => new Set(["desktop"]));
  const active = tabs.some((t) => t.id === tab) ? tab : "desktop";
  const activeLabel = active === "desktop" ? "desktop" : "terminal";
  const activeURL = urls[active];

  return (
    <section
      aria-label="The agent's machine"
      data-testid="desktop-panel"
      className="flex min-h-0 min-w-0 flex-1 flex-col border-l border-border bg-panel"
    >
      <Tabs
        value={active}
        onValueChange={(v) => {
          setTab(v as Tab);
          setOpened((s) => new Set(s).add(v as Tab));
        }}
        className="min-h-0 flex-1"
      >
        <div className="flex h-10 shrink-0 items-center border-b border-border pr-2">
          <TabsList className="h-full w-auto gap-0 overflow-visible border-b-0 px-1">
            {tabs.map(({ id, label, icon: Icon }) => (
              <TabsTrigger key={id} value={id} className="h-full min-h-0 px-3 text-xs">
                <Icon aria-hidden />
                {label}
              </TabsTrigger>
            ))}
          </TabsList>
          <div className="ml-auto flex items-center gap-1">
            <Button
              size="icon-xs"
              variant="ghost"
              title="Reconnect"
              aria-label="Reconnect the desktop and terminal"
              onClick={() => void ticket.refetch()}
            >
              <RotateCw className="size-3.5" aria-hidden />
            </Button>
            {activeURL ? (
              <Button size="icon-xs" variant="ghost" asChild title="Open in a new tab">
                <a
                  href={activeURL}
                  target="_blank"
                  rel="noreferrer noopener"
                  aria-label={`Open the ${activeLabel} in a new tab`}
                >
                  <ExternalLink className="size-3.5" aria-hidden />
                </a>
              </Button>
            ) : null}
            <Button
              size="icon-xs"
              variant="ghost"
              title="Hide"
              aria-label="Hide the desktop"
              onClick={onHide}
            >
              <X className="size-3.5" aria-hidden />
            </Button>
          </div>
        </div>
        {tabs.map(({ id, frameTitle }) => {
          const url = urls[id];
          return (
            <TabsContent
              key={id}
              value={id}
              forceMount
              className="relative min-h-0 flex-1 bg-black pt-0 data-[state=inactive]:hidden"
            >
              {url && opened.has(id) ? (
                <iframe
                  key={url}
                  src={url}
                  title={frameTitle}
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
                    : `Connecting to the ${id}…`}
                </p>
              )}
            </TabsContent>
          );
        })}
      </Tabs>
    </section>
  );
}
