import { useState, type MouseEvent, type ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { Cpu, MessageSquare, MessageSquarePlus, Plus, Trash2, UserRound } from "lucide-react";
import { Link, Navigate, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import type { Timestamp } from "@bufbuild/protobuf/wkt";
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
import { AgentPicker } from "./AgentPicker";
import { ModelLine, PodiumEditor } from "./AssistantProfile";
import {
  AssistantPanel,
  AssistantTile,
  Issue,
  PanelBody,
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
import { Skeleton, TableSkeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { absolute, relative } from "../../lib/format";
import { AssistantRowActions, DeleteAssistantDialog } from "./AssistantRowActions";
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
  | { kind: "list" }
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

/**
 * The address after /agent/assistants. Bare is the list. `podium` is the built-in assistant's
 * page, `new` is the form for a new voice, and anything else is a personal assistant's id.
 */
function parseAssistantsPath(splat: string): AssistantsRoute {
  const parts = splat.split("/").filter(Boolean);
  if (parts.length === 0) return { kind: "list" };
  if (parts[0] === "podium") {
    if (parts.length > 2) return { kind: "redirect", to: "/agent/assistants" };
    const section = assistantSection(`/agent/assistants/podium/${parts[1] ?? ""}`);
    return section ? { kind: "podium", section } : { kind: "redirect", to: "/agent/assistants" };
  }
  if (parts.length > 2) return { kind: "redirect", to: "/agent/assistants" };
  if (parts[0] === "new") return personalRoute("new", "", parts[1]);
  return personalRoute("one", parts[0], parts[1]);
}

const LIST = "/agent/assistants";
const BACK = { to: LIST, label: "Assistants" };

/**
 * Assistants is where a person defines a voice, and where an owner maintains Podium.
 * Chat is where they talk. This screen does not start a turn.
 *
 * The list is a table, the way Tasks and Playbooks are, and a row opens that assistant on
 * its own page with the list one click back.
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
  const readOnly = !canManageInfra(viewer);

  if (route.kind === "list") return <AssistantList voices={voices} />;
  if (route.kind === "podium") return <PodiumEditor section={route.section} readOnly={readOnly} />;

  const title = route.kind === "new" ? "New assistant" : "Assistant";
  if (isAgentUnreachable(voices.error)) {
    return (
      <PageFrame title={title} back={BACK}>
        <ConductorDown
          what="Your assistants could not be read"
          onRetry={() => void voices.refetch()}
          retrying={voices.isFetching}
        />
      </PageFrame>
    );
  }
  if (voices.isPending) {
    return (
      <PageFrame title={title} back={BACK}>
        <EditorSkeleton />
      </PageFrame>
    );
  }
  if (voices.isError) {
    return (
      <PageFrame title={title} back={BACK}>
        <Empty title="Could not read your assistants" hint={errorMessage(voices.error)} />
      </PageFrame>
    );
  }
  if (route.kind === "new") return <PersonalityEditor personality={null} section={route.section} />;
  return <OpenPersonality personalities={personalities} id={route.id} section={route.section} />;
}

/**
 * AssistantList is every assistant this person can open: Podium first, then their own voices
 * in name order. Each row says what it runs on, what it may reach, and when it last changed,
 * so the table answers "which one was that" without opening each.
 */
function AssistantList({ voices }: { voices: UseQueryResult<{ personalities: Personality[] }> }) {
  const navigate = useNavigate();
  const profile = useQuery({
    queryKey: ["agent", "profile"],
    queryFn: () => agent.getProfile({}),
    staleTime: 30_000,
  });
  const podium = profile.data?.profile;
  const personalities = [...(voices.data?.personalities ?? [])].sort(comparePersonalities);
  const down = isAgentUnreachable(voices.error) || isAgentUnreachable(profile.error);

  const openRow = (e: MouseEvent<HTMLTableRowElement>, href: string) => {
    // The name cell holds the real link; anything that is already a control keeps its click.
    if ((e.target as HTMLElement).closest("a,button")) return;
    void navigate(href);
  };

  return (
    <PageFrame
      title="Assistants"
      lead="The assistants people can talk to, and the personality and job each one has."
      actions={
        <Button size="sm" asChild data-testid="assistant-new">
          <Link to={`${LIST}/new`} aria-label="New assistant">
            <Plus />
            <span className="hidden sm:inline">New assistant</span>
          </Link>
        </Button>
      }
    >
      {down ? (
        <ConductorDown
          what="Your assistants could not be read"
          onRetry={() => {
            void voices.refetch();
            void profile.refetch();
          }}
          retrying={voices.isFetching || profile.isFetching}
        />
      ) : null}
      {voices.isError && !down ? <Empty title="Could not read your assistants" hint={errorMessage(voices.error)} /> : null}

      {profile.isPending || voices.isPending ? (
        <TableSkeleton rows={3} cols={5} />
      ) : (
        <Table>
          <caption className="sr-only">Assistants</caption>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Assistant</TableHead>
              <TableHead>Model</TableHead>
              <TableHead>Reaches</TableHead>
              <TableHead>Updated</TableHead>
              <TableHead pinned>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {podium || !profile.isError ? (
              <AssistantRow
                href={`${LIST}/podium`}
                chatHref="/agent/chat"
                onOpen={openRow}
                tile={<AssistantTile kind="podium" />}
                name={podium?.displayName || "Podium"}
                badge={<Chip className="px-2 py-0.5">Built-in</Chip>}
                hint={firstLine(podium?.systemPrompt ?? "")}
                model={<ModelLine agentId={podium?.agent ?? ""} model={podium?.model ?? ""} />}
                reaches={<Reaches skills={podium?.skills?.length ?? 0} servers={podium?.mcpServers?.length ?? 0} />}
                updatedAt={podium?.updatedAt}
                updatedBy={podium?.updatedBy}
              />
            ) : null}
            {personalities.map((p) => (
              <AssistantRow
                key={p.id}
                href={`${LIST}/${p.id}`}
                chatHref={`/agent/chat/a/${p.id}`}
                personalId={p.id}
                onOpen={openRow}
                tile={<AssistantTile kind="personal" name={p.displayName || p.name} seed={p.id} />}
                name={p.displayName || p.name}
                badge={<Chip className="px-2 py-0.5">Personal</Chip>}
                hint={firstLine(p.instructions)}
                model={<ModelLine agentId={p.agent} model={p.model} fallback="Follows Podium's model" />}
                reaches={<span className="text-muted">Podium&rsquo;s skills and tools</span>}
                updatedAt={p.updatedAt}
              />
            ))}
          </TableBody>
        </Table>
      )}

      {!voices.isPending && !voices.isError && personalities.length === 0 ? (
        <div className="flex flex-col gap-4 rounded-lg border border-dashed border-line-strong px-6 py-5 sm:flex-row sm:items-center">
          <AssistantTile kind="new" />
          <div className="min-w-0 flex-1">
            <p className="m-0 font-bold text-fg">No assistants of your own yet</p>
            <p className="m-0 text-sm text-muted">
              Give Podium another voice: a name, a few lines on how it talks, and a model if it should differ.
            </p>
          </div>
          <Button variant="outline" size="sm" asChild className="shrink-0 self-start sm:self-center">
            <Link to={`${LIST}/new`}>
              <Plus />
              Add one
            </Link>
          </Button>
        </div>
      ) : null}
    </PageFrame>
  );
}

function AssistantRow({
  href,
  chatHref,
  personalId,
  onOpen,
  tile,
  name,
  badge,
  hint,
  model,
  reaches,
  updatedAt,
  updatedBy,
}: {
  href: string;
  chatHref: string;
  personalId?: string;
  onOpen: (e: MouseEvent<HTMLTableRowElement>, href: string) => void;
  tile: ReactNode;
  name: string;
  badge: ReactNode;
  hint: string;
  model: ReactNode;
  reaches: ReactNode;
  updatedAt?: Timestamp;
  updatedBy?: string;
}) {
  return (
    <TableRow data-testid="assistant-row" onClick={(e) => onOpen(e, href)} className="cursor-pointer">
      <TableCell className="max-w-[30rem] py-3">
        <div className="flex items-center gap-3">
          {tile}
          <div className="min-w-0">
            <div className="flex min-w-0 items-center gap-2">
              <Link
                to={href}
                className="min-w-0 truncate rounded-sm font-bold text-fg outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring"
              >
                {name}
              </Link>
              {badge}
            </div>
            {hint ? (
              <p className="m-0 mt-0.5 truncate text-xs text-muted" title={hint}>
                {hint}
              </p>
            ) : null}
          </div>
        </div>
      </TableCell>
      <TableCell className="text-xs whitespace-nowrap">
        <span className="flex items-center gap-1.5">{model}</span>
      </TableCell>
      <TableCell className="text-xs whitespace-nowrap">{reaches}</TableCell>
      <TableCell className="text-xs whitespace-nowrap text-muted" title={updatedAt ? absolute(updatedAt) : undefined}>
        {updatedAt ? relative(updatedAt) : "-"}
        {updatedBy ? <div className="truncate text-2xs text-faint">{updatedBy}</div> : null}
      </TableCell>
      <TableCell pinned className="w-0 pr-2 text-right">
        <AssistantRowActions name={name} href={href} chatHref={chatHref} personalId={personalId} />
      </TableCell>
    </TableRow>
  );
}

function Reaches({ skills, servers }: { skills: number; servers: number }) {
  if (skills === 0 && servers === 0) return <span className="text-muted">Delegates to playbooks</span>;
  const parts = [
    skills ? `${skills} ${skills === 1 ? "skill" : "skills"}` : "",
    servers ? `${servers} MCP ${servers === 1 ? "server" : "servers"}` : "",
  ].filter(Boolean);
  return <span className="text-fg">{parts.join(" · ")}</span>;
}

/** The first line of a prompt that is prose, for a row that has room for a sentence. */
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
      <PageFrame title="Assistant" back={BACK}>
        <Empty
          icon={UserRound}
          title="That assistant is not here"
          hint="It belongs to someone else, or it was deleted."
          action={
            <Button size="sm" variant="outline" asChild>
              <Link to={LIST}>Back to assistants</Link>
            </Button>
          }
        />
      </PageFrame>
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
      if (!id) void navigate(`${LIST}/${row.id}`, { replace: true });
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

  const savedName = cleanDisplay(saved.displayName) || saved.name;

  return (
    <PageFrame
      title={id ? savedName : "New assistant"}
      back={BACK}
      meta={<Chip>{id ? "Personal" : "Draft"}</Chip>}
      actions={
        id ? (
          <>
            <Button variant="outline" size="sm" asChild>
              <Link to={`/agent/chat/a/${id}`} aria-label="Talk to it">
                <MessageSquarePlus />
                <span className="hidden sm:inline">Talk to it</span>
              </Link>
            </Button>
            <Button
              type="button"
              variant="ghost"
              size="sm"
              aria-label="Delete"
              className="text-err hover:bg-err-bg"
              onClick={() => setAskDelete(true)}
            >
              <Trash2 />
              <span className="hidden sm:inline">Delete</span>
            </Button>
          </>
        ) : null
      }
    >
      <AssistantPanel label={title}>
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
            <Row id="personality-display" label="Display name" hint="What it is called in its chats, and on the list of assistants.">
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
            {id ? null : (
              <Button variant="ghost" asChild>
                <Link to={LIST}>Cancel</Link>
              </Button>
            )}
            <Button type="submit" disabled={!ready || save.isPending}>
              {save.isPending ? "Saving…" : "Save"}
            </Button>
          </SaveBar>
        </form>
      </AssistantPanel>

      {id ? (
        <DeleteAssistantDialog
          id={id}
          name={savedName}
          open={askDelete}
          onOpenChange={setAskDelete}
          onDeleted={() => void navigate(LIST)}
        />
      ) : null}
    </PageFrame>
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
      <div className="flex gap-6">
        <Skeleton className="h-4 w-20" />
        <Skeleton className="h-4 w-24" />
        <Skeleton className="h-4 w-16" />
      </div>
      <Skeleton className="h-4 w-96 max-w-full" />
      <Skeleton className="h-11 w-full max-w-md" />
    </div>
  );
}
