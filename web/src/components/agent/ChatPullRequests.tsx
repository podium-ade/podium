import { useEffect, useRef, useState } from "react";
import { GitPullRequest, Plus, X } from "lucide-react";
import { useMutation } from "@tanstack/react-query";
import type { ChatPullRequest } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage } from "../../lib/client";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Skeleton } from "../ui/skeleton";
import { Tooltip } from "../ui/tooltip";

/**
 * ChatPullRequests is the pull requests a conversation produced, above the transcript.
 *
 * It is a bar of a fixed height that scrolls sideways rather than a list that grows, which
 * is the whole reason it can sit here: four links do not push the conversation down the
 * page, and the twentieth does not either.
 *
 * The set comes from the chat stream and nothing here keeps a copy. Attaching and
 * detaching publish to every browser watching the chat, so the one that pressed the button
 * and the one that did not are updated by the same frame — there is no local list to fall
 * out of step with the server.
 */
export function ChatPullRequests({
  chatId,
  pullRequests,
  leading,
  trailing,
  loading = false,
  expect,
}: {
  chatId: string;
  pullRequests: ChatPullRequest[];
  /**
   * leading is drawn at the start of the bar: who is in the conversation and where it lives
   * (ChatPeople). With it, an empty set says nothing rather than a sentence, so the bar is
   * the people and the attach button, and one row instead of two.
   */
  leading?: React.ReactNode;
  /** trailing is drawn at the end, before the attach button: the faces of who is in it. */
  trailing?: React.ReactNode;
  /**
   * loading is true until the stream has said what this chat is and what it produced. The bar
   * keeps its height and draws nothing, rather than the empty state and the attach button
   * for a moment before the pull request the chat is about arrives.
   */
  loading?: boolean;
  /**
   * expect is what the row will hold once it loads, as far as the chat list already knows:
   * a surface chip for a conversation that lives elsewhere, faces for one with other people
   * in it. While loading those are drawn as placeholders in their places, shimmering like
   * the transcript's, so the row fills in rather than appearing.
   */
  expect?: { surface: boolean; people: boolean };
}) {
  const toast = useToast();
  const [adding, setAdding] = useState(false);
  const [draft, setDraft] = useState("");
  // The newest link is on the right, which is off the end of a bar that is already full.
  // A turn that just opened a pull request is the case this exists for.
  const strip = useRef<HTMLUListElement>(null);
  useEffect(() => {
    const el = strip.current;
    if (el) el.scrollLeft = el.scrollWidth;
  }, [pullRequests.length]);

  const attach = useMutation({
    mutationFn: (url: string) => agent.attachChatPullRequest({ chatId, url }),
    onSuccess: () => {
      setDraft("");
      setAdding(false);
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const detach = useMutation({
    mutationFn: (url: string) => agent.detachChatPullRequest({ chatId, url }),
    onError: (err) => toast(errorMessage(err)),
  });

  if (loading) {
    return <BarLoading surface={expect?.surface ?? false} people={expect?.people ?? false} />;
  }

  if (adding) {
    return (
      <Bar>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            const url = draft.trim();
            if (url !== "") attach.mutate(url);
          }}
          className="flex min-w-0 flex-1 items-center gap-2"
        >
          <Input
            data-testid="chat-pr-url"
            aria-label="Pull request URL"
            placeholder="https://github.com/owner/repo/pull/123"
            value={draft}
            autoFocus
            disabled={attach.isPending}
            onChange={(e) => setDraft(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Escape") {
                e.preventDefault();
                setAdding(false);
                setDraft("");
              }
            }}
            className="h-7 min-w-0 flex-1 px-2 text-xs"
          />
          <Button type="submit" size="xs" disabled={attach.isPending || draft.trim() === ""}>
            {attach.isPending ? "Attaching…" : "Attach"}
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="xs"
            disabled={attach.isPending}
            onClick={() => {
              setAdding(false);
              setDraft("");
            }}
          >
            Cancel
          </Button>
        </form>
      </Bar>
    );
  }

  // With anything else on the row, an empty set says nothing: the attach button is enough.
  const quiet = (leading !== undefined || trailing !== undefined) && pullRequests.length === 0;
  return (
    <Bar>
      {leading}
      {leading !== undefined && pullRequests.length > 0 ? (
        <span aria-hidden className="h-4 w-px shrink-0 bg-border" />
      ) : null}
      {quiet ? null : <GitPullRequest aria-hidden className="size-3.5 shrink-0 text-faint" />}
      {quiet ? (
        <span className="min-w-0 flex-1" />
      ) : pullRequests.length === 0 ? (
        <span className="min-w-0 flex-1 truncate text-2xs text-faint">
          No pull requests yet. A turn that opens one links it here.
        </span>
      ) : (
        <ul
          ref={strip}
          data-testid="chat-pull-requests"
          className="flex min-w-0 flex-1 items-center gap-1.5 overflow-x-auto"
        >
          {pullRequests.map((pr) => (
            <Chip
              key={pr.url}
              pr={pr}
              detaching={detach.isPending && detach.variables === pr.url}
              onDetach={() => detach.mutate(pr.url)}
            />
          ))}
        </ul>
      )}
      {trailing}
      <Tooltip label="Attach a pull request">
        <Button
          type="button"
          variant="ghost"
          size="icon-xs"
          data-testid="chat-pr-add"
          aria-label="Attach a pull request"
          onClick={() => setAdding(true)}
        >
          <Plus />
        </Button>
      </Tooltip>
    </Bar>
  );
}

/**
 * BarLoading is the row before the stream has said what is on it: placeholders for a surface
 * chip at the left and faces and the attach button at the right, where they will land. A
 * plain web chat expects neither and is an empty row of the same height.
 */
function BarLoading({ surface, people }: { surface: boolean; people: boolean }) {
  let n = 0;
  // The same wave as the transcript skeleton: each shape's shimmer a beat after the last.
  const wave = () => ({ animationDelay: `${n++ * 90}ms` });
  return (
    <Bar>
      <div aria-busy="true" aria-label="Loading" className="flex min-w-0 flex-1 animate-in items-center fade-in-0 duration-300">
        {surface ? <Skeleton className="h-6 w-40 rounded-full" style={wave()} /> : null}
        <span className="flex-1" />
        {people ? (
          <span className="flex items-center gap-2">
            <Skeleton className="size-7 rounded-full" style={wave()} />
            <Skeleton className="size-6 rounded-md" style={wave()} />
          </span>
        ) : null}
      </div>
    </Bar>
  );
}

/**
 * Bar is the fixed-height row itself, so both states are exactly the same height. It runs the
 * pane's full width, edge to edge like the title row above it: where the conversation lives
 * at the far left, its people and the attach button at the far right.
 */
function Bar({ children }: { children: React.ReactNode }) {
  return <div className="flex h-10 shrink-0 items-center gap-2 border-b border-hairline px-5">{children}</div>;
}

/**
 * Chip is one link, rendered as owner/repo#number.
 *
 * That is everything Podium knows about it: the conductor reads a pull request out of a
 * turn's own answer and never asks GitHub, so there is no title to show and no open or
 * merged state to keep in step. The title attribute carries the URL and where the link
 * came from, which is the difference between what this chat did and what somebody filed
 * here afterwards.
 */
function Chip({
  pr,
  detaching,
  onDetach,
}: {
  pr: ChatPullRequest;
  detaching: boolean;
  onDetach: () => void;
}) {
  const origin = pr.source === "human" ? "attached by hand" : "found in a turn's answer";
  return (
    <li className="flex shrink-0 items-center rounded-md border border-border bg-panel shadow-xs">
      <a
        data-testid="chat-pull-request"
        href={pr.url}
        target="_blank"
        rel="noreferrer noopener"
        title={`${pr.url} · ${origin}`}
        className="rounded-l-md py-1 pr-1 pl-2 font-mono text-2xs text-fg outline-none hover:text-accent focus-visible:ring-2 focus-visible:ring-ring/50"
      >
        {pr.owner}/{pr.repo}
        <span className="text-muted">#{pr.number}</span>
      </a>
      <Tooltip label={`Detach ${pr.owner}/${pr.repo}#${pr.number}`}>
        <button
          type="button"
          data-testid="chat-pr-detach"
          aria-label={`Detach ${pr.owner}/${pr.repo}#${pr.number}`}
          disabled={detaching}
          onClick={onDetach}
          className="grid size-5 shrink-0 place-items-center rounded-r-md text-faint outline-none hover:text-err disabled:opacity-45 focus-visible:ring-2 focus-visible:ring-ring/50"
        >
          <X className="size-3" />
        </button>
      </Tooltip>
    </li>
  );
}
