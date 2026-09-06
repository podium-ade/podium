import { useEffect, useState } from "react";
import { Brain, Search, Trash2 } from "lucide-react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code } from "@connectrpc/connect";
import type { Memory } from "../../gen/podium/agent/v1/agent_pb";
import { agent, connectCode, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { Badge, Chip, type Tone } from "../Badge";
import { Empty } from "../Empty";
import { PageHeader } from "../PageHeader";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Tooltip } from "../ui/tooltip";
import { ConductorDown } from "./ConductorDown";

/** How long the search box waits before asking. */
const DEBOUNCE_MS = 300;

/** PAGE is how many memories a page of the list holds. */
const PAGE = 25;

/**
 * A world fact reads as settled, an experience as something that happened, an observation as
 * something the memory engine worked out for itself rather than being told.
 */
const FACT_TONE: Record<string, Tone> = {
  world: "idle",
  experience: "run",
  observation: "ok",
};

/**
 * MemoryPanel is the shared memory, and the only place a human sees it.
 *
 * Everything on this screen is content written by an agent turn out of material a human, a
 * repository or a ticket supplied. Nothing here interprets it, and nothing here trusts it:
 * the provenance chips and the forget button are the point of the screen.
 */
export function MemoryPanel() {
  const toast = useToast();
  const qc = useQueryClient();
  const [typed, setTyped] = useState("");
  const [query, setQuery] = useState("");
  const [pages, setPages] = useState<string[]>([""]);

  useEffect(() => {
    const id = setTimeout(() => {
      setQuery(typed.trim());
      setPages([""]);
    }, DEBOUNCE_MS);
    return () => clearTimeout(id);
  }, [typed]);

  const searching = query !== "";

  const list = useQuery({
    queryKey: ["agent", "memories", pages],
    queryFn: async () => {
      const out: Memory[] = [];
      let next = "";
      for (const cursor of pages) {
        const res = await agent.listMemories({ cursor, limit: PAGE });
        out.push(...res.items);
        next = res.nextCursor;
      }
      return { items: out, nextCursor: next };
    },
    enabled: !searching,
    // Asking for another page changes the key, so without this the list would blink back to
    // a skeleton to show one more page of what is already on the screen.
    placeholderData: (prev) => prev,
  });

  const search = useQuery({
    queryKey: ["agent", "memories", "search", query],
    queryFn: () => agent.searchMemories({ query, limit: PAGE }),
    enabled: searching,
  });

  const forget = useMutation({
    mutationFn: (id: string) => agent.deleteMemory({ id }),
    onSuccess: async () => {
      toast("Forgotten", "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "memories"] });
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const active = searching ? search : list;

  // An install with no memory service is not a broken page: the RPC says so by name.
  if (connectCode(active.error) === Code.FailedPrecondition) {
    return (
      <Empty
        icon={Brain}
        title="Memory is not configured on this host"
        hint="Set PODIUM_AGENT_MEMORY_URL and PODIUM_AGENT_MEMORY_API_KEY on podium-agent and run the memory service beside it. See docs/agent.md#memory."
      />
    );
  }

  const items = searching ? search.data?.items : list.data?.items;
  const failed = active.isError && !isAgentUnreachable(active.error);

  return (
    <div className="space-y-5">
      <PageHeader
        title="Memory"
        description="One bank of durable facts, shared by every agent turn. A turn retains what it worked out; this is where a human reads it back and throws out what is wrong."
      />

      {isAgentUnreachable(active.error) ? (
        <ConductorDown
          what={searching ? "The search could not run" : "The memory could not be read"}
          onRetry={() => void active.refetch()}
          retrying={active.isFetching}
        />
      ) : null}

      {/* role="note" rather than the default status: this is standing copy, not something
          that just happened, and a live region that never changes is noise to a reader. */}
      <Alert variant="warn" role="note" title="Treat every line here as something an agent was told">
        Anything an agent reads — a Slack message, a ticket, a repository — can try to plant a
        false memory here. This list is where a human catches it: forget anything that looks
        wrong.
      </Alert>

      <div className="space-y-2">
        <div className="relative max-w-xl">
          <Search
            aria-hidden
            className="pointer-events-none absolute top-1/2 left-3 size-3.5 -translate-y-1/2 text-faint"
          />
          <Input
            type="search"
            data-testid="memory-search"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
            placeholder="Search what the agents remember"
            aria-label="Search memories"
            className="pl-8"
          />
        </div>
        <p className="text-2xs text-faint" aria-live="polite">
          {searching ? (
            active.isPending ? (
              <>Searching for “{query}”…</>
            ) : (
              <>
                <span className="tabular">{items?.length ?? 0}</span>{" "}
                {items?.length === 1 ? "match" : "matches"} for “{query}”
                {items && items.length > 0 ? ", closest first" : ""}
              </>
            )
          ) : active.isPending ? (
            <>Reading the memory…</>
          ) : (
            <>
              <span className="tabular">{items?.length ?? 0}</span> most recent
              {list.data?.nextCursor ? ", and there are more" : ""}
            </>
          )}
        </p>
      </div>

      {active.isPending ? <MemorySkeleton /> : null}

      {failed ? (
        <Empty
          icon={Brain}
          title={searching ? "Could not search the memory" : "Could not read the memory"}
          hint={errorMessage(active.error)}
        />
      ) : null}

      {items?.length === 0 ? (
        searching ? (
          <Empty
            icon={Search}
            title="Nothing remembered matches that."
            hint="The store is not empty — nothing in it is close enough to those words. Try a name, a host or a port instead of a sentence."
          />
        ) : (
          <Empty
            icon={Brain}
            title="Nothing remembered yet."
            hint="Agents retain durable facts about your organisation here after each turn."
          />
        )
      ) : null}

      <ul className="space-y-2.5">
        {items?.map((m) => (
          <MemoryCard
            key={m.id}
            memory={m}
            onForget={() => forget.mutate(m.id)}
            forgetting={forget.isPending && forget.variables === m.id}
          />
        ))}
      </ul>

      {!searching && list.data?.nextCursor ? (
        <Button
          type="button"
          variant="outline"
          size="sm"
          disabled={list.isFetching}
          onClick={() => setPages((p) => [...p, list.data.nextCursor])}
        >
          {list.isFetching ? "Loading…" : "Load more"}
        </Button>
      ) : null}
    </div>
  );
}

/** Dot separates the quiet metadata so two loose words do not read as one phrase. */
function Dot() {
  return <span aria-hidden>·</span>;
}

function MemorySkeleton() {
  return (
    <ul aria-busy="true" aria-label="Loading" className="space-y-2.5">
      {[0, 1, 2].map((i) => (
        <li key={i} className="space-y-3 rounded-xl border border-border bg-card px-4 py-3.5">
          <Skeleton className="h-3.5 w-4/5" />
          <div className="flex gap-2">
            <Skeleton className="h-4 w-16" />
            <Skeleton className="h-4 w-20" />
            <Skeleton className="ml-auto h-4 w-12" />
          </div>
        </li>
      ))}
    </ul>
  );
}

function MemoryCard({
  memory,
  onForget,
  forgetting,
}: {
  memory: Memory;
  onForget: () => void;
  forgetting: boolean;
}) {
  const [confirming, setConfirming] = useState(false);
  const meta = memory.metadata;
  const source = memory.tags.find((t) => t.startsWith("source:"))?.slice("source:".length);
  const skill = memory.tags.find((t) => t.startsWith("skill:"))?.slice("skill:".length);
  // Whatever is left is a plain subject tag the retain step chose, and it is as much of the
  // provenance as the prefixed ones.
  const plain = memory.tags.filter((t) => !t.startsWith("source:") && !t.startsWith("skill:"));
  const quiet = memory.entities.length > 0 || memory.context !== "" || meta.task_id;

  return (
    <li
      data-testid="memory-row"
      className="rounded-xl border border-border bg-card px-4 py-3 shadow-xs"
    >
      <div className="flex items-start gap-3">
        <p className="min-w-0 flex-1 text-sm leading-relaxed break-words whitespace-pre-wrap text-fg">
          {memory.text}
        </p>
        <Tooltip label="Forget this memory">
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            data-testid="memory-delete"
            aria-label="Forget this memory"
            onClick={() => setConfirming(true)}
            className="hover:bg-err/12 hover:text-err"
          >
            <Trash2 />
          </Button>
        </Tooltip>
      </div>

      <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
        <Badge tone={FACT_TONE[memory.factType] ?? "idle"}>{memory.factType || "fact"}</Badge>
        {source ? (
          meta.source_url ? (
            <a
              href={meta.source_url}
              target="_blank"
              rel="noreferrer noopener"
              className="inline-flex w-fit items-center rounded-md border border-accent/35 bg-accent/12 px-1.5 py-0.5 text-2xs text-accent hover:underline"
            >
              {source}
            </a>
          ) : (
            <Chip>{source}</Chip>
          )
        ) : null}
        {skill ? <Chip>/{skill}</Chip> : null}
        {plain.map((t) => (
          <Chip key={t}>{t}</Chip>
        ))}
        <span className="ml-auto shrink-0 text-2xs text-faint">
          {memory.createdAt ? (
            <span title={absolute(memory.createdAt)}>{relative(memory.createdAt)}</span>
          ) : (
            <span>learned at an unknown time</span>
          )}
        </span>
      </div>

      {quiet ? (
        <div className="mt-2.5 flex flex-wrap items-center gap-x-2 gap-y-1 border-t border-hairline pt-2 text-2xs text-faint">
          {memory.entities.length > 0 ? <span>about {memory.entities.join(", ")}</span> : null}
          {memory.entities.length > 0 && memory.context ? <Dot /> : null}
          {memory.context ? <span className="min-w-0 truncate">in {memory.context}</span> : null}
          {meta.task_id ? (
            <>
              <Dot />
              <span>retained by</span>
              <Link
                to={`/tasks/${meta.task_id}`}
                title={meta.task_id}
                className="font-mono text-accent hover:underline"
              >
                {meta.task_id}
              </Link>
            </>
          ) : null}
        </div>
      ) : null}

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Forget this memory?</DialogTitle>
            <DialogDescription>
              Forget this? Every future agent turn stops seeing it.
            </DialogDescription>
          </DialogHeader>
          <p className="rounded-lg border border-border bg-raised/60 px-3 py-2 text-xs leading-relaxed break-words text-fg">
            {memory.text}
          </p>
          <DialogFooter>
            <Button type="button" variant="outline" size="sm" onClick={() => setConfirming(false)}>
              Keep
            </Button>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              data-testid="memory-delete-confirm"
              disabled={forgetting}
              onClick={onForget}
            >
              {forgetting ? "Forgetting…" : "Forget"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </li>
  );
}
