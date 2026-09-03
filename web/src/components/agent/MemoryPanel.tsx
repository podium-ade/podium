import { useEffect, useState } from "react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Code } from "@connectrpc/connect";
import type { Memory } from "../../gen/podium/agent/v1/agent_pb";
import { agent, connectCode, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { Badge, Chip, type Tone } from "../Badge";
import { Empty } from "../Empty";
import { TableSkeleton } from "../Skeleton";
import { useToast } from "../Toast";

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
        title="Memory is not configured on this host"
        hint="Set PODIUM_AGENT_MEMORY_URL and PODIUM_AGENT_MEMORY_API_KEY on podium-agent and run the memory service beside it. See docs/agent.md#memory."
      />
    );
  }

  const items = searching ? search.data?.items : list.data?.items;

  return (
    <div className="space-y-4">
      {isAgentUnreachable(active.error) ? (
        <p className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
          podium-agent is not reachable. Check its /readyz on PODIUM_AGENT_LISTEN.
        </p>
      ) : null}

      <div className="space-y-2">
        <input
          type="search"
          data-testid="memory-search"
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          placeholder="Search what the agents remember"
          aria-label="Search memories"
          className="w-full rounded border border-border bg-panel px-3 py-2 text-sm text-fg placeholder:text-muted focus-visible:ring-1 focus-visible:ring-accent"
        />
        <p className="text-xs text-muted">
          One memory bank, shared by every agent turn. Anything an agent reads — a Slack
          message, a ticket, a repository — can try to plant a false memory here, and this
          list is where a human catches it. Forget anything that looks wrong.
        </p>
      </div>

      {active.isPending ? <TableSkeleton rows={4} cols={2} /> : null}

      {active.isError && !isAgentUnreachable(active.error) ? (
        <Empty
          title={searching ? "Could not search the memory" : "Could not read the memory"}
          hint={errorMessage(active.error)}
        />
      ) : null}

      {items?.length === 0 ? (
        searching ? (
          <Empty title="Nothing remembered matches that." />
        ) : (
          <Empty
            title="Nothing remembered yet."
            hint="Agents retain durable facts about your organisation here after each turn."
          />
        )
      ) : null}

      <ul className="space-y-2">
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
        <button
          type="button"
          onClick={() => setPages((p) => [...p, list.data.nextCursor])}
          className="rounded border border-border px-3 py-1.5 text-xs text-muted hover:text-fg focus-visible:ring-1 focus-visible:ring-accent"
        >
          Load more
        </button>
      ) : null}
    </div>
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

  return (
    <li
      data-testid="memory-row"
      className="rounded border border-border bg-panel px-3 py-2 text-sm"
    >
      <div className="flex items-start gap-3">
        <p className="min-w-0 flex-1 whitespace-pre-wrap break-words text-fg">{memory.text}</p>
        <Badge tone={FACT_TONE[memory.factType] ?? "idle"}>{memory.factType || "fact"}</Badge>
        {confirming ? null : (
          <button
            type="button"
            data-testid="memory-delete"
            aria-label="Forget this memory"
            onClick={() => setConfirming(true)}
            className="rounded border border-border px-2 py-0.5 text-xs text-muted hover:border-err/50 hover:text-err focus-visible:ring-1 focus-visible:ring-accent"
          >
            Forget
          </button>
        )}
      </div>

      {confirming ? (
        <div className="mt-2 flex flex-wrap items-center gap-2 rounded border border-err/40 bg-err/10 px-2 py-1.5 text-xs">
          <span className="text-err">Forget this? Every future agent turn stops seeing it.</span>
          <button
            type="button"
            data-testid="memory-delete-confirm"
            disabled={forgetting}
            onClick={onForget}
            className="ml-auto rounded border border-err/50 px-2 py-0.5 text-err disabled:opacity-50 focus-visible:ring-1 focus-visible:ring-accent"
          >
            {forgetting ? "Forgetting…" : "Forget"}
          </button>
          <button
            type="button"
            onClick={() => setConfirming(false)}
            className="rounded border border-border px-2 py-0.5 text-muted hover:text-fg focus-visible:ring-1 focus-visible:ring-accent"
          >
            Keep
          </button>
        </div>
      ) : null}

      <div className="mt-2 flex flex-wrap items-center gap-2 text-xs text-muted">
        {source ? (
          meta.source_url ? (
            <a
              href={meta.source_url}
              target="_blank"
              rel="noreferrer noopener"
              className="rounded bg-raised px-1.5 py-0.5 text-accent hover:underline"
            >
              {source}
            </a>
          ) : (
            <Chip>{source}</Chip>
          )
        ) : null}
        {skill ? <Chip>{skill}</Chip> : null}
        {memory.createdAt ? (
          <span title={absolute(memory.createdAt)}>{relative(memory.createdAt)}</span>
        ) : (
          <span>learned at an unknown time</span>
        )}
        {meta.task_id ? (
          <Link to={`/tasks/${meta.task_id}`} className="font-mono text-accent hover:underline">
            {meta.task_id}
          </Link>
        ) : null}
        {memory.entities.length > 0 ? <span>about {memory.entities.join(", ")}</span> : null}
      </div>
    </li>
  );
}
