import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ExternalLink, LoaderCircle, Monitor, RotateCw, SquareTerminal, X } from "lucide-react";
import { Code, connectCode, errorMessage, tasks } from "../../lib/client";
import { Button } from "../ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";

type Tab = "desktop" | "terminal";

/** How often the panel asks whether a starting desktop answers yet. */
const READY_POLL_MS = 2000;

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
    // A task runs before its desktop is up, and a frame loaded in between keeps the error page
    // it got. Until the server says the desktop answers, ask again; the links it mints
    // meanwhile are never loaded, so the one in use does not change under the frame.
    refetchInterval: (q) => (q.state.data && !q.state.data.ready ? READY_POLL_MS : false),
  });
  const ready = ticket.data?.ready ?? false;
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
  const activeURL = ready ? urls[active] : undefined;

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
              {url && ready && opened.has(id) ? (
                <iframe
                  key={url}
                  src={url}
                  title={frameTitle}
                  className="absolute inset-0 size-full border-0"
                  allow="clipboard-read; clipboard-write; fullscreen"
                  referrerPolicy="no-referrer"
                />
              ) : (
                <Starting what={id} error={ticket.isError ? errorMessage(ticket.error) : undefined} />
              )}
            </TabsContent>
          );
        })}
      </Tabs>
    </section>
  );
}

/**
 * Starting is a tab before its frame loads: the desktop coming up, or why it cannot. The
 * display, XFCE, the browser and the stream take seconds, and the first desktop on a machine
 * waits for its image.
 */
function Starting({ what, error }: { what: Tab; error?: string }) {
  return (
    <div
      role="status"
      className="absolute inset-0 flex flex-col items-center justify-center gap-3 p-6 text-center"
    >
      {error ? (
        <p className="max-w-sm text-sm text-muted">{error}</p>
      ) : (
        <>
          <LoaderCircle className="size-5 animate-spin text-muted" aria-hidden />
          <p className="text-sm text-fg">Starting the {what}…</p>
          <p className="max-w-xs text-xs leading-relaxed text-faint">
            The display, the browser and the stream take a few seconds. The first desktop on a
            machine can take a minute or two.
          </p>
        </>
      )}
    </div>
  );
}
