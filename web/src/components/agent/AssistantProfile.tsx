import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Cpu, Lock, MessageSquare, MessageSquarePlus, Plug, Timer, UserRound } from "lucide-react";
import { Link } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { AgentBackend } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import {
  assistantSectionHref,
  canSave,
  dirtySections,
  draftFromProfile,
  draftIssues,
  durationsEqual,
  gitIssue,
  updateFromDraft,
  viewFromProfile,
  type AssistantDraft,
  type AssistantSection,
  type ProfileUpdate,
  MAX_PROMPT_BYTES,
  listSections,
} from "../../lib/assistantProfile";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { cn } from "../../lib/utils";
import { ConductorDown } from "./ConductorDown";
import { AgentPicker } from "./AgentPicker";
import {
  AssistantPanel,
  Issue,
  PanelBody,
  Row,
  SaveBar,
  ScopeBar,
  SectionTabs,
  type AssistantTab,
} from "./AssistantPanel";
import { BackendMark } from "./BackendMark";
import { Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageFrame } from "../PageHeader";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { Checkbox } from "../ui/checkbox";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Textarea } from "../ui/textarea";

const SECTIONS: { id: AssistantSection; label: string; icon: LucideIcon; sentence: string }[] = [
  {
    id: "identity",
    label: "Identity",
    icon: UserRound,
    sentence: "What the assistant is called in chat and in Slack, and who its commits are by when a playbook names nobody.",
  },
  {
    id: "instructions",
    label: "Instructions",
    icon: MessageSquare,
    sentence: "The text a conversation is told. A playbook adds its own on top of this when the work needs a task.",
  },
  {
    id: "model",
    label: "Model",
    icon: Cpu,
    sentence: "What the assistant answers on, and what a playbook runs on unless it names its own.",
  },
  {
    id: "reach",
    label: "Reach",
    icon: Plug,
    sentence:
      "What Podium can use itself while it answers a chat, without starting a task. Anything not ticked here is only reachable by starting a playbook that names it.",
  },
  {
    id: "limits",
    label: "Limits",
    icon: Timer,
    sentence:
      "A turn always stops on the clock. The step cap is off unless you set one, because a cap that fires mid-answer ends a turn that was still working.",
  },
];

const TIMEOUTS = [
  { id: "5m", label: "5 min" },
  { id: "15m", label: "15 min" },
  { id: "30m", label: "30 min" },
  { id: "1h", label: "1 hour" },
  { id: "2h", label: "2 hours" },
] as const;

const textLink =
  "rounded-sm font-medium text-fg underline decoration-border underline-offset-4 outline-none hover:decoration-fg focus-visible:ring-2 focus-visible:ring-ring";

type ReachChoice = { name: string; detail?: string };

export type AssistantFormProps = {
  draft: AssistantDraft;
  saved: AssistantDraft;
  section: AssistantSection;
  agents: AgentBackend[];
  skills: ReachChoice[];
  servers: ReachChoice[];
  skillsNote?: string;
  serversNote?: string;
  saving: boolean;
  /** Members can read Podium. Every control is disabled and Save is absent. */
  readOnly?: boolean;
  onChange: (next: AssistantDraft) => void;
  onSave: (update: ProfileUpdate) => void;
};

/** AssistantForm edits the active assistant, one section at a time. Save writes the whole definition. */
export function AssistantForm({
  draft,
  saved,
  section,
  agents,
  skills,
  servers,
  skillsNote,
  serversNote,
  saving,
  readOnly = false,
  onChange,
  onSave,
}: AssistantFormProps) {
  const issues = new Set(draftIssues(draft));
  const ready = canSave(draft, saved);
  const sentence = SECTIONS.find((s) => s.id === section)?.sentence;
  const dirty = dirtySections(draft, saved);
  const commit = () => {
    if (!ready || saving) return;
    onSave(updateFromDraft(draft));
  };

  return (
    <form
      id="assistant-profile"
      data-testid="profile-card"
      noValidate
      onSubmit={(e) => {
        e.preventDefault();
        commit();
      }}
    >
      {sentence ? (
        <ScopeBar
          aside={
            readOnly ? (
              <Chip>
                <Lock aria-hidden className="size-3" />
                Owners set this assistant.
              </Chip>
            ) : undefined
          }
        >
          {sentence}
        </ScopeBar>
      ) : null}

      <fieldset disabled={readOnly} className="m-0 min-w-0 border-0 p-0">
        <PanelBody hidden={section !== "identity"}>
          <Row id="assistant-display-name" label="Display name" hint="What it calls itself in a chat header and in Slack.">
            <Input
              id="assistant-display-name"
              value={draft.displayName}
              onChange={(e) => onChange({ ...draft, displayName: e.target.value })}
              className="max-w-md"
              autoComplete="off"
              disabled={readOnly}
            />
            {issues.has("display-name") ? <Issue>The assistant needs a display name.</Issue> : null}
          </Row>
          <Row label="Git author" hint="Both a name and an email, or neither. GitHub matches the email to an account.">
            <div className="grid max-w-2xl gap-4 sm:grid-cols-2">
              <div className="space-y-1.5">
                <Label htmlFor="assistant-git-name" className="text-sm font-semibold text-soft">
                  Name
                </Label>
                <Input
                  id="assistant-git-name"
                  value={draft.gitName}
                  onChange={(e) => onChange({ ...draft, gitName: e.target.value })}
                  autoComplete="off"
                  disabled={readOnly}
                />
              </div>
              <div className="space-y-1.5">
                <Label htmlFor="assistant-git-email" className="text-sm font-semibold text-soft">
                  Email
                </Label>
                <Input
                  id="assistant-git-email"
                  value={draft.gitEmail}
                  onChange={(e) => onChange({ ...draft, gitEmail: e.target.value })}
                  autoComplete="off"
                  inputMode="email"
                  spellCheck={false}
                  disabled={readOnly}
                />
              </div>
            </div>
            {issues.has("git") ? <Issue>{gitIssue(draft) ?? ""}</Issue> : null}
          </Row>
        </PanelBody>

        <PanelBody hidden={section !== "instructions"}>
          <Row
            id="assistant-prompt"
            label="System prompt"
            hint={<PromptSize text={draft.systemPrompt} limit={MAX_PROMPT_BYTES} />}
          >
            <Textarea
              id="assistant-prompt"
              value={draft.systemPrompt}
              onChange={(e) => onChange({ ...draft, systemPrompt: e.target.value })}
              className="min-h-96 font-mono text-sm leading-relaxed"
              spellCheck
              disabled={readOnly}
            />
            {issues.has("prompt") ? <Issue>The assistant needs a prompt.</Issue> : null}
            {issues.has("prompt-size") ? <Issue>The prompt is over 32 KiB. A turn could not carry it.</Issue> : null}
          </Row>
        </PanelBody>

        <PanelBody hidden={section !== "model"}>
          <Row label="Model" hint="Chat can still pick another for one conversation.">
            <div className="max-w-md">
              <AgentPicker
                label="Assistant"
                agents={agents}
                value={{ agent: draft.agent, model: draft.model, effort: draft.effort }}
                onChange={(next) => onChange({ ...draft, agent: next.agent, model: next.model, effort: next.effort })}
                disabled={readOnly}
              />
            </div>
            {issues.has("model") ? <Issue>Choose a model.</Issue> : null}
          </Row>
        </PanelBody>

        <PanelBody hidden={section !== "reach"}>
          <Checklist
            idPrefix="skill"
            label="Skills"
            hint="Ticked skills load into every chat turn, so Podium can follow them in its own answer."
            none="No skills load into a chat. Work that needs one goes to a playbook that names it."
            some={(n, total) => `${n} of ${total} load into every chat turn.`}
            items={skills}
            selected={draft.skills}
            note={skillsNote}
            empty={
              <>
                No skills are installed. Add one on{" "}
                <Link to="/agent/skills" className={textLink}>
                  Skills
                </Link>
                , then name it here.
              </>
            }
            onToggle={(item, on) => onChange({ ...draft, skills: toggleName(draft.skills, item, on) })}
            disabled={readOnly}
          />
          <Checklist
            idPrefix="mcp"
            label="MCP servers"
            hint="Ticked servers connect to every chat turn. A web chat uses the signed-in person's server of that name if they have one. Slack uses the company's."
            none="No MCP servers connect to a chat. Work that needs one goes to a playbook that names it."
            some={(n, total) => `${n} of ${total} connect to every chat turn.`}
            items={servers}
            selected={draft.mcpServers}
            note={serversNote}
            empty={
              <>
                No MCP servers are registered. Add one on{" "}
                <Link to="/agent/mcp" className={textLink}>
                  MCP
                </Link>
                , then name it here.
              </>
            }
            onToggle={(item, on) => onChange({ ...draft, mcpServers: toggleName(draft.mcpServers, item, on) })}
            disabled={readOnly}
          />
        </PanelBody>

        <PanelBody hidden={section !== "limits"}>
          <Row label="Step cap" hint="Off unless you set one.">
            <div role="radiogroup" aria-label="Step cap" className="flex flex-wrap items-center gap-2.5">
              <RadioCard
                name="assistant-max-turns"
                checked={draft.maxTurns === 0}
                onChange={() => onChange({ ...draft, maxTurns: 0 })}
              >
                No cap
              </RadioCard>
              <RadioCard
                name="assistant-max-turns"
                checked={draft.maxTurns !== 0}
                onChange={() => onChange({ ...draft, maxTurns: draft.maxTurns > 0 ? draft.maxTurns : 50 })}
              >
                Cap
              </RadioCard>
              {draft.maxTurns !== 0 ? (
                <div className="flex items-center gap-2">
                  <Input
                    type="number"
                    min={1}
                    aria-label="Turn cap"
                    className="w-24 tabular-nums"
                    value={draft.maxTurns < 0 ? "" : draft.maxTurns}
                    onChange={(e) => {
                      const raw = e.target.value;
                      onChange({ ...draft, maxTurns: raw === "" ? -1 : Number(raw) });
                    }}
                  />
                  <span className="text-sm text-muted">steps</span>
                </div>
              ) : null}
            </div>
            {issues.has("cap") ? <Issue>A cap is at least 1. Choose No cap to leave it off.</Issue> : null}
          </Row>
          <Row id="assistant-timeout" label="Timeout" hint="How long one turn may run. It cannot be turned off.">
            <div className="flex flex-wrap gap-2" role="group" aria-label="Timeout presets">
              {TIMEOUTS.map((t) => {
                const on = durationsEqual(draft.timeout, t.id);
                return (
                  <Button
                    key={t.id}
                    type="button"
                    size="sm"
                    variant="outline"
                    aria-pressed={on}
                    disabled={readOnly}
                    className={cn("tabular-nums", on && "border-accent bg-run-bg text-fg shadow-[inset_0_0_0_1px_var(--color-accent)] hover:bg-run-bg")}
                    onClick={() => onChange({ ...draft, timeout: t.id })}
                  >
                    {t.label}
                  </Button>
                );
              })}
            </div>
            <div className="flex items-center gap-2">
              <Input
                id="assistant-timeout"
                value={draft.timeout}
                onChange={(e) => onChange({ ...draft, timeout: e.target.value })}
                className="max-w-40 font-mono"
                spellCheck={false}
                disabled={readOnly}
              />
              <span className="text-sm text-muted">or type your own, such as 45m or 1h30m</span>
            </div>
            {issues.has("timeout") ? <Issue>Use minutes or hours, such as 15m or 1h30m. The clock stays on.</Issue> : null}
          </Row>
        </PanelBody>
      </fieldset>

      {readOnly ? null : (
        <SaveBar
          dirty={dirty.length > 0}
          status={
            dirty.length > 0
              ? `Unsaved changes in ${listSections(dirty.map((id) => SECTIONS.find((s) => s.id === id)?.label ?? id))}`
              : "Saved. Every section is in use on the next turn."
          }
        >
          <Button type="submit" disabled={!ready || saving}>
            {saving ? "Saving…" : "Save"}
          </Button>
        </SaveBar>
      )}
    </form>
  );
}

/**
 * PodiumEditor is the built-in assistant, one section at a time. Save writes the
 * whole definition. Members see the same sections with every control disabled.
 */
export function PodiumEditor({ section, readOnly }: { section: AssistantSection; readOnly: boolean }) {
  const qc = useQueryClient();
  const toast = useToast();
  const { agents, defaultAgent } = useAgents();
  const profile = useQuery({
    queryKey: ["agent", "profile"],
    queryFn: () => agent.getProfile({}),
    staleTime: 30_000,
  });
  const skillsQ = useQuery({
    queryKey: ["agent", "skills"],
    queryFn: () => agent.listSkills({}),
    staleTime: 30_000,
  });
  const mcpQ = useQuery({
    queryKey: ["agent", "mcp"],
    queryFn: () => agent.listMcpServers({}),
    staleTime: 30_000,
  });
  const save = useMutation({
    mutationFn: (fields: ProfileUpdate) => agent.updateProfile(fields),
    onSuccess: async () => {
      toast("Saved. The next turn uses it.", "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "profile"] });
    },
    onError: (err) => toast(errorMessage(err)),
  });
  const [dirty, setDirty] = useState<AssistantSection[]>([]);
  const onDirty = useCallback((next: AssistantSection[]) => {
    setDirty((prev) => (prev.length === next.length && prev.every((id, i) => id === next[i]) ? prev : next));
  }, []);

  const raw = profile.data?.profile;
  const view = raw ? viewFromProfile(raw) : null;
  const initial = view ? draftFromProfile(view, defaultAgent) : null;
  const down = isAgentUnreachable(profile.error);
  const failed = profile.isError && !down;
  const tabs: AssistantTab[] = SECTIONS.map((s) => ({
    id: s.id,
    label: s.label,
    icon: s.icon,
    unsaved: readOnly ? false : dirty.includes(s.id),
  }));

  const frame = {
    title: initial?.displayName || "Podium",
    back: { to: "/agent/assistants", label: "Assistants" },
    meta: <Chip>Built-in</Chip>,
    actions: (
      <Button variant="outline" size="sm" asChild>
        <Link to="/agent/chat" aria-label="Talk to it">
          <MessageSquarePlus />
          <span className="hidden sm:inline">Talk to it</span>
        </Link>
      </Button>
    ),
  };

  if (down) {
    return (
      <PageFrame {...frame}>
        <ConductorDown
          what="The assistant could not be read"
          onRetry={() => void profile.refetch()}
          retrying={profile.isFetching}
        />
      </PageFrame>
    );
  }

  return (
    <PageFrame {...frame}>
      <AssistantPanel label={frame.title}>
        <SectionTabs tabs={tabs} current={section} href={(id) => assistantSectionHref(id as AssistantSection)} />
        {failed ? (
          <div className="p-6">
            <Empty title="Could not read the assistant" hint={errorMessage(profile.error)} />
          </div>
        ) : null}
        {!failed && !initial ? <ProfileSkeleton /> : null}
        {initial && view ? (
          <DraftEditor
            key={`${defaultAgent}:${JSON.stringify(initial)}`}
            initial={initial}
            section={section}
            agents={agents}
            skills={(skillsQ.data?.skills ?? []).map(skillChoice)}
            servers={(mcpQ.data?.servers ?? []).map(serverChoice)}
            skillsNote={skillsQ.isError ? errorMessage(skillsQ.error) : undefined}
            serversNote={mcpQ.isError ? errorMessage(mcpQ.error) : undefined}
            saving={save.isPending}
            readOnly={readOnly}
            onSave={(update) => save.mutate(update)}
            onDirty={onDirty}
          />
        ) : null}
      </AssistantPanel>
    </PageFrame>
  );
}

/** ModelLine is what an assistant answers on: the backend's mark and the model, as data. */
export function ModelLine({ agentId, model, fallback }: { agentId: string; model: string; fallback?: string }) {
  if (!model && !agentId) {
    return <span className="truncate">{fallback ?? "No model chosen"}</span>;
  }
  return (
    <>
      <BackendMark id={agentId || "claude"} className="size-3.5 text-soft" />
      <span className="truncate font-mono text-[13px] text-soft">{model || agentId}</span>
    </>
  );
}

/** PromptSize is how much of the 32 KiB a turn can carry this prompt already uses. */
function PromptSize({ text, limit }: { text: string; limit: number }) {
  const bytes = new TextEncoder().encode(text.trim()).length;
  const over = bytes > limit;
  return (
    <>
      The text every conversation starts from.{" "}
      <span className={cn("tabular-nums", over ? "font-semibold text-err" : undefined)}>
        {(bytes / 1024).toFixed(1)} of {limit / 1024} KiB.
      </span>
    </>
  );
}

function RadioCard({
  name,
  checked,
  onChange,
  children,
}: {
  name: string;
  checked: boolean;
  onChange: () => void;
  children: string;
}) {
  return (
    <label
      className={cn(
        "flex min-h-9 cursor-pointer items-center gap-2.5 rounded-md border px-3.5 text-sm font-semibold transition-colors duration-150",
        "has-[:focus-visible]:ring-2 has-[:focus-visible]:ring-ring has-[:disabled]:cursor-not-allowed has-[:disabled]:opacity-60",
        checked ? "border-accent bg-run-bg text-fg shadow-[inset_0_0_0_1px_var(--color-accent)]" : "border-line-strong bg-panel text-soft hover:bg-raised",
      )}
    >
      <input type="radio" name={name} className="size-4 accent-accent" checked={checked} onChange={onChange} />
      {children}
    </label>
  );
}

function DraftEditor({
  initial,
  onDirty,
  onSave,
  ...rest
}: Omit<AssistantFormProps, "draft" | "saved" | "onChange"> & {
  initial: AssistantDraft;
  onDirty: (sections: AssistantSection[]) => void;
}) {
  const [draft, setDraft] = useState(initial);
  useEffect(() => {
    onDirty(dirtySections(draft, initial));
  }, [draft, initial, onDirty]);
  return <AssistantForm {...rest} draft={draft} saved={initial} onChange={setDraft} onSave={onSave} />;
}

function skillChoice(skill: { name: string; enabled: boolean; problem: string }): ReachChoice {
  const parts: string[] = [];
  if (!skill.enabled) parts.push("Turned off. A turn that names it fails.");
  if (skill.problem) parts.push(skill.problem);
  return { name: skill.name, detail: parts.join(" ") || undefined };
}

function serverChoice(server: { name: string; enabled: boolean; owner: string }): ReachChoice {
  const parts: string[] = [];
  if (server.owner) parts.push("Yours");
  if (!server.enabled) parts.push("Turned off. A turn that names it fails.");
  return { name: server.name, detail: parts.join(". ") || undefined };
}

function ProfileSkeleton() {
  return (
    <div aria-busy="true" className="space-y-4 p-6">
      <Skeleton className="h-4 w-96 max-w-full" />
      <Skeleton className="h-11 w-full max-w-md" />
      <Skeleton className="h-11 w-full max-w-md" />
    </div>
  );
}

function Checklist({
  idPrefix,
  label,
  hint,
  none,
  some,
  items,
  selected,
  note,
  empty,
  onToggle,
  disabled = false,
}: {
  idPrefix: string;
  label: string;
  hint: string;
  /** What a turn does when nothing is ticked, said plainly. */
  none: string;
  some: (n: number, total: number) => string;
  items: ReachChoice[];
  selected: string[];
  note?: string;
  empty: ReactNode;
  onToggle: (name: string, on: boolean) => void;
  disabled?: boolean;
}) {
  const known = new Set(items.map((item) => item.name));
  const unknown = selected.filter((name) => !known.has(name));
  const total = items.length + unknown.length;
  return (
    <Row
      label={label}
      hint={hint}
    >
      {note ? <Issue>{note}</Issue> : null}
      {!note && total === 0 ? (
        <p className="m-0 rounded-md border border-dashed border-line-strong px-4 py-5 text-sm leading-relaxed text-soft">{empty}</p>
      ) : null}
      {total > 0 ? (
        <ul className="max-w-2xl divide-y divide-border overflow-hidden rounded-md border border-border">
          {items.map((item) => (
            <li key={item.name}>
              <CheckRow
                idPrefix={idPrefix}
                name={item.name}
                checked={selected.includes(item.name)}
                detail={item.detail}
                onToggle={onToggle}
                disabled={disabled}
              />
            </li>
          ))}
          {unknown.map((name) => (
            <li key={name}>
              <CheckRow
                idPrefix={idPrefix}
                name={name}
                checked
                detail="Not installed. A turn that names it fails."
                warn
                onToggle={onToggle}
                disabled={disabled}
              />
            </li>
          ))}
        </ul>
      ) : null}
      {total > 0 ? (
        <p
          aria-live="polite"
          className={cn(
            "m-0 max-w-2xl text-sm tabular-nums",
            selected.length === 0 ? "text-muted" : "font-semibold text-fg",
          )}
        >
          {selected.length === 0 ? none : some(selected.length, total)}
        </p>
      ) : null}
    </Row>
  );
}

function CheckRow({
  idPrefix,
  name,
  checked,
  detail,
  warn = false,
  onToggle,
  disabled = false,
}: {
  idPrefix: string;
  name: string;
  checked: boolean;
  detail?: string;
  warn?: boolean;
  onToggle: (name: string, on: boolean) => void;
  disabled?: boolean;
}) {
  const id = `assistant-${idPrefix}-${name}`;
  return (
    <label
      htmlFor={id}
      className={cn(
        "flex items-start gap-3 px-4 py-3 transition-colors duration-150",
        disabled ? "cursor-not-allowed" : "cursor-pointer hover:bg-raised",
        checked && "bg-run-bg/40",
      )}
    >
      <Checkbox
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={(value) => onToggle(name, value === true)}
        className="mt-0.5"
      />
      <span className="min-w-0">
        <span className="block font-mono text-sm font-semibold text-fg">{name}</span>
        {detail ? (
          <span className={cn("block text-sm leading-relaxed", warn ? "font-semibold text-warn" : "text-muted")}>{detail}</span>
        ) : null}
      </span>
    </label>
  );
}

function toggleName(list: string[], name: string, on: boolean): string[] {
  if (on) return list.includes(name) ? list : [...list, name];
  return list.filter((item) => item !== name);
}
