import { useInfiniteQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { ExternalLink } from "lucide-react";
import type { WorkItem } from "../../../gen/podium/agent/v1/agent_pb";
import { errorMessage } from "../../../lib/client";
import { absolute, relative } from "../../../lib/format";
import { money } from "../../../lib/routines";
import { cn } from "../../../lib/utils";
import { Badge } from "../../Badge";
import { Skeleton } from "../../Skeleton";
import { Button } from "../../ui/button";
import { ProviderMark } from "./ProviderMark";
import { COLUMNS, type Column, type WorkFilters, boardColumnQuery, itemLabel, podiumState, providerName } from "./workView";

/**
 * WorkBoard is the Issues view as columns, one per status: the category each issue tracker's own
 * state falls in (Linear's "In Review" is In progress). Each column is its own paged query, so a
 * busy Done column never holds back In progress, and Load more reads the next ten of that column
 * alone. Cards move when the tracker says they did; the board does not move them, since that
 * would be writing to the tracker.
 */
export function WorkBoard({ filters }: { filters: WorkFilters }) {
  return (
    <div
      data-testid="work-board"
      className="-mx-1 flex items-start gap-3 overflow-x-auto px-1 pb-2"
    >
      {COLUMNS.map((col) => (
        <BoardColumn key={col.status} col={col} filters={filters} />
      ))}
    </div>
  );
}

function BoardColumn({ col, filters }: { col: Column; filters: WorkFilters }) {
  const q = useInfiniteQuery(boardColumnQuery(filters, col.status));
  const cards = q.data?.pages.flatMap((p) => p.items) ?? [];
  if (col.optional && cards.length === 0) return null;

  const total = q.data?.pages[0]?.total ?? 0;
  // A server that counts nothing: say what is loaded, and that more exist.
  const count = total > 0 ? String(total) : `${cards.length}${q.hasNextPage ? "+" : ""}`;
  const Icon = col.icon;
  const heading = `work-col-${col.status}`;

  return (
    <section
      aria-labelledby={heading}
      data-testid={`work-column-${col.status}`}
      className="flex max-h-[max(24rem,calc(100dvh-16rem))] min-w-[16rem] flex-1 basis-0 flex-col rounded-xl border border-border bg-panel"
    >
      <header className="flex items-center gap-2 border-b border-hairline px-3 py-2.5">
        <Icon aria-hidden className="size-3.5 shrink-0 text-muted" />
        <h3 id={heading} className="text-xs font-semibold text-fg">
          {col.label}
        </h3>
        {q.isPending ? null : (
          <span className="text-xs tabular-nums text-faint" data-testid="work-column-count">
            {count}
          </span>
        )}
      </header>
      <div
        aria-busy={q.isPlaceholderData || q.isPending}
        className={cn(
          "min-h-0 flex-1 overflow-y-auto p-2 transition-opacity duration-150",
          q.isPlaceholderData && "opacity-60",
        )}
      >
        {q.isPending ? (
          <div className="space-y-2" aria-label="Loading">
            {[0, 1, 2].map((i) => (
              <Skeleton key={i} className="h-[5.5rem] rounded-lg" style={{ opacity: 1 - i * 0.25 }} />
            ))}
          </div>
        ) : q.isError ? (
          <p className="px-1 py-2 text-2xs text-err">{errorMessage(q.error)}</p>
        ) : cards.length === 0 ? (
          <p className="px-1 py-2 text-2xs text-faint">None</p>
        ) : (
          <ol className="space-y-2">
            {cards.map((c) => (
              <BoardCard key={c.chatId} c={c} />
            ))}
          </ol>
        )}
        {q.hasNextPage ? (
          <Button
            variant="ghost"
            size="sm"
            className="mt-2 w-full text-muted"
            disabled={q.isFetchingNextPage}
            onClick={() => void q.fetchNextPage()}
          >
            {q.isFetchingNextPage ? "Loading…" : "Load more"}
          </Button>
        ) : null}
      </div>
    </section>
  );
}

/**
 * BoardCard is one issue. Its title is the link to the issue's conversation, stretched over the
 * whole card, so the card opens with a click anywhere or with Enter on the focused title; its
 * key, raised above that, opens the issue in its tracker, whose own name for its state is beside
 * the team.
 */
function BoardCard({ c }: { c: WorkItem }) {
  const state = podiumState(c.state);
  const label = itemLabel(c);
  const where = providerName(c.provider);
  const meta = [c.team, c.statusLabel].filter(Boolean).join(" · ");
  return (
    <li
      data-testid="work-card"
      className="relative rounded-lg border border-hairline bg-panel p-2.5 transition-colors duration-150 hover:border-line-strong hover:bg-raised/40"
    >
      <div className="flex items-center justify-between gap-2">
        <span className="flex min-w-0 items-center gap-1.5">
          <ProviderMark provider={c.provider} className="relative z-10" />
          {c.url ? (
            <a
              href={c.url}
              target="_blank"
              rel="noreferrer"
              className="relative z-10 inline-flex items-center gap-1 font-mono text-xs text-accent hover:underline"
              title={`Open in ${where}`}
            >
              {label}
              <ExternalLink aria-hidden className="size-3" />
            </a>
          ) : (
            <span className="font-mono text-xs text-muted">{label}</span>
          )}
        </span>
        {meta ? (
          <span className="truncate text-2xs text-faint" title={c.statusLabel ? `In ${where}: ${c.statusLabel}` : undefined}>
            {meta}
          </span>
        ) : null}
      </div>
      <Link
        to={`/agent/chat/${c.chatId}`}
        aria-label={`Open the conversation of ${c.key ? `${c.key} ` : ""}${c.title || "Untitled"}`}
        title={c.title}
        className={cn(
          "mt-1 line-clamp-2 text-sm leading-snug text-fg outline-none",
          "after:absolute after:inset-0 after:rounded-lg after:content-['']",
          "focus-visible:after:ring-2 focus-visible:after:ring-ring/50",
        )}
      >
        {c.title || "Untitled"}
      </Link>
      <div className="mt-2 flex items-center justify-between gap-2">
        <Badge tone={state.tone} className="px-2 py-0.5">
          {state.label}
        </Badge>
        <span className="truncate text-2xs tabular-nums text-faint" title={absolute(c.lastActivityAt)}>
          {relative(c.lastActivityAt)}
          {c.costUsd > 0 ? ` · ${money(c.costUsd)}` : ""}
        </span>
      </div>
    </li>
  );
}
