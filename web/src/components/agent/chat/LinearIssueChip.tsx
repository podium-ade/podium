import { useEffect, useRef, useState, type ReactNode } from "react";
import { useLocation, useNavigate } from "react-router";
import { ExternalLink, MessagesSquare } from "lucide-react";
import type { LinearIssue } from "../../../gen/podium/agent/v1/agent_pb";
import { absolute, relative } from "../../../lib/format";
import { stateTone, useLinearIssue } from "../../../lib/linearIssues";
import { cn } from "../../../lib/utils";
import { Badge, type Tone } from "../../Badge";
import { Button } from "../../ui/button";
import { Popover, PopoverAnchor, PopoverContent } from "../../ui/popover";

/** How long a pointer rests on a chip before its card opens, and is away before it closes. */
const OPEN_MS = 350;
const CLOSE_MS = 150;

const DOT: Record<Tone, string> = {
  ok: "bg-ok",
  err: "bg-err",
  warn: "bg-warn",
  run: "bg-run",
  idle: "bg-idle",
  lost: "bg-lost",
};

/** StateDot is a Linear state's kind as a small square in the app's status colours. */
export function StateDot({ stateType, className }: { stateType: string; className?: string }) {
  return (
    <span
      aria-hidden
      className={cn("inline-block size-1.5 shrink-0 rounded-[1px]", DOT[stateTone(stateType)], className)}
    />
  );
}

/**
 * LinearIssueChip is a Linear issue a message names, drawn as a chip — its state, identifier and
 * title — once the conductor says the viewer may see it. Until then, and for good if it does not,
 * it is children: the text or link the message had.
 *
 * Its card opens on a resting pointer and on a press, and says what Linear says now: the state,
 * team, assignee and when it last changed, a link to Linear, and the issue's conversation in
 * Podium when the viewer may read it.
 */
export function LinearIssueChip({ issueRef, children }: { issueRef: string; children: ReactNode }) {
  const chip = useLinearIssue(issueRef, false);
  const [open, setOpen] = useState(false);
  // A card opened by the pointer leaves focus where it was; one opened by a press takes it, so a
  // keyboard reaches its links.
  const [byPointer, setByPointer] = useState(false);
  const timer = useRef<ReturnType<typeof setTimeout> | undefined>(undefined);
  useEffect(() => () => clearTimeout(timer.current), []);

  const issue = chip.data;
  if (!issue) return <>{children}</>;

  const hoverIn = () => {
    clearTimeout(timer.current);
    if (open) return;
    timer.current = setTimeout(() => {
      setByPointer(true);
      setOpen(true);
    }, OPEN_MS);
  };
  const hoverOut = () => {
    clearTimeout(timer.current);
    if (!byPointer) return;
    timer.current = setTimeout(() => setOpen(false), CLOSE_MS);
  };

  return (
    <Popover open={open} onOpenChange={setOpen}>
      <PopoverAnchor asChild>
        <button
          type="button"
          data-testid="linear-chip"
          aria-label={`${issue.identifier}: ${issue.title}${issue.stateName ? `, ${issue.stateName}` : ""}`}
          aria-expanded={open}
          aria-haspopup="dialog"
          onPointerEnter={(e) => e.pointerType === "mouse" && hoverIn()}
          onPointerLeave={(e) => e.pointerType === "mouse" && hoverOut()}
          onClick={() => {
            clearTimeout(timer.current);
            setByPointer(false);
            setOpen((o) => !(o && !byPointer));
          }}
          className={cn(
            "mx-px inline-flex max-w-[22rem] translate-y-[-1px] items-center gap-1.5 rounded-md border border-border bg-panel px-1.5 align-middle",
            "text-[0.8125rem] leading-5 text-fg shadow-xs outline-none transition-colors hover:border-line-strong",
            "focus-visible:ring-2 focus-visible:ring-ring/50",
          )}
        >
          <StateDot stateType={issue.stateType} />
          <span className="shrink-0 font-mono text-2xs text-muted">{issue.identifier}</span>
          <span className="min-w-0 truncate">{issue.title}</span>
        </button>
      </PopoverAnchor>
      <PopoverContent
        align="start"
        className="w-80 p-0"
        onOpenAutoFocus={(e) => byPointer && e.preventDefault()}
        onPointerEnter={() => clearTimeout(timer.current)}
        onPointerLeave={(e) => e.pointerType === "mouse" && hoverOut()}
      >
        <IssueCard issueRef={issueRef} chip={issue} enabled={open} />
      </PopoverContent>
    </Popover>
  );
}

/**
 * IssueCard is the issue as Linear has it now, asked for when the card opens; the chip's own
 * answer stands in until it arrives, and if Linear cannot be asked.
 */
function IssueCard({ issueRef, chip, enabled }: { issueRef: string; chip: LinearIssue; enabled: boolean }) {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  const detail = useLinearIssue(issueRef, true, enabled);
  const issue = detail.data ?? chip;
  const tone = stateTone(issue.stateType);
  const team = issue.teamName || issue.teamKey;
  return (
    <div data-testid="linear-card" className="space-y-3 p-3">
      <div className="flex items-center gap-2">
        <span className="font-mono text-2xs text-muted">{issue.identifier}</span>
        {team ? <span className="truncate text-2xs text-faint">{team}</span> : null}
        <span className="flex-1" />
        {issue.stateName ? <Badge tone={tone}>{issue.stateName}</Badge> : null}
      </div>
      <p className="text-sm leading-snug font-medium text-fg">{issue.title || "Untitled"}</p>
      <dl className="grid grid-cols-[auto_1fr] gap-x-3 gap-y-1 text-xs">
        <dt className="text-faint">Assignee</dt>
        <dd className="truncate text-fg">
          {detail.isPending && enabled ? "…" : issue.assignee || (issue.cached ? "—" : "Unassigned")}
        </dd>
        <dt className="text-faint">Updated</dt>
        <dd className="text-fg" title={absolute(issue.updatedAt)}>
          {issue.updatedAt ? relative(issue.updatedAt) : "—"}
        </dd>
      </dl>
      <div className="flex flex-wrap gap-2 border-t border-hairline pt-3">
        {issue.url ? (
          <Button asChild size="xs" variant="outline">
            <a href={issue.url} target="_blank" rel="noreferrer noopener">
              <ExternalLink aria-hidden />
              Open in Linear
            </a>
          </Button>
        ) : null}
        {issue.chatId && !inChat(pathname, issue.chatId) ? (
          <Button
            size="xs"
            variant="outline"
            data-testid="linear-card-continue"
            onClick={() => void navigate(`/agent/chat/${issue.chatId}`)}
          >
            <MessagesSquare aria-hidden />
            Continue in ticket
          </Button>
        ) : null}
      </div>
    </div>
  );
}

/** inChat says whether the page is already the chat a card would continue in. */
function inChat(pathname: string, chatId: string): boolean {
  const here = `/agent/chat/${chatId}`;
  return pathname === here || pathname.startsWith(`${here}/`);
}
