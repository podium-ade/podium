import { Fragment, useEffect, useId, useRef, useState, type ReactNode } from "react";
import {
  GitBranch,
  MessageSquare,
  MessagesSquare,
  Repeat,
  ShieldCheck,
  User,
  Webhook,
  Wrench,
  type LucideIcon,
} from "lucide-react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { type RoutineDraft } from "../../gen/podium/agent/v1/agent_pb";
import {
  browserZone,
  CI_FIXER,
  CI_FIXER_ON_GITHUB,
  GITHUB_CI,
  GITHUB_EVENTS,
  ciOutcome,
  withOutcome,
  GITHUB_GROUPS,
  LINEAR_ACTIONS,
  LINEAR_EVENTS,
  LINEAR_TYPES,
  repoNames,
  teamKeys,
  type ChatMode,
  type Scope,
  type Trigger,
} from "../../lib/routines";
import { agent, errorMessage } from "../../lib/client";
import { cn } from "../../lib/utils";
import { DEFAULT_REPEAT, toCron } from "../../lib/schedule";
import { useBrand } from "../../lib/brand";
import { Button } from "../ui/button";
import { Checkbox } from "../ui/checkbox";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "../ui/dialog";
import { Input } from "../ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { Textarea } from "../ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "../ui/toggle-group";
import { ScheduleField } from "./ScheduleField";
import { TimezonePicker } from "./TimezonePicker";

/** SystemBadge marks a routine that runs as nobody, wherever one is listed, so it is never taken for somebody's own. */
export function SystemBadge({ className }: { className?: string }) {
  return (
    <span
      data-testid="routine-system-badge"
      title="A system routine: admins set it up, and it runs as Podium"
      className={cn(
        "inline-flex w-fit items-center gap-1 rounded-sm border border-line-strong bg-raised px-1.5 py-0.5 text-[11px] font-semibold tracking-[0.02em] text-muted",
        className,
      )}
    >
      <ShieldCheck className="size-3" aria-hidden />
      System
    </span>
  );
}

/** DEFAULT_ASSISTANT stands for "no personal assistant" in the picker, which cannot hold "". */
const DEFAULT_ASSISTANT = "podium";

const SCOPES: { scope: Scope; label: string; hint: string }[] = [
  {
    scope: "personal",
    label: "Me",
    hint: "Runs as you: your model key, your connections, your GitHub account. Only you see it.",
  },
  {
    scope: "system",
    label: "Podium (system)",
    hint: "Runs as nobody, with the company’s credentials. Reads GitHub as the Podium App, in the repositories its instruction names, and acts in Linear as the Podium app. Admins see it; nobody writes in its chat.",
  },
];

/** TRIGGERS are the sources that can start a run. One more source is one more entry, and its fields under the picker. */
const TRIGGERS: { trigger: Trigger; label: string; hint: string; icon: LucideIcon }[] = [
  { trigger: "schedule", label: "On a schedule", hint: "At set times, in a timezone.", icon: Repeat },
  { trigger: "linear", label: "On a Linear event", hint: "When an issue, comment or project changes.", icon: Webhook },
  { trigger: "github", label: "On a GitHub event", hint: "When CI finishes, or a pull request or issue changes.", icon: GitBranch },
];

/**
 * CHAT_MODES are where a routine on Linear events runs: all in its own chat, opened on its first
 * run, or each ticket's events in that ticket's conversation, which needs no chat of the routine's.
 */
const CHAT_MODES: { mode: ChatMode; label: string; hint: string; icon: LucideIcon }[] = [
  {
    mode: "routine",
    label: "The routine’s chat",
    hint: "Every run is in one chat, with the whole history in view. It is opened on the first run.",
    icon: MessageSquare,
  },
  {
    mode: "subject",
    label: "Each ticket’s conversation",
    hint: "A run joins the issue’s or project’s one conversation, where delegations, mentions and other routines are, and sees what was done on it before.",
    icon: MessagesSquare,
  },
];

/**
 * GITHUB_CHAT_MODES are where a routine on GitHub events runs. The pull request's chat comes
 * first and is the default: its reviews, mentions and CI are one conversation, which is the
 * context a run about it needs. A run about no pull request — a push to a branch, an issue —
 * is in the routine's own chat either way.
 */
const GITHUB_CHAT_MODES: { mode: ChatMode; label: string; hint: string; icon: LucideIcon }[] = [
  {
    mode: "subject",
    label: "The pull request’s chat",
    hint: "A run about a pull request joins its chat, where its reviews and everything said about it are, and sees what was done on it before. Events about no pull request run in the routine’s own chat.",
    icon: MessagesSquare,
  },
  {
    mode: "routine",
    label: "A new chat",
    hint: "Every run is in one chat of the routine’s own, with its whole history in view. It is opened on the first run.",
    icon: MessageSquare,
  },
];

/**
 * LinearEventsField is what a routine on Linear events runs on: the events, and optionally the
 * teams. The Linear app's webhook delivers them, so the field says when no app is connected —
 * the routine saves, and runs once one is. Under them is where its runs happen.
 */
function LinearEventsField({
  events,
  onEvents,
  teams,
  onTeams,
  chatMode,
  onChatMode,
}: {
  events: string[];
  onEvents: (events: string[]) => void;
  teams: string;
  onTeams: (teams: string) => void;
  chatMode: ChatMode;
  onChatMode: (mode: ChatMode) => void;
}) {
  const ids = { events: useId(), teams: useId() };
  const connections = useQuery({
    queryKey: ["agent", "connections"],
    queryFn: () => agent.getConnections({}),
    staleTime: 30_000,
  });
  const lin = connections.data?.linear;
  const connected = Boolean(lin?.configured) && !lin?.revokedAt;
  const toggle = (event: string, on: boolean) =>
    onEvents(LINEAR_EVENTS.map((e) => e.event).filter((e) => (e === event ? on : events.includes(e))));
  return (
    <>
      <div className="grid gap-1.5" role="group" aria-labelledby={ids.events}>
        <span id={ids.events} className="text-sm font-medium">
          Linear events
        </span>
        {/* A grid of what by what happened to it: eighteen labels in a row read as a wall. */}
        <div
          data-testid="routine-linear-events"
          className="grid grid-cols-[minmax(0,1fr)_repeat(3,4.5rem)] rounded-lg border border-border text-sm"
        >
          <span />
          {LINEAR_ACTIONS.map((a) => (
            <span key={a.action} className="py-1.5 text-center text-xs text-muted capitalize">
              {a.label}
            </span>
          ))}
          {LINEAR_TYPES.map((t) => (
            <Fragment key={t.type}>
              <span className="flex items-center border-t border-border py-1.5 pl-3 text-fg">{t.label}</span>
              {LINEAR_ACTIONS.map((a) => {
                const event = `${t.type}.${a.action}`;
                return (
                  <span key={event} className="flex items-center justify-center border-t border-border py-1.5">
                    <Checkbox
                      data-testid={`routine-event-${event}`}
                      aria-label={`${t.label} ${a.label}`}
                      checked={events.includes(event)}
                      onCheckedChange={(v) => toggle(event, v === true)}
                    />
                  </span>
                );
              })}
            </Fragment>
          ))}
        </div>
        <p data-testid="routine-linear-hint" className={cn("text-xs leading-relaxed", connections.isSuccess && !connected ? "text-warn" : "text-muted")}>
          {connections.isSuccess && !connected ? (
            <>
              The Linear app is not connected, so no events arrive yet. Connect it under{" "}
              <Link to="/org/settings/connections" className="underline underline-offset-2 hover:text-fg">
                Connections
              </Link>
              .
            </>
          ) : (
            "Each run is given the events that arrived since the last one. A run that is busy leaves them waiting for the next."
          )}
        </p>
      </div>
      <div className="grid gap-1.5">
        <label htmlFor={ids.teams} className="text-sm font-medium">
          Teams{" "}
          <span className="font-normal text-muted">(optional)</span>
        </label>
        <Input
          id={ids.teams}
          data-testid="routine-teams"
          value={teams}
          onChange={(e) => onTeams(e.target.value)}
          placeholder="ENG, WEB"
          aria-describedby={`${ids.teams}-hint`}
        />
        <p id={`${ids.teams}-hint`} className="text-xs text-muted">
          Team keys, separated by commas. Leave empty for every team.
        </p>
      </div>
      <ChatModeField modes={CHAT_MODES} chatMode={chatMode} onChatMode={onChatMode} />
    </>
  );
}

/**
 * ChatModeField is where a routine on events runs, among the modes its source offers: a
 * ticket's or pull request's conversation, or a chat of the routine's own.
 */
function ChatModeField({
  modes,
  chatMode,
  onChatMode,
}: {
  modes: { mode: ChatMode; label: string; hint: string; icon: LucideIcon }[];
  chatMode: ChatMode;
  onChatMode: (mode: ChatMode) => void;
}) {
  const id = useId();
  return (
    <div className="grid gap-1.5" role="group" aria-labelledby={id}>
      <span id={id} className="text-sm font-medium">
        Runs happen in
      </span>
      <ToggleGroup
        type="single"
        value={chatMode}
        aria-label="Runs happen in"
        onValueChange={(v) => v && onChatMode(v as ChatMode)}
        className="grid w-full grid-cols-2 border-border bg-raised"
      >
        {modes.map((m) => (
          <ToggleGroupItem
            key={m.mode}
            value={m.mode}
            data-testid={`routine-chat-mode-${m.mode}`}
            className="h-8 text-sm text-muted data-[state=on]:bg-panel data-[state=on]:font-semibold data-[state=on]:text-fg data-[state=on]:shadow-xs"
          >
            <m.icon aria-hidden />
            {m.label}
          </ToggleGroupItem>
        ))}
      </ToggleGroup>
      <p data-testid="routine-chat-mode-hint" className="text-xs leading-relaxed text-muted">
        {modes.find((m) => m.mode === chatMode)?.hint}
      </p>
    </div>
  );
}

/**
 * GitHubEventsField is what a routine on GitHub events runs on: the events, grouped as CI, pull
 * requests and issues, and optionally the repositories. The GitHub App's webhook delivers them,
 * so the field says when it is not set up — the routine saves, and runs once it is. Under them
 * is where its runs happen: the pull request's chat, by default, or a chat of its own.
 */
function GitHubEventsField({
  events,
  onEvents,
  repos,
  onRepos,
  chatMode,
  onChatMode,
}: {
  events: string[];
  onEvents: (events: string[]) => void;
  repos: string;
  onRepos: (repos: string) => void;
  chatMode: ChatMode;
  onChatMode: (mode: ChatMode) => void;
}) {
  const ids = { events: useId(), repos: useId() };
  const connections = useQuery({
    queryKey: ["agent", "connections"],
    queryFn: () => agent.getConnections({}),
    staleTime: 30_000,
  });
  const gh = connections.data?.github;
  // Deliveries arrive only once the App has a webhook secret and a listen address.
  const delivering = Boolean(gh?.configured && gh?.reviews);
  const toggle = (event: string, on: boolean) =>
    onEvents(GITHUB_EVENTS.map((e) => e.event).filter((e) => (e === event ? on : events.includes(e))));
  return (
    <>
      <div className="grid gap-1.5" role="group" aria-labelledby={ids.events}>
        <span id={ids.events} className="text-sm font-medium">
          GitHub events
        </span>
        {/* One table, as Linear's: CI by finished or only failed, then the rest one switch each. */}
        <div
          data-testid="routine-github-events"
          className="grid grid-cols-[minmax(0,1fr)_repeat(2,5.5rem)] overflow-hidden rounded-lg border border-border text-sm"
        >
          {GITHUB_GROUPS.map((group, i) => (
            // contents: the group is a group to a screen reader and rows of the one grid to the eye.
            <div key={group} role="group" aria-label={group} className="contents">
              <span
                aria-hidden
                className={cn("bg-raised py-1.5 pl-3 text-xs font-medium text-muted", i > 0 && "border-t border-border")}
              >
                {group}
              </span>
              {group === "CI" ? (
                <>
                  <span aria-hidden className="bg-raised py-1.5 text-center text-xs text-muted">
                    Finished
                  </span>
                  <span aria-hidden className="bg-raised py-1.5 text-center text-xs text-muted">
                    Only if failed
                  </span>
                </>
              ) : (
                <span className={cn("col-span-2 bg-raised", i > 0 && "border-t border-border")} />
              )}
              {group === "CI"
                ? GITHUB_CI.map((row) => {
                    const outcome = ciOutcome(events, row);
                    return (
                      <Fragment key={row.completed}>
                        <label
                          htmlFor={`${ids.events}-${row.completed}`}
                          className="grid gap-0.5 border-t border-border py-2 pl-3 text-fg"
                        >
                          {row.label}
                          <span className="text-xs text-muted">{row.hint}</span>
                        </label>
                        <span className="flex items-center justify-center border-t border-border">
                          <Checkbox
                            id={`${ids.events}-${row.completed}`}
                            data-testid={`routine-event-${row.completed}`}
                            aria-label={`${row.label} finished`}
                            checked={outcome !== "off"}
                            onCheckedChange={(v) => onEvents(withOutcome(events, row, v === true ? "finished" : "off"))}
                          />
                        </span>
                        <span className="flex items-center justify-center border-t border-border">
                          <Checkbox
                            data-testid={`routine-event-${row.failed}`}
                            aria-label={`${row.label} only if failed`}
                            checked={outcome === "failed"}
                            onCheckedChange={(v) => onEvents(withOutcome(events, row, v === true ? "failed" : "finished"))}
                          />
                        </span>
                      </Fragment>
                    );
                  })
                : GITHUB_EVENTS.filter((e) => e.group === group).map((e) => (
                    <Fragment key={e.event}>
                      <label htmlFor={`${ids.events}-${e.event}`} className="border-t border-border py-2 pl-3 text-fg">
                        {e.short ?? e.label}
                      </label>
                      <span className="flex items-center justify-center border-t border-border">
                        <Checkbox
                          id={`${ids.events}-${e.event}`}
                          data-testid={`routine-event-${e.event}`}
                          aria-label={e.label}
                          checked={events.includes(e.event)}
                          onCheckedChange={(v) => toggle(e.event, v === true)}
                        />
                      </span>
                      <span className="border-t border-border" />
                    </Fragment>
                  ))}
            </div>
          ))}
        </div>
        <p
          data-testid="routine-github-hint"
          className={cn("text-xs leading-relaxed", connections.isSuccess && !delivering ? "text-warn" : "text-muted")}
        >
          {connections.isSuccess && !delivering ? (
            <>
              The GitHub App’s webhook is not set up, so no events arrive yet. Set it up under{" "}
              <Link to="/org/settings/connections" className="underline underline-offset-2 hover:text-fg">
                Connections
              </Link>
              .
            </>
          ) : (
            "The App must be subscribed to the matching events in its GitHub settings: Workflow runs, Check suites, Pull requests, Pull request reviews or Issues. All checks finished needs Workflow runs or Check suites, and Statuses for CI that reports commit statuses. Each run is given the events that arrived since the last one."
          )}
        </p>
      </div>
      <div className="grid gap-1.5">
        <label htmlFor={ids.repos} className="text-sm font-medium">
          Repositories{" "}
          <span className="font-normal text-muted">(optional)</span>
        </label>
        <Input
          id={ids.repos}
          data-testid="routine-repos"
          value={repos}
          onChange={(e) => onRepos(e.target.value)}
          placeholder="acme/api, acme"
          aria-describedby={`${ids.repos}-hint`}
        />
        <p id={`${ids.repos}-hint`} className="text-xs text-muted">
          owner/name, or an owner for all of its repositories, separated by commas. Leave empty for every repository
          the App is installed on.
        </p>
      </div>
      <ChatModeField modes={GITHUB_CHAT_MODES} chatMode={chatMode} onChatMode={onChatMode} />
    </>
  );
}

type SectionKey = "instruction" | "trigger" | "identity" | "limits";

/** One part of the editor: its heading and a sentence on what it decides, then its fields. */
function Section({
  id,
  title,
  description,
  first,
  children,
}: {
  id: string;
  title: string;
  description: ReactNode;
  first?: boolean;
  children: ReactNode;
}) {
  return (
    <section
      id={id}
      aria-labelledby={`${id}-title`}
      className={cn("py-7", first ? "pt-6" : "border-t border-hairline")}
    >
      <div className="grid gap-1 pb-5">
        <h2 id={`${id}-title`} className="m-0 text-base font-bold text-fg">
          {title}
        </h2>
        <p className="m-0 max-w-[68ch] text-sm text-muted">{description}</p>
      </div>
      <div className="grid max-w-2xl gap-5">{children}</div>
    </section>
  );
}

/**
 * RoutineDialog is the one editor, for a new routine and for an existing one: a dialog of
 * sections with a menu beside them, as a sandbox provider's settings are. The server checks the schedule
 * — its cron, its timezone and the floor — and its refusal is shown as it says it.
 *
 * An admin making a routine chooses who it runs as: themselves, or Podium (a system routine).
 * The choice is made once; editing shows it and does not offer it again. Duplicating a routine
 * is how to have it run as somebody else: the copy starts on the original's choice, and an admin
 * may change it before saving.
 */
export function RoutineDialog({
  open,
  title,
  initial,
  lockAssistant,
  scope: fixedScope,
  defaultScope,
  canChooseScope,
  canChooseTrigger,
  onClose,
  onSave,
}: {
  open: boolean;
  title: string;
  initial: Partial<RoutineDraft>;
  lockAssistant?: boolean;
  /** scope is the routine's, for one that exists. */
  scope?: Scope;
  /** defaultScope is where "Runs as" starts for a new routine: a duplicate starts on its original's. */
  defaultScope?: Scope;
  /** canChooseScope offers "Runs as": an admin making a new routine. */
  canChooseScope?: boolean;
  /** canChooseTrigger offers the sources other than a schedule, which only admins may set. */
  canChooseTrigger?: boolean;
  onClose: () => void;
  onSave: (draft: RoutineDraft, scope: Scope) => Promise<void>;
}) {
  const brand = useBrand();
  const [scope, setScope] = useState<Scope>(fixedScope ?? defaultScope ?? "personal");
  const system = scope === "system";
  const [name, setName] = useState(initial.name ?? "");
  const [instruction, setInstruction] = useState(initial.instruction ?? "");
  const [notes, setNotes] = useState(initial.notes ?? "");
  const [schedule, setSchedule] = useState(
    initial.schedule || toCron(DEFAULT_REPEAT),
  );
  // Remounted by a template, so the schedule editor reads the schedule it was given afresh.
  const [scheduleKey, setScheduleKey] = useState(0);
  const [trigger, setTrigger] = useState<Trigger>(
    initial.trigger === "linear" || initial.trigger === "github" ? initial.trigger : "schedule",
  );
  const linear = trigger === "linear";
  const github = trigger === "github";
  // Each source keeps its own events and filter, so switching between them never carries one
  // source's events to the other, and switching back finds what was chosen.
  const [linearEvents, setLinearEvents] = useState<string[]>(initial.trigger === "linear" ? (initial.events ?? []) : []);
  const [teams, setTeams] = useState(initial.trigger === "linear" ? (initial.teams ?? []).join(", ") : "");
  const [githubEvents, setGitHubEvents] = useState<string[]>(initial.trigger === "github" ? (initial.events ?? []) : []);
  const [repos, setRepos] = useState(initial.trigger === "github" ? (initial.teams ?? []).join(", ") : "");
  const events = linear ? linearEvents : github ? githubEvents : [];
  const [chatMode, setChatMode] = useState<ChatMode>(
    initial.trigger === "linear" && initial.chatMode === "subject" ? "subject" : "routine",
  );
  // A routine on GitHub events contributes to the pull request's chat unless it was saved to
  // run in its own: that conversation is the context its reviews and CI share.
  const [githubChatMode, setGitHubChatMode] = useState<ChatMode>(
    initial.trigger === "github" && initial.chatMode === "routine" ? "routine" : "subject",
  );
  const [timezone, setTimezone] = useState(initial.timezone || browserZone());
  const [personalityId, setPersonalityId] = useState(
    initial.personalityId ?? "",
  );
  const [cap, setCap] = useState(
    initial.dailyCapUsd ? String(initial.dailyCapUsd) : "",
  );
  const [error, setError] = useState("");
  const [saving, setSaving] = useState(false);
  const ids = { assistant: useId(), zone: useId(), cap: useId(), scope: useId(), trigger: useId(), form: useId() };
  // The assistant is chosen when the routine is made: its chat is labelled with it for good.
  const voices = useQuery({
    queryKey: ["agent", "personalities"],
    queryFn: () => agent.listPersonalities({}),
    staleTime: 30_000,
    enabled: open && !lockAssistant && !system,
  });
  const assistants = voices.data?.personalities ?? [];
  const capValue = cap.trim() === "" ? 0 : Number(cap);
  const capProblem =
    cap.trim() !== "" && (!Number.isFinite(capValue) || capValue < 0)
      ? "A spend cap is an amount in dollars, like 5 or 2.50."
      : "";

  const useTemplate = () => {
    // On GitHub events the run is given each failed workflow run, so the template waits for them
    // instead of looking every few minutes.
    if (github) {
      setName(CI_FIXER_ON_GITHUB.name);
      setInstruction(CI_FIXER_ON_GITHUB.instruction);
      setNotes(CI_FIXER_ON_GITHUB.notes);
      setGitHubEvents(CI_FIXER_ON_GITHUB.events);
      return;
    }
    setName(CI_FIXER.name);
    setInstruction(CI_FIXER.instruction);
    setNotes(CI_FIXER.notes);
    setSchedule(CI_FIXER.schedule);
    setScheduleKey((k) => k + 1);
  };

  const save = async () => {
    setSaving(true);
    setError("");
    try {
      await onSave(
        {
          $typeName: "podium.agent.v1.RoutineDraft",
          name: name.trim(),
          instruction: instruction.trim(),
          notes: notes.trim(),
          // One or the other, never both: the server refuses a draft that names a schedule and events.
          schedule: linear || github ? "" : schedule.trim(),
          trigger,
          events,
          // teams is the filter of whichever source it is: Linear team keys, or GitHub repositories.
          teams: linear ? teamKeys(teams) : github ? repoNames(repos) : [],
          // Only a Linear run is about a ticket: the server refuses a ticket's conversation elsewhere.
          chatMode: linear ? chatMode : github ? githubChatMode : "routine",
          timezone: timezone.trim() || "UTC",
          personalityId: system
            ? ""
            : lockAssistant
              ? (initial.personalityId ?? "")
              : personalityId,
          model: initial.model ?? "",
          effort: initial.effort ?? "",
          repos: [],
          dailyCapUsd: capValue,
        },
        scope,
      );
    } catch (err) {
      setError(errorMessage(err));
    } finally {
      setSaving(false);
    }
  };

  const scopeHint = SCOPES.find((s) => s.scope === scope)?.hint ?? "";
  const blank = !name.trim() && !instruction.trim();


  const offerScope = Boolean(canChooseScope) && !fixedScope;
  const offerAssistant = !system && !lockAssistant && assistants.length > 0;
  const sid = (key: SectionKey) => `${ids.form}-${key}`;
  const sections: { key: SectionKey; title: string }[] = [
    { key: "instruction", title: "Instruction" },
    { key: "trigger", title: "Trigger" },
    ...(offerScope || offerAssistant ? [{ key: "identity" as const, title: "Runs as" }] : []),
    { key: "limits", title: "Limits" },
  ];
  const keys = sections.map((s) => s.key).join(" ");

  // The menu follows the reader: the lit section is the first one in view in the form pane.
  // The pane mounts a render after this component, once the dialog's portal is in the page, so
  // the observer waits on the element itself.
  const [pane, setPane] = useState<HTMLDivElement | null>(null);
  const [active, setActive] = useState<SectionKey>("instruction");
  // While a click on the menu scrolls the form, the menu keeps the section clicked.
  const jumping = useRef(false);
  useEffect(() => {
    if (!pane || typeof IntersectionObserver === "undefined") return;
    const seen = new Set<string>();
    const io = new IntersectionObserver(
      (entries) => {
        for (const en of entries) {
          if (en.isIntersecting) seen.add(en.target.id);
          else seen.delete(en.target.id);
        }
        if (jumping.current) return;
        const first = keys.split(" ").find((k) => seen.has(`${ids.form}-${k}`));
        if (first) setActive(first as SectionKey);
      },
      { root: pane, rootMargin: "0px 0px -55% 0px" },
    );
    for (const k of keys.split(" ")) {
      const el = document.getElementById(`${ids.form}-${k}`);
      if (el) io.observe(el);
    }
    let still: ReturnType<typeof setTimeout> | undefined;
    const onScroll = () => {
      if (!jumping.current) return;
      clearTimeout(still);
      still = setTimeout(() => (jumping.current = false), 150);
    };
    pane.addEventListener("scroll", onScroll, { passive: true });
    return () => {
      io.disconnect();
      pane.removeEventListener("scroll", onScroll);
      clearTimeout(still);
    };
  }, [pane, keys, ids.form]);
  const jump = (key: SectionKey) => {
    setActive(key);
    // Scroll the form pane only: scrollIntoView would also move the dialog around it.
    const el = document.getElementById(sid(key));
    if (!el || !pane) return;
    const top = Math.min(el.offsetTop, pane.scrollHeight - pane.clientHeight);
    if (Math.abs(pane.scrollTop - top) < 1) return;
    jumping.current = true;
    pane.scrollTo?.({ top, behavior: "smooth" });
  };

  // Why Save is off, beside it: a disabled button explains nothing on its own.
  const blocker = !name.trim()
    ? "Give it a name to save."
    : !instruction.trim()
      ? "Say what to do each run to save."
      : linear && events.length === 0
        ? "Choose at least one Linear event to save."
        : github && events.length === 0
          ? "Choose at least one GitHub event to save."
          : capProblem
          ? "Fix the spend cap to save."
          : "";
  // The menu marks a section whose choice is unfinished, not one nobody has reached yet: an
  // empty name on a new routine is said beside Save, and is not a mark on the menu.
  const troubled = new Set<SectionKey>();
  if ((linear || github) && events.length === 0) troubled.add("trigger");
  if (capProblem) troubled.add("limits");

  const timezoneField = (hint: string) => (
    <div className="grid gap-1.5">
      <label htmlFor={ids.zone} className="text-sm font-medium">
        Timezone
      </label>
      <TimezonePicker id={ids.zone} data-testid="routine-timezone" value={timezone} onChange={setTimezone} />
      <p className="text-xs text-muted">{hint}</p>
    </div>
  );

  return (
    <Dialog open={open} onOpenChange={(o) => !o && onClose()}>
      <DialogContent
        className="flex h-[min(780px,calc(100dvh-2rem))] max-w-[920px] flex-col gap-0 overflow-hidden p-0"
        // The name, not the menu: the first thing in the dialog is the menu, and nobody opens a
        // routine to press "Instruction".
        onOpenAutoFocus={(e) => {
          e.preventDefault();
          document.getElementById(`${ids.form}-name`)?.focus();
        }}
      >
        <DialogHeader className="border-b border-hairline px-6 pt-6 pb-5">
          <DialogTitle className="flex items-center gap-2 text-xl leading-tight font-bold tracking-normal">
            {title}
            {fixedScope === "system" ? <SystemBadge /> : null}
          </DialogTitle>
          <DialogDescription className="text-sm text-soft">
            {fixedScope === "system"
              ? "Runs as Podium with the company’s credentials, and acts in Linear as the Podium app. Nobody writes in its chat: what it does is steered from here."
              : "A job an assistant does in a chat of its own. A run that finds nothing stays quiet; one that does answers in that chat."}
          </DialogDescription>
        </DialogHeader>
        <form
          className="flex min-h-0 flex-1 flex-col"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (!blocker) void save();
          }}
        >
          <div className="grid min-h-0 flex-1 grid-rows-[minmax(0,1fr)] sm:grid-cols-[11rem_minmax(0,1fr)]">
            <nav aria-label="Routine sections" className="hidden border-r border-hairline bg-bg/60 p-3 sm:block">
              <ul className="m-0 grid list-none gap-0.5 p-0">
                {sections.map((s) => (
                  <li key={s.key}>
                    <button
                      type="button"
                      onClick={() => jump(s.key)}
                      aria-current={active === s.key ? "true" : undefined}
                      className={cn(
                        "flex w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left text-sm transition-colors",
                        "outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                        active === s.key ? "bg-raised font-bold text-fg" : "text-soft hover:bg-raised hover:text-fg",
                      )}
                    >
                      {s.title}
                      {troubled.has(s.key) ? (
                        <span className="size-1.5 shrink-0 rounded-full bg-warn" aria-label="needs a value" />
                      ) : null}
                    </button>
                  </li>
                ))}
              </ul>
            </nav>

            <div ref={setPane} className="relative min-h-0 overflow-y-auto overscroll-contain px-6 pb-6">
          <Section
            id={sid("instruction")}
            first
            title="Instruction"
            description="What one run is told. A run that finds nothing to do stays quiet; one that does answers in the routine’s chat."
          >
            {system && !fixedScope && blank ? (
              <button
                type="button"
                data-testid="routine-template-ci"
                onClick={useTemplate}
                className="group flex items-start gap-3 rounded-lg border border-dashed border-line-strong px-3 py-2.5 text-left text-sm outline-none transition-colors hover:border-muted hover:bg-raised/60 focus-visible:ring-2 focus-visible:ring-ring/50"
              >
                <Wrench className="mt-0.5 size-4 shrink-0 text-muted group-hover:text-fg" aria-hidden />
                <span className="grid gap-0.5">
                  <span className="font-medium text-fg">Start from “Fix failing CI”</span>
                  <span className="text-xs text-muted">
                    {github
                      ? "Runs on each failed workflow run and proposes a fix on its pull request, once per commit, giving up after 3 tries. It only proposes until you edit it to push."
                      : "Finds failed checks on open pull requests and proposes a fix on each, once per commit, giving up after 3 tries. It only proposes until you edit it to push."}
                  </span>
                </span>
              </button>
            ) : null}
            <label className="grid gap-1.5 text-sm font-medium">
              Name
              <Input
                id={`${ids.form}-name`}
                data-testid="routine-name"
                value={name}
                maxLength={80}
                onChange={(e) => setName(e.target.value)}
                placeholder={system ? "Fix failing CI" : "Morning PR digest"}
              />
            </label>
            <label className="grid gap-1.5 text-sm font-medium">
              What to do each run
              <Textarea
                data-testid="routine-instruction"
                value={instruction}
                onChange={(e) => setInstruction(e.target.value)}
                placeholder={
                  system
                    ? "Look for failed checks on open pull requests in the API and web repos, and comment on each with the fix you would make. Do not push."
                    : "Check acme/api for pull requests whose CI failed since the last run, and tell me which ones and why."
                }
                className="min-h-40"
              />
            </label>
            <label className="grid gap-1.5 text-sm font-medium">
              <span>
                Standing notes <span className="font-normal text-muted">(optional)</span>
              </span>
              <Textarea
                value={notes}
                onChange={(e) => setNotes(e.target.value)}
                placeholder="- skip draft pull requests"
                className="min-h-20"
              />
              <span className="text-xs font-normal text-muted">
                Rules on top of the instruction, one per line. Every run is given them.
              </span>
            </label>
          </Section>

          <Section
            id={sid("trigger")}
            title="Trigger"
            description={
              canChooseTrigger
                ? "What starts a run. A routine has one source."
                : "When it runs."
            }
          >
            {canChooseTrigger ? (
              <ToggleGroup
                type="single"
                value={trigger}
                aria-label="Runs"
                onValueChange={(v) => v && setTrigger(v as Trigger)}
                className="grid w-full items-stretch gap-2 border-0 bg-transparent p-0 md:grid-cols-3"
              >
                {TRIGGERS.map((t) => (
                  <ToggleGroupItem
                    key={t.trigger}
                    value={t.trigger}
                    data-testid={`routine-trigger-${t.trigger}`}
                    className="h-auto flex-col items-start justify-start gap-0.5 rounded-lg border border-border bg-panel px-3 py-2.5 text-left whitespace-normal data-[state=on]:border-accent data-[state=on]:bg-raised"
                  >
                    <span className="flex items-center gap-2 text-sm font-semibold text-fg">
                      <t.icon aria-hidden />
                      {t.label}
                    </span>
                    <span className="text-xs font-normal text-muted">{t.hint}</span>
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
            ) : null}
            {linear ? (
              <LinearEventsField
                events={linearEvents}
                onEvents={setLinearEvents}
                teams={teams}
                onTeams={setTeams}
                chatMode={chatMode}
                onChatMode={setChatMode}
              />
            ) : github ? (
              <GitHubEventsField
                events={githubEvents}
                onEvents={setGitHubEvents}
                repos={repos}
                onRepos={setRepos}
                chatMode={githubChatMode}
                onChatMode={setGitHubChatMode}
              />
            ) : (
              <>
                <ScheduleField key={scheduleKey} value={schedule} onChange={setSchedule} system={system} />
                {timezoneField("The schedule is read in this timezone, so 09:00 stays 09:00 across daylight saving.")}
              </>
            )}
          </Section>

          {offerScope || offerAssistant ? (
            <Section
              id={sid("identity")}
              title="Runs as"
              description="Whose credentials a run uses, and which assistant answers. Both are chosen once, when the routine is made."
            >
              {offerScope ? (
                <div className="grid gap-1.5" role="group" aria-labelledby={ids.scope}>
                  <span id={ids.scope} className="text-sm font-medium">
                    Runs as
                  </span>
                  <ToggleGroup
                    type="single"
                    value={scope}
                    aria-label="Runs as"
                    onValueChange={(v) => v && setScope(v as Scope)}
                    className="grid w-full grid-cols-2 border-border bg-raised"
                  >
                    {SCOPES.map((s) => (
                      <ToggleGroupItem
                        key={s.scope}
                        value={s.scope}
                        data-testid={`routine-scope-${s.scope}`}
                        className="h-8 text-sm text-muted data-[state=on]:bg-panel data-[state=on]:font-semibold data-[state=on]:text-fg data-[state=on]:shadow-xs"
                      >
                        {s.scope === "system" ? <ShieldCheck aria-hidden /> : <User aria-hidden />}
                        {s.scope === "system" ? `${brand} (system)` : s.label}
                      </ToggleGroupItem>
                    ))}
                  </ToggleGroup>
                  <p data-testid="routine-scope-hint" className="text-xs leading-relaxed text-muted">
                    {scopeHint}
                  </p>
                </div>
              ) : null}
              {offerAssistant ? (
                <div className="grid gap-1.5">
                  <label htmlFor={ids.assistant} className="text-sm font-medium">
                    Assistant
                  </label>
                  <Select
                    value={personalityId || DEFAULT_ASSISTANT}
                    onValueChange={(v) => setPersonalityId(v === DEFAULT_ASSISTANT ? "" : v)}
                  >
                    <SelectTrigger id={ids.assistant} data-testid="routine-assistant">
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={DEFAULT_ASSISTANT}>{brand}</SelectItem>
                      {assistants.map((p) => (
                        <SelectItem key={p.id} value={p.id}>
                          {p.displayName || p.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                </div>
              ) : null}
            </Section>
          ) : null}

          <Section id={sid("limits")} title="Limits" description="What the routine may spend in a day.">
            <div className="grid gap-1.5">
              <label htmlFor={ids.cap} className="text-sm font-medium">
                Daily spend cap <span className="font-normal text-muted">(optional)</span>
              </label>
              <div className="relative w-36">
                <span className="pointer-events-none absolute top-1/2 left-3 -translate-y-1/2 text-sm text-faint">$</span>
                <Input
                  id={ids.cap}
                  data-testid="routine-cap"
                  inputMode="decimal"
                  value={cap}
                  onChange={(e) => setCap(e.target.value)}
                  placeholder="No cap"
                  aria-invalid={capProblem !== ""}
                  aria-describedby={`${ids.cap}-hint`}
                  className="pl-7 tabular-nums"
                />
              </div>
              {/* A routine on events has no schedule to read in a timezone, so none is asked for: its
                  spend cap's day ends at midnight in the one it was made in, the browser's. */}
              <p id={`${ids.cap}-hint`} className={cn("text-xs", capProblem ? "text-warn" : "text-muted")}>
                {capProblem ||
                  `Reaching the cap pauses the routine until midnight in ${linear || github ? timezone || "UTC" : "its timezone"}, and it starts again on its own.`}
              </p>
            </div>
          </Section>
            </div>
          </div>

          <div className="flex flex-wrap items-center justify-end gap-2 border-t border-hairline px-6 py-3">
            {error ? (
              <p role="alert" data-testid="routine-error" className="mr-auto min-w-0 flex-1 text-sm text-err">
                {error}
              </p>
            ) : blocker ? (
              <p className="mr-auto text-xs text-muted">{blocker}</p>
            ) : null}
            <Button type="button" variant="outline" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" data-testid="routine-save" disabled={saving || blocker !== ""}>
              {saving ? "Saving…" : system && !fixedScope ? "Save system routine" : "Save routine"}
            </Button>
          </div>
        </form>
      </DialogContent>
    </Dialog>
  );
}
