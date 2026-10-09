import { useState, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Cpu, MessageSquare, UserRound } from "lucide-react";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { Personality } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import {
  assistantSection,
  draftFromProfile,
  viewFromProfile,
  type AssistantSection,
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
import { PodiumEditor } from "./AssistantProfile";
import { ConductorDown } from "./ConductorDown";
import { FormActions, SectionLead } from "./FormActions";
import { SettingsSectionNav, type SettingsSection } from "./SettingsSections";
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
import { Label } from "../ui/label";
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
    <PageFrame title="Assistants">
      <div className="flex flex-col gap-6 sm:flex-row sm:items-start sm:gap-8">
        <VoiceList route={route} personalities={personalities} loading={voices.isPending} />
        <div className="min-w-0 flex-1">
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

function VoiceList({
  route,
  personalities,
  loading,
}: {
  route: AssistantsRoute;
  personalities: Personality[];
  loading: boolean;
}) {
  const podium = route.kind === "podium";
  return (
    <nav
      aria-label="Assistants"
      className="border-b border-border pb-3 sm:sticky sm:top-0 sm:w-48 sm:shrink-0 sm:self-start sm:border-r sm:border-b-0 sm:pr-6 sm:pb-0"
    >
      <ul className="flex flex-col gap-0.5">
        <li>
          <RowLink to="/agent/assistants" open={podium}>
            <span className="min-w-0 flex-1 truncate">Podium</span>
            <span className="shrink-0 text-2xs text-muted">Built-in</span>
          </RowLink>
        </li>
        {loading
          ? Array.from({ length: 2 }, (_, i) => (
              <li key={i} className="flex h-8 items-center px-2.5" aria-hidden>
                <Skeleton className="h-3.5 w-24" />
              </li>
            ))
          : [...personalities].sort(comparePersonalities).map((p) => (
              <li key={p.id}>
                <RowLink to={`/agent/assistants/${p.id}`} open={route.kind === "one" && route.id === p.id}>
                  <span className="min-w-0 flex-1 truncate" title={p.displayName}>
                    {p.displayName || p.name}
                  </span>
                </RowLink>
              </li>
            ))}
        <li>
          <RowLink to="/agent/assistants/new" open={route.kind === "new"}>
            New
          </RowLink>
        </li>
      </ul>
    </nav>
  );
}

function RowLink({ to, open, children }: { to: string; open: boolean; children: ReactNode }) {
  return (
    <Link
      to={to}
      aria-current={open ? "page" : undefined}
      className={cn(
        "flex h-8 items-center gap-2 rounded-md px-2.5 text-sm transition-colors duration-150",
        "outline-none focus-visible:ring-2 focus-visible:ring-ring",
        open ? "bg-raised font-medium text-fg shadow-xs" : "text-muted hover:bg-panel hover:text-fg",
      )}
    >
      {children}
    </Link>
  );
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
    return <p className="text-sm leading-relaxed text-muted">That assistant is not yours, or it was deleted.</p>;
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
  const nav: SettingsSection[] = PERSONAL.map((item) => ({
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

  return (
    <div className="flex flex-col gap-6 sm:flex-row sm:items-start sm:gap-8">
      <SettingsSectionNav
        label="Assistant"
        sections={nav}
        current={section}
        href={(item) => personalSectionHref(base, item)}
      />
      <form
        id="personality-form"
        className="min-w-0 max-w-3xl flex-1"
        onSubmit={(e) => {
          e.preventDefault();
          commit();
        }}
      >
      {sentence ? <SectionLead>{sentence}</SectionLead> : null}
      <div hidden={section !== "identity"}>
        <Field id="personality-display" label="Display name" hint="What it is called in its chats.">
          <Input
            id="personality-display"
            value={draft.displayName}
            onChange={(e) => setDraft({ ...draft, displayName: e.target.value })}
            className="h-8 max-w-sm"
            autoComplete="off"
            aria-invalid={issues.has("display-name") && cleanDisplay(draft.displayName) !== ""}
          />
          <FieldNote text={displayNote(draft)} />
        </Field>
      </div>
      <div hidden={section !== "instructions"}>
        <Field id="personality-instructions" label="Instructions">
          <Textarea
            id="personality-instructions"
            value={draft.instructions}
            onChange={(e) => setDraft({ ...draft, instructions: e.target.value })}
            className="min-h-40"
            spellCheck
            aria-invalid={issues.has("instructions") && draft.instructions.trim() !== ""}
          />
          <FieldNote text={instructionsNote(draft)} />
        </Field>
      </div>
      <div hidden={section !== "model"}>
        <VoiceModel
          value={{ agent: draft.agent, model: draft.model, effort: draft.effort }}
          onChange={(next) => setDraft({ ...draft, ...next })}
        />
      </div>

      <FormActions>
        <Button type="submit" size="sm" disabled={!ready || save.isPending}>
          {save.isPending ? "Saving…" : "Save"}
        </Button>
        {id ? (
          <>
            <Button variant="outline" size="sm" asChild>
              <Link to={`/agent/chat/a/${id}`}>Talk to it</Link>
            </Button>
            <Button type="button" variant="danger" size="sm" onClick={() => setAskDelete(true)}>
              Delete
            </Button>
          </>
        ) : null}
      </FormActions>

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
      </form>
    </div>
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
    <Field label="Model">
      {view ? (
        <AgentPicker
          label="Assistant"
          agents={agents}
          loading={loading}
          value={value}
          onChange={onChange}
          inherit={{ label: "Podium's model", hint: view.model }}
          inherited={inherited}
        />
      ) : profile.isError ? (
        <p className="text-sm leading-relaxed text-muted">Podium's model could not be read.</p>
      ) : (
        <Skeleton className="h-9 w-full max-w-sm" />
      )}
    </Field>
  );
}

function Field({
  id,
  label,
  hint,
  children,
}: {
  id?: string;
  label: string;
  hint?: string;
  children: ReactNode;
}) {
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

function FieldNote({ text }: { text: string | null }) {
  if (!text) return null;
  return <p className="text-xs text-err">{text}</p>;
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
    <div aria-busy="true" className="max-w-3xl space-y-4 pt-2">
      <Skeleton className="h-4 w-72" />
      <Skeleton className="h-9 w-full max-w-sm" />
      <Skeleton className="h-9 w-full max-w-sm" />
    </div>
  );
}
