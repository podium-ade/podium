import { useCallback, useEffect, useState, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Cpu, MessageSquare, Plug, Timer, UserRound } from "lucide-react";
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
} from "../../lib/assistantProfile";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { ConductorDown } from "./ConductorDown";
import { AgentPicker } from "./AgentPicker";
import { FormActions, SectionLead } from "./FormActions";
import { SettingsSectionNav, type SettingsSection } from "./SettingsSections";
import { Empty } from "../Empty";
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
      "Skills and MCP servers the assistant may call without starting a task. Leave a list empty and it delegates. A web chat uses the signed-in person's server of that name. Slack uses the company server.",
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
      <fieldset disabled={readOnly} className="m-0 min-w-0 border-0 p-0">
      {sentence ? <SectionLead>{sentence}</SectionLead> : null}

      <div hidden={section !== "identity"}>
        <Row id="assistant-display-name" label="Display name" hint="What it calls itself in a chat header and in Slack.">
          <Input
            id="assistant-display-name"
            value={draft.displayName}
            onChange={(e) => onChange({ ...draft, displayName: e.target.value })}
            className="h-8 max-w-sm"
            autoComplete="off"
            disabled={readOnly}
          />
          {issues.has("display-name") ? <Issue>The assistant needs a display name.</Issue> : null}
        </Row>
        <Row label="Git" hint="Both a name and an email, or neither. GitHub matches the email to an account.">
          <div className="grid gap-3 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor="assistant-git-name">Name</Label>
              <Input
                id="assistant-git-name"
                value={draft.gitName}
                onChange={(e) => onChange({ ...draft, gitName: e.target.value })}
                className="h-8"
                autoComplete="off"
                disabled={readOnly}
              />
            </div>
            <div className="space-y-1.5">
              <Label htmlFor="assistant-git-email">Email</Label>
              <Input
                id="assistant-git-email"
                value={draft.gitEmail}
                onChange={(e) => onChange({ ...draft, gitEmail: e.target.value })}
                className="h-8"
                autoComplete="off"
                inputMode="email"
                spellCheck={false}
                disabled={readOnly}
              />
            </div>
          </div>
          {issues.has("git") ? <Issue>{gitIssue(draft) ?? ""}</Issue> : null}
        </Row>
      </div>

      <div hidden={section !== "instructions"}>
        <Row id="assistant-prompt" label="System prompt">
          <Textarea
            id="assistant-prompt"
            value={draft.systemPrompt}
            onChange={(e) => onChange({ ...draft, systemPrompt: e.target.value })}
            className="min-h-72"
            spellCheck
            disabled={readOnly}
          />
          {issues.has("prompt") ? <Issue>The assistant needs a prompt.</Issue> : null}
          {issues.has("prompt-size") ? <Issue>The prompt is over 32 KiB. A turn could not carry it.</Issue> : null}
        </Row>
      </div>

      <div hidden={section !== "model"}>
        <Row label="Model">
          <AgentPicker
            label="Assistant"
            agents={agents}
            value={{ agent: draft.agent, model: draft.model, effort: draft.effort }}
            onChange={(next) => onChange({ ...draft, agent: next.agent, model: next.model, effort: next.effort })}
            disabled={readOnly}
          />
          {issues.has("model") ? <Issue>Choose a model.</Issue> : null}
        </Row>
      </div>

      <div hidden={section !== "reach"} className="space-y-8">
        <Checklist
          idPrefix="skill"
          label="Skills"
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
      </div>

      <div hidden={section !== "limits"}>
        <Row label="Step cap" hint="Off unless you set one.">
          <div role="radiogroup" aria-label="Step cap" className="flex flex-wrap items-center gap-x-5 gap-y-2">
            <label className="flex items-center gap-2 text-sm text-fg">
              <input
                type="radio"
                name="assistant-max-turns"
                className="accent-accent"
                checked={draft.maxTurns === 0}
                onChange={() => onChange({ ...draft, maxTurns: 0 })}
              />
              No cap
            </label>
            <label className="flex items-center gap-2 text-sm text-fg">
              <input
                type="radio"
                name="assistant-max-turns"
                className="accent-accent"
                checked={draft.maxTurns !== 0}
                onChange={() => onChange({ ...draft, maxTurns: draft.maxTurns > 0 ? draft.maxTurns : 50 })}
              />
              Cap
            </label>
            {draft.maxTurns !== 0 ? (
              <Input
                type="number"
                min={1}
                aria-label="Turn cap"
                className="h-8 w-24 tabular-nums"
                value={draft.maxTurns < 0 ? "" : draft.maxTurns}
                onChange={(e) => {
                  const raw = e.target.value;
                  onChange({ ...draft, maxTurns: raw === "" ? -1 : Number(raw) });
                }}
              />
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
                  variant={on ? "subtle" : "outline"}
                  aria-pressed={on}
                  disabled={readOnly}
                  onClick={() => onChange({ ...draft, timeout: t.id })}
                >
                  {t.label}
                </Button>
              );
            })}
          </div>
          <Input
            id="assistant-timeout"
            value={draft.timeout}
            onChange={(e) => onChange({ ...draft, timeout: e.target.value })}
            className="h-8 max-w-xs font-mono"
            spellCheck={false}
            disabled={readOnly}
          />
          {issues.has("timeout") ? <Issue>Use a duration such as 15m. The clock stays on.</Issue> : null}
        </Row>
      </div>
      </fieldset>

      {readOnly ? null : (
        <FormActions>
          <Button type="submit" size="sm" disabled={!ready || saving}>
            {saving ? "Saving…" : "Save"}
          </Button>
        </FormActions>
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
  const nav: SettingsSection[] = SECTIONS.map((s) => ({
    id: s.id,
    label: s.label,
    icon: s.icon,
    unsaved: readOnly ? false : dirty.includes(s.id),
  }));

  if (down) {
    return (
      <ConductorDown
        what="The assistant could not be read"
        onRetry={() => void profile.refetch()}
        retrying={profile.isFetching}
      />
    );
  }

  return (
    <div className="flex flex-col gap-6 sm:flex-row sm:items-start sm:gap-8">
      <SettingsSectionNav
        label="Assistant"
        sections={nav}
        current={section}
        href={(id) => assistantSectionHref(id as AssistantSection)}
      />
      <div className="min-w-0 max-w-3xl flex-1">
        {readOnly ? (
          <p className="mb-4 text-sm leading-relaxed text-muted">Owners set this assistant.</p>
        ) : null}
        {failed ? <Empty title="Could not read the assistant" hint={errorMessage(profile.error)} /> : null}
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
      </div>
    </div>
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
    <div aria-busy="true" className="space-y-4 pt-2">
      <Skeleton className="h-4 w-72" />
      <Skeleton className="h-9 w-full max-w-sm" />
      <Skeleton className="h-9 w-full max-w-sm" />
    </div>
  );
}

function Row({ id, label, hint, children }: { id?: string; label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="grid gap-x-8 gap-y-2 border-t border-border py-5 sm:grid-cols-[12rem_minmax(0,1fr)]">
      <div className="space-y-1">
        {id ? (
          <Label htmlFor={id}>{label}</Label>
        ) : (
          <p className="text-xs font-medium text-muted">{label}</p>
        )}
        {hint ? <p className="text-xs leading-relaxed text-muted">{hint}</p> : null}
      </div>
      <div className="min-w-0 space-y-2">{children}</div>
    </div>
  );
}

function Issue({ children }: { children: string }) {
  return <p className="text-xs text-err">{children}</p>;
}

function Checklist({
  idPrefix,
  label,
  items,
  selected,
  note,
  empty,
  onToggle,
  disabled = false,
}: {
  idPrefix: string;
  label: string;
  items: ReachChoice[];
  selected: string[];
  note?: string;
  empty: ReactNode;
  onToggle: (name: string, on: boolean) => void;
  disabled?: boolean;
}) {
  const known = new Set(items.map((item) => item.name));
  const unknown = selected.filter((name) => !known.has(name));
  return (
    <section className="space-y-3">
      <h2 className="text-sm font-medium text-fg">{label}</h2>
      {note ? <p className="text-xs text-err">{note}</p> : null}
      {!note && items.length === 0 && unknown.length === 0 ? <p className="text-sm leading-relaxed text-muted">{empty}</p> : null}
      {items.length > 0 || unknown.length > 0 ? (
        <ul className="divide-y divide-border border-y border-border">
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
                onToggle={onToggle}
                disabled={disabled}
              />
            </li>
          ))}
        </ul>
      ) : null}
    </section>
  );
}

function CheckRow({
  idPrefix,
  name,
  checked,
  detail,
  onToggle,
  disabled = false,
}: {
  idPrefix: string;
  name: string;
  checked: boolean;
  detail?: string;
  onToggle: (name: string, on: boolean) => void;
  disabled?: boolean;
}) {
  const id = `assistant-${idPrefix}-${name}`;
  return (
    <label htmlFor={id} className="flex cursor-pointer items-start gap-3 py-2.5">
      <Checkbox
        id={id}
        checked={checked}
        disabled={disabled}
        onCheckedChange={(value) => onToggle(name, value === true)}
        className="mt-0.5"
      />
      <span className="min-w-0">
        <span className="block font-mono text-sm text-fg">{name}</span>
        {detail ? <span className="block text-xs leading-relaxed text-muted">{detail}</span> : null}
      </span>
    </label>
  );
}

function toggleName(list: string[], name: string, on: boolean): string[] {
  if (on) return list.includes(name) ? list : [...list, name];
  return list.filter((item) => item !== name);
}
