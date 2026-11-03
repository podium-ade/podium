import { useId, useState, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Cpu, MessageSquare, MessageSquarePlus, Plus, Trash2, UserRound } from "lucide-react";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Personality } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import {
  assistantSection,
  draftFromProfile,
  viewFromProfile,
  type AssistantSection,
  listSections,
} from "../../lib/assistantProfile";
import type { AgentChoice } from "../../lib/agents";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { useViewer } from "../../lib/identity";
import {
  canSavePersonality,
  cleanDisplay,
  comparePersonalities,
  emptyPersonality,
  personalityIssues,
  personalityModelDirty,
  type PersonalityDraft,
} from "../../lib/personality";
import { canManageInfra } from "../../lib/rbac";
import { cn } from "../../lib/utils";
import { AgentPicker } from "./AgentPicker";
import { ModelLine, PodiumEditor } from "./AssistantProfile";
import {
  AssistantPanel,
  AssistantTile,
  Issue,
  PanelBody,
  PanelHeader,
  Row,
  SaveBar,
  ScopeBar,
  SectionTabs,
  type AssistantTab,
} from "./AssistantPanel";
import { ConductorDown } from "./ConductorDown";
import { Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageFrame } from "../PageHeader";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
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
import { Textarea } from "../ui/textarea";

const PERSONAL_SECTIONS = ["identity", "instructions", "model"] as const;

type PersonalSection = (typeof PERSONAL_SECTIONS)[number];

const PERSONAL: { id: PersonalSection; label: string; icon: LucideIcon; sentence: string }[] = [
  {
    id: "identity",
    label: "Identity",
    icon: UserRound,
    sentence: "A personal assistant is a voice on top of Podium. It uses Podium's skills and tools.",
  },
  {
    id: "instructions",
    label: "Instructions",
    icon: MessageSquare,
    sentence: "Added after Podium's prompt, for this voice's turns only.",
  },
  {
    id: "model",
    label: "Model",
    icon: Cpu,
    sentence: "Pick a model for this voice, or leave it following Podium. A choice in one chat still wins there.",
  },
];

type AssistantsRoute =
  | { kind: "podium"; section: AssistantSection }
  | { kind: "new"; section: PersonalSection }
  | { kind: "one"; id: string; section: PersonalSection }
  | { kind: "redirect"; to: string };

function isPersonalSection(id: string): id is PersonalSection {
  return (PERSONAL_SECTIONS as readonly string[]).includes(id);
}

/** Identity is the bare address. A repeated `identity` segment folds back to that address. */
function personalRoute(kind: "new" | "one", id: string, raw: string | undefined): AssistantsRoute {
  const base = kind === "new" ? "/agent/assistants/new" : `/agent/assistants/${id}`;
  if (!raw) return kind === "new" ? { kind: "new", section: "identity" } : { kind: "one", id, section: "identity" };
  if (raw === "identity") return { kind: "redirect", to: base };
  if (!isPersonalSection(raw)) return { kind: "redirect", to: "/agent/assistants" };
  return kind === "new" ? { kind: "new", section: raw } : { kind: "one", id, section: raw };
}

function personalSectionHref(base: string, id: string): string {
  return id === "identity" ? base : `${base}/${id}`;
}

/** The address after /agent/assistants. `podium` is the built-in assistant's sections, not an id. */
function parseAssistantsPath(splat: string): AssistantsRoute {
  const parts = splat.split("/").filter(Boolean);
  if (parts.length === 0) return { kind: "podium", section: "identity" };
  if (parts[0] === "podium") {
    if (parts.length > 2) return { kind: "redirect", to: "/agent/assistants" };
    const section = assistantSection(`/agent/assistants/podium/${parts[1] ?? ""}`);
    return section ? { kind: "podium", section } : { kind: "redirect", to: "/agent/assistants" };
  }
  if (parts.length > 2) return { kind: "redirect", to: "/agent/assistants" };
  if (parts[0] === "new") return personalRoute("new", "", parts[1]);
  return personalRoute("one", parts[0], parts[1]);
}

/**
 * Assistants is where a person defines a voice, and where an owner maintains Podium.
 * Chat is where they talk. This screen does not start a turn.
 *
 * The roster is on the left and the open assistant is one panel on the right, drawn in the
 * same parts as MCP, Secrets and Models.
 */
export function Assistants() {
  const viewer = useViewer();
  const splat = useParams()["*"] ?? "";
  const route = parseAssistantsPath(splat);
  const voices = useQuery({
    queryKey: ["agent", "personalities"],
    queryFn: () => agent.listPersonalities({}),
    staleTime: 30_000,
  });

  if (route.kind === "redirect") return <Navigate to={route.to} replace />;
  // canManageInfra is true until WhoAmI answers. Wait, or a member's Podium flashes editable.
  if (!viewer) return null;

  const personalities = voices.data?.personalities ?? [];
  const down = isAgentUnreachable(voices.error);
  const readOnly = !canManageInfra(viewer);

  return (
    <PageFrame
      title="Assistants"
      lead="The assistants people can talk to, and the personality and job each one has."
      actions={
        // With none of your own yet, the roster's own New card is the way in.
        route.kind === "new" || voices.isPending || personalities.length === 0 ? null : (
          <Button size="sm" asChild data-testid="assistant-new">
            <Link to="/agent/assistants/new" aria-label="New assistant">
              <Plus />
              <span className="hidden sm:inline">New assistant</span>
            </Link>
          </Button>
        )
      }
    >
      <div className="grid gap-5 lg:grid-cols-[19rem_minmax(0,1fr)] lg:items-start">
        <Roster route={route} personalities={personalities} loading={voices.isPending} />
        <div className="min-w-0">
          {route.kind === "podium" ? (
            <PodiumEditor section={route.section} readOnly={readOnly} />
          ) : down ? (
            <ConductorDown
              what="Your assistants could not be read"
              onRetry={() => void voices.refetch()}
              retrying={voices.isFetching}
            />
          ) : voices.isPending ? (
            <EditorSkeleton />
          ) : voices.isError ? (
            <Empty title="Could not read your assistants" hint={errorMessage(voices.error)} />
          ) : route.kind === "new" ? (
            <PersonalityEditor personality={null} section={route.section} />
          ) : (
            <OpenPersonality personalities={personalities} id={route.id} section={route.section} />
          )}
        </div>
      </div>
    </PageFrame>
  );
}

/**
 * Roster is every assistant this person can open: Podium first, then their own voices. Each
 * card says what it runs on and how it starts, so the list answers "which one was that"
 * without opening each. A narrow window scrolls the cards sideways above the panel.
 */
function Roster({
  route,
  personalities,
  loading,
}: {
  route: AssistantsRoute;
  personalities: Personality[];
  loading: boolean;
}) {
  const profile = useQuery({
    queryKey: ["agent", "profile"],
    queryFn: () => agent.getProfile({}),
    staleTime: 30_000,
  });
  const podium = profile.data?.profile;
  const sorted = [...personalities].sort(comparePersonalities);
  // The draft card shows while one is being written, and as the way in when there are none.
  const showNew = route.kind === "new" || (!loading && sorted.length === 0);

  return (
    <nav aria-label="Assistants" className="min-w-0 lg:sticky lg:top-0">
      <ul className="-mx-4 flex snap-x scroll-px-4 gap-2.5 overflow-x-auto px-4 sm:scroll-px-6 pb-1 [scrollbar-width:thin] sm:-mx-6 sm:px-6 lg:mx-0 lg:flex-col lg:overflow-visible lg:px-0 lg:pb-0">
        <li className="w-72 shrink-0 snap-start lg:w-auto">
          <RosterCard
            to="/agent/assistants"
            open={route.kind === "podium"}
            tile={<AssistantTile kind="podium" />}
            name={podium?.displayName || "Podium"}
            badge={<Chip className="px-2 py-0.5">Built-in</Chip>}
            model={<ModelLine agentId={podium?.agent ?? ""} model={podium?.model ?? ""} fallback="Reading…" />}
            hint={firstLine(podium?.systemPrompt ?? "")}
          />
        </li>
        {loading
          ? Array.from({ length: 2 }, (_, i) => (
              <li key={i} className="w-72 shrink-0 lg:w-auto" aria-hidden>
                <div className="flex items-start gap-3 rounded-lg border border-border bg-panel p-3">
                  <Skeleton className="size-10 rounded-sm" />
                  <div className="flex-1 space-y-2 pt-1">
                    <Skeleton className="h-4 w-28" />
                    <Skeleton className="h-3 w-40" />
                  </div>
                </div>
              </li>
            ))
          : sorted.map((p) => (
              <li key={p.id} className="w-72 shrink-0 snap-start lg:w-auto">
                <RosterCard
                  to={`/agent/assistants/${p.id}`}
                  open={route.kind === "one" && route.id === p.id}
                  tile={<AssistantTile kind="personal" name={p.displayName || p.name} seed={p.id} />}
                  name={p.displayName || p.name}
                  model={<ModelLine agentId={p.agent} model={p.model} fallback="Follows Podium's model" />}
                  hint={firstLine(p.instructions)}
                />
              </li>
            ))}
        {showNew ? (
          <li className="w-72 shrink-0 snap-start lg:w-auto">
            <RosterCard
              to="/agent/assistants/new"
              open={route.kind === "new"}
              dashed
              tile={<AssistantTile kind="new" />}
              name="New assistant"
              model={<span>A voice of your own on top of Podium</span>}
            />
          </li>
        ) : null}
      </ul>
    </nav>
  );
}

function RosterCard({
  to,
  open,
  tile,
  name,
  badge,
  model,
  hint,
  dashed = false,
}: {
  to: string;
  open: boolean;
  tile: ReactNode;
  name: string;
  badge?: ReactNode;
  model: ReactNode;
  hint?: string;
  dashed?: boolean;
}) {
  const nameId = useId();
  const detailId = useId();
  return (
    <Link
      to={to}
      aria-current={open ? "page" : undefined}
      aria-labelledby={nameId}
      aria-describedby={detailId}
      className={cn(
        "group flex h-full items-start gap-3 rounded-lg border p-3 transition-[background-color,border-color,box-shadow] duration-150 ease-out",
        "outline-none focus-visible:ring-2 focus-visible:ring-ring focus-visible:ring-offset-2 focus-visible:ring-offset-bg",
        open
          ? "border-accent bg-panel shadow-[inset_0_0_0_1px_var(--color-accent),0_1px_3px_rgb(25_35_45/0.08)]"
          : dashed
            ? "border-dashed border-line-strong bg-transparent hover:bg-panel"
            : "border-border bg-panel hover:border-line-strong hover:bg-raised",
      )}
    >
      {tile}
      <span className="min-w-0 flex-1">
        <span className="flex min-w-0 items-center gap-2">
          <span id={nameId} className="min-w-0 truncate text-base font-bold text-fg" title={name}>
            {name}
          </span>
          {badge}
        </span>
        <span id={detailId} className="block">
          <span className="mt-0.5 flex min-w-0 items-center gap-1.5 text-xs text-muted">{model}</span>
          {hint ? <span className="mt-1.5 line-clamp-2 text-xs leading-relaxed text-muted">{hint}</span> : null}
        </span>
      </span>
    </Link>
  );
}

/** The first line of a prompt that is prose, for a card that has room for a sentence. */
function firstLine(text: string): string {
  for (const raw of text.split("\n")) {
    const line = raw.trim();
    if (line === "" || line.startsWith("#")) continue;
    return line.length > 140 ? `${line.slice(0, 140)}…` : line;
  }
  return "";
}

function OpenPersonality({
  personalities,
  id,
  section,
}: {
  personalities: Personality[];
  id: string;
  section: PersonalSection;
}) {
  const row = personalities.find((p) => p.id === id);
  if (!row) {
    return (
      <Empty
        icon={UserRound}
        title="That assistant is not here"
        hint="It belongs to someone else, or it was deleted."
        action={
          <Button size="sm" variant="outline" asChild>
            <Link to="/agent/assistants">Open Podium</Link>
          </Button>
        }
      />
    );
  }
  return <PersonalityEditor personality={row} section={section} />;
}

function PersonalityEditor({ personality, section }: { personality: Personality | null; section: PersonalSection }) {
  const saved: PersonalityDraft = personality
    ? {
        name: personality.name,
        displayName: personality.displayName,
        instructions: personality.instructions,
        agent: personality.agent,
        model: personality.model,
        effort: personality.effort,
      }
    : emptyPersonality();
  return (
    <PersonalityForm
      key={
        personality
          ? `${personality.id}:${personality.name}:${personality.displayName}:${personality.instructions}:${personality.agent}:${personality.model}:${personality.effort}`
          : "new"
      }
      id={personality?.id ?? ""}
      saved={saved}
      section={section}
    />
  );
}

function PersonalityForm({ id, saved, section }: { id: string; saved: PersonalityDraft; section: PersonalSection }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const toast = useToast();
  const [draft, setDraft] = useState(saved);
  const [askDelete, setAskDelete] = useState(false);
  const issues = new Set(personalityIssues(draft));
  const ready = canSavePersonality(draft, saved);
  const base = id ? `/agent/assistants/${id}` : "/agent/assistants/new";
  const sentence = PERSONAL.find((item) => item.id === section)?.sentence;
  const tabs: AssistantTab[] = PERSONAL.map((item) => ({
    id: item.id,
    label: item.label,
    icon: item.icon,
    unsaved:
      item.id === "identity"
        ? cleanDisplay(draft.displayName) !== cleanDisplay(saved.displayName)
        : item.id === "instructions"
          ? draft.instructions.trim() !== saved.instructions.trim()
          : personalityModelDirty(draft, saved),
  }));
  const dirty = tabs.filter((t) => t.unsaved).map((t) => t.label);
  const title = cleanDisplay(draft.displayName) || cleanDisplay(saved.displayName) || "New assistant";

  const remember = (row: Personality) => {
    qc.setQueryData(["agent", "personalities"], (current: { personalities: Personality[] } | undefined) => {
      const personalities = [...(current?.personalities ?? []).filter((p) => p.id !== row.id), row];
      personalities.sort(comparePersonalities);
      return { personalities };
    });
  };

  const save = useMutation({
    mutationFn: async () => {
      const displayName = cleanDisplay(draft.displayName);
      const instructions = draft.instructions.trim();
      const choice = {
        agent: draft.agent.trim(),
        model: draft.model.trim(),
        effort: draft.effort.trim(),
      };
      const res = id
        ? await agent.updatePersonality({ id, name: "", displayName, instructions, ...choice })
        : await agent.createPersonality({ name: "", displayName, instructions, ...choice });
      const row = res.personality;
      if (!row?.id) throw new Error("the conductor did not return the assistant");
      return row;
    },
    onSuccess: async (row) => {
      remember(row);
      toast("Saved. The next turn uses it.", "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "personalities"] });
      if (!id) navigate(`/agent/assistants/${row.id}`);
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const remove = useMutation({
    mutationFn: () => agent.deletePersonality({ id }),
    onSuccess: async () => {
      setAskDelete(false);
      qc.setQueryData(["agent", "personalities"], (current: { personalities: Personality[] } | undefined) => ({
        personalities: (current?.personalities ?? []).filter((p) => p.id !== id),
      }));
      await qc.invalidateQueries({ queryKey: ["agent", "personalities"] });
      navigate("/agent/assistants");
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const commit = () => {
    if (!ready || save.isPending) return;
    save.mutate();
  };

  const status = !id
    ? ready
      ? "Not saved yet. Save to add it to Chat."
      : "Not saved yet. Give it a display name to save it."
    : dirty.length > 0
      ? `Unsaved changes in ${listSections(dirty)}`
      : "Saved. Chat uses it on the next turn.";

  return (
    <AssistantPanel label={title}>
      <PanelHeader
        tile={<AssistantTile kind={id ? "personal" : "new"} name={title === "New assistant" ? "" : title} seed={id} size="detail" />}
        title={title}
        chips={<Chip>{id ? "Personal" : "Draft"}</Chip>}
        subtitle={<ModelLine agentId={draft.agent} model={draft.model} fallback="Follows Podium's model" />}
        actions={
          id ? (
            <>
              <Button variant="outline" size="sm" asChild>
                <Link to={`/agent/chat/a/${id}`}>
                  <MessageSquarePlus />
                  Talk to it
                </Link>
              </Button>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                className="text-err hover:bg-err-bg"
                onClick={() => setAskDelete(true)}
              >
                <Trash2 />
                Delete
              </Button>
            </>
          ) : (
            <Button variant="ghost" size="sm" asChild>
              <Link to="/agent/assistants">Cancel</Link>
            </Button>
          )
        }
      />
      <SectionTabs tabs={tabs} current={section} href={(item) => personalSectionHref(base, item)} />
      <form
        id="personality-form"
        className="min-w-0"
        onSubmit={(e) => {
          e.preventDefault();
          commit();
        }}
      >
        {sentence ? <ScopeBar>{sentence}</ScopeBar> : null}
        <PanelBody hidden={section !== "identity"}>
          <Row id="personality-display" label="Display name" hint="What it is called in its chats, and on its card here.">
            <Input
              id="personality-display"
              value={draft.displayName}
              onChange={(e) => setDraft({ ...draft, displayName: e.target.value })}
              className="max-w-md"
              autoComplete="off"
              aria-invalid={issues.has("display-name") && cleanDisplay(draft.displayName) !== ""}
            />
            <FieldNote text={displayNote(draft)} />
          </Row>
        </PanelBody>
        <PanelBody hidden={section !== "instructions"}>
          <Row
            id="personality-instructions"
            label="Instructions"
            hint={
              <>
                How this voice talks and what it cares about.{" "}
                <span className="tabular-nums">{draft.instructions.trim().length.toLocaleString()} of 4,000 characters.</span>
              </>
            }
          >
            <Textarea
              id="personality-instructions"
              value={draft.instructions}
              onChange={(e) => setDraft({ ...draft, instructions: e.target.value })}
              className="min-h-64 leading-relaxed"
              spellCheck
              aria-invalid={issues.has("instructions") && draft.instructions.trim() !== ""}
            />
            <FieldNote text={instructionsNote(draft)} />
          </Row>
        </PanelBody>
        <PanelBody hidden={section !== "model"}>
          <VoiceModel
            value={{ agent: draft.agent, model: draft.model, effort: draft.effort }}
            onChange={(next) => setDraft({ ...draft, ...next })}
          />
        </PanelBody>

        <SaveBar dirty={dirty.length > 0 || !id} status={status}>
          <Button type="submit" disabled={!ready || save.isPending}>
            {save.isPending ? "Saving…" : "Save"}
          </Button>
        </SaveBar>
      </form>

      <Dialog open={askDelete} onOpenChange={setAskDelete}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {saved.displayName || saved.name}?</DialogTitle>
            <DialogDescription>
              Chats you already had stay readable under this name. A new message in one of them is refused.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button type="button" variant="outline" size="sm" onClick={() => setAskDelete(false)}>
              Keep
            </Button>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              disabled={remove.isPending}
              onClick={() => remove.mutate()}
            >
              {remove.isPending ? "Deleting…" : "Delete assistant"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </AssistantPanel>
  );
}

/** VoiceModel picks this assistant's default. An empty choice follows Podium, and is stored empty. */
function VoiceModel({ value, onChange }: { value: AgentChoice; onChange: (next: AgentChoice) => void }) {
  const { agents, loading, defaultAgent } = useAgents();
  const profile = useQuery({
    queryKey: ["agent", "profile"],
    queryFn: () => agent.getProfile({}),
    staleTime: 30_000,
  });
  const raw = profile.data?.profile;
  const view = raw ? draftFromProfile(viewFromProfile(raw), defaultAgent) : null;
  const inherited = view ? { agent: view.agent, model: view.model, effort: view.effort } : undefined;

  return (
    <Row label="Model" hint="Leave it on Podium's model and it changes when Podium's does.">
      {view ? (
        <div className="max-w-md">
        <AgentPicker
          label="Assistant"
          agents={agents}
          loading={loading}
          value={value}
          onChange={onChange}
          inherit={{ label: "Podium's model", hint: view.model }}
          inherited={inherited}
        />
        </div>
      ) : profile.isError ? (
        <p className="text-sm leading-relaxed text-muted">Podium's model could not be read.</p>
      ) : (
        <Skeleton className="h-11 w-full max-w-md" />
      )}
    </Row>
  );
}

function FieldNote({ text }: { text: string | null }) {
  if (!text) return null;
  return <Issue>{text}</Issue>;
}

function displayNote(draft: PersonalityDraft): string | null {
  const display = cleanDisplay(draft.displayName);
  if (display === "" || !personalityIssues(draft).includes("display-name")) return null;
  if (hasControl(display)) return "Take the control characters out.";
  return "Keep the display name to 80 characters.";
}

function hasControl(value: string): boolean {
  for (const ch of value) {
    const code = ch.codePointAt(0) ?? 0;
    if (code <= 0x1f || code === 0x7f) return true;
  }
  return false;
}

function instructionsNote(draft: PersonalityDraft): string | null {
  const instructions = draft.instructions.trim();
  if (instructions === "" || !personalityIssues(draft).includes("instructions")) return null;
  return "Keep the instructions to 4,000 characters.";
}

function EditorSkeleton() {
  return (
    <div aria-busy="true" className="space-y-5 rounded-lg border border-border bg-panel p-6">
      <div className="flex items-center gap-4">
        <Skeleton className="size-14 rounded-sm" />
        <div className="space-y-2">
          <Skeleton className="h-5 w-40" />
          <Skeleton className="h-3.5 w-28" />
        </div>
      </div>
      <Skeleton className="h-11 w-full max-w-md" />
    </div>
  );
}
