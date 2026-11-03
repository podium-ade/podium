import { FileCode2, Plus, Search, SearchX, Sparkles } from "lucide-react";
import { useMemo, useState, type MouseEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import type { PlaybookDefinition } from "../../gen/podium/agent/v1/agent_pb";
import { PlaybookDefinitionSchema } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import { agent, errorMessage, isAgentUnreachable, sandboxProviders, secrets } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import type { PlaybookDraft } from "../../lib/playbook";
import { RUNS_ON_NODES } from "../../lib/sandboxProviders";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageFrame } from "../PageHeader";
import { TableSkeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { ConductorDown } from "./ConductorDown";
import { PlaybookEditor, type PlaybookEditorProps } from "./PlaybookEditor";
import { RowActions } from "./RowActions";
import { ReloadProfileDirButton } from "./ReloadProfileDirButton";
import { useViewer } from "../../lib/identity";
import { canManageInfra } from "../../lib/rbac";

const HINT_CHARS = 160;
const LIST = "/agent/playbooks";

function playbookPath(name: string): string {
  return `${LIST}/${encodeURIComponent(name)}`;
}

function useProfile() {
  return useQuery({
    queryKey: ["agent", "profile"],
    queryFn: () => agent.getProfile({}),
  });
}

/**
 * PlaybooksPanel is the list of playbooks/<name>.yaml on the conductor, one row each, the
 * way Tasks lists tasks. A row opens the playbook on its own page (PlaybookPage).
 */
export function PlaybooksPanel() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const navigate = useNavigate();
  const profile = useProfile();
  const [filter, setFilter] = useState("");
  const qc = useQueryClient();
  const deletePlaybook = async (name: string) => {
    await agent.deletePlaybook({ name });
    await qc.invalidateQueries({ queryKey: ["agent", "profile"] });
  };

  const all = useMemo(() => profile.data?.playbooks ?? [], [profile.data]);
  const rows = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return all;
    return all.filter((p) =>
      [p.name, p.image, p.model, p.agent, p.runsOn, promptHint(p.systemPrompt ?? "")].some((v) =>
        (v ?? "").toLowerCase().includes(q),
      ),
    );
  }, [all, filter]);

  if (profile.isError && !isAgentUnreachable(profile.error)) {
    return (
      <PageFrame title="Playbooks">
        <Empty title="Could not read the agent profile" hint={errorMessage(profile.error)} />
      </PageFrame>
    );
  }

  const openRow = (e: MouseEvent<HTMLTableRowElement>, name: string) => {
    // The name cell holds the real link; anything that is already a control keeps its click.
    if ((e.target as HTMLElement).closest("a,button")) return;
    void navigate(playbookPath(name));
  };

  return (
    <PageFrame
      title="Playbooks"
      lead="The jobs your assistants know how to do: what each one is for, and the tools it may use."
      actions={
        manage ? (
          <div className="flex items-center gap-2">
            <ReloadProfileDirButton />
            <Button type="button" size="sm" data-testid="playbook-new" onClick={() => void navigate(`${LIST}/new`)}>
              <Plus />
              New playbook
            </Button>
          </div>
        ) : null
      }
    >
      {isAgentUnreachable(profile.error) ? (
        <ConductorDown
          what="The playbooks could not be read"
          onRetry={() => void profile.refetch()}
          retrying={profile.isFetching}
        />
      ) : null}

      <StaleAlert reason={profile.data?.staleReason} />

      {profile.isPending ? <TableSkeleton rows={4} cols={6} /> : null}

      {!profile.isPending && !profile.isError && all.length === 0 ? (
        <Empty
          icon={Sparkles}
          title="No playbooks"
          hint="Nothing for the assistant to start yet."
          action={
            manage ? (
              <Button
                type="button"
                size="sm"
                data-testid="playbook-new-empty"
                onClick={() => void navigate(`${LIST}/new`)}
              >
                <Plus />
                New playbook
              </Button>
            ) : undefined
          }
        />
      ) : null}

      {all.length > 0 ? (
        <>
          <div className="relative max-w-sm">
            <Search className="pointer-events-none absolute top-1/2 left-3 size-4 -translate-y-1/2 text-faint" />
            <Input
              type="search"
              aria-label="Filter playbooks"
              placeholder="Filter by name, image or model"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              className="pl-9"
            />
          </div>

          {rows.length === 0 ? (
            <Empty
              icon={SearchX}
              title="No playbook matches"
              hint={`Nothing here mentions “${filter.trim()}”.`}
              action={
                <Button type="button" size="sm" variant="outline" onClick={() => setFilter("")}>
                  Clear filter
                </Button>
              }
            />
          ) : (
            <Table className="[&_td]:px-2.5 [&_th]:px-2.5">
              <TableHeader>
                <TableRow className="hover:bg-transparent">
                  <TableHead>Playbook</TableHead>
                  <TableHead>Image</TableHead>
                  <TableHead>Model</TableHead>
                  <TableHead>Runs on</TableHead>
                  <TableHead>Reaches</TableHead>
                  <TableHead>Starts from</TableHead>
                  <TableHead>Updated</TableHead>
                  <TableHead pinned />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((p) => (
                  <TableRow
                    key={`${p.name}:${p.origin}:${p.shadowed}`}
                    data-testid="playbook-row"
                    onClick={(e) => openRow(e, p.name)}
                    className="h-[4.5rem] cursor-pointer"
                  >
                    <TableCell className="max-w-[28rem]">
                      <div className="flex items-center gap-2">
                        <Link
                          to={playbookPath(p.name)}
                          aria-label={`Edit ${p.name}`}
                          className="rounded-sm font-mono text-sm font-medium text-fg outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50"
                        >
                          /{p.name}
                        </Link>
                        {p.shadowed ? (
                          <Badge tone="warn" dot={false}>
                            Shadowed
                          </Badge>
                        ) : null}
                      </div>
                      {promptHint(p.systemPrompt) ? (
                        <p className="mt-0.5 truncate text-xs text-muted" title={promptHint(p.systemPrompt)}>
                          {promptHint(p.systemPrompt)}
                        </p>
                      ) : null}
                    </TableCell>
                    <TableCell className="max-w-56">
                      <span className="block truncate font-mono text-xs text-muted" title={p.image} data-testid="playbook-image">
                        {p.image || "-"}
                      </span>
                    </TableCell>
                    <TableCell className="text-xs whitespace-nowrap">
                      <ModelCell p={p} />
                    </TableCell>
                    <TableCell className="text-xs whitespace-nowrap text-muted">{runsOnLabel(p.runsOn)}</TableCell>
                    <TableCell className="text-xs whitespace-nowrap text-muted">
                      <Reaches p={p} />
                    </TableCell>
                    <TableCell>
                      <Triggers p={p} />
                    </TableCell>
                    <TableCell
                      className="text-xs whitespace-nowrap text-muted"
                      title={p.updatedAt ? absolute(p.updatedAt) : undefined}
                    >
                      {p.updatedAt ? relative(p.updatedAt) : "-"}
                      {p.updatedBy ? <div className="truncate text-2xs text-faint">{p.updatedBy}</div> : null}
                    </TableCell>
                    <TableCell pinned className="w-0 pr-2 text-right">
                      <RowActions
                        noun="playbook"
                        name={p.name}
                        title={`/${p.name}`}
                        href={playbookPath(p.name)}
                        testId="playbook"
                        onDelete={manage ? () => deletePlaybook(p.name) : undefined}
                        deleteDescription="The definition goes with it: prompt, tools, limits and the secrets it names. Anything that routes to this playbook stops working on the next turn. There is no undo."
                      />
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}

        </>
      ) : null}
    </PageFrame>
  );
}

/**
 * PlaybookPage is one playbook, or a new one at /agent/playbooks/new: the whole editor, on
 * its own page, with the list one click back.
 */
export function PlaybookPage() {
  const { name: rawName } = useParams();
  const creating = rawName === undefined;
  const name = rawName ?? "";
  const qc = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const profile = useProfile();
  const editorProps = useEditorContext();
  const [saveError, setSaveError] = useState("");

  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "profile"] });
  const back = () => void navigate(LIST);

  const createPb = useMutation({
    mutationFn: (draft: PlaybookDraft) => agent.createPlaybook({ playbook: draftToProto(draft) }),
    onSuccess: async (res) => {
      toast(`/${res.playbook?.name} written.`, "ok");
      setSaveError("");
      await reload();
      void navigate(res.playbook ? playbookPath(res.playbook.name) : LIST, { replace: true });
    },
    onError: (err) => setSaveError(errorMessage(err)),
  });
  const updatePb = useMutation({
    mutationFn: (draft: PlaybookDraft) => agent.updatePlaybook({ playbook: draftToProto(draft) }),
    onSuccess: async (res) => {
      toast(`/${res.playbook?.name} saved.`, "ok");
      setSaveError("");
      await reload();
    },
    onError: (err) => setSaveError(errorMessage(err)),
  });
  const deletePb = useMutation({
    mutationFn: (n: string) => agent.deletePlaybook({ name: n }),
    onSuccess: async (_res, n) => {
      toast(`/${n} deleted.`, "ok");
      setSaveError("");
      await reload();
      void navigate(LIST, { replace: true });
    },
    onError: (err) => setSaveError(errorMessage(err)),
  });

  const all = profile.data?.playbooks ?? [];
  // A shadowed stored playbook can share its name with the file that wins; open the one that runs.
  const playbook = creating
    ? undefined
    : all.find((p) => p.name === name && !p.shadowed) ?? all.find((p) => p.name === name);

  const frame = {
    title: creating ? "New playbook" : `/${name}`,
    back: { to: LIST, label: "Playbooks" },
    meta: playbook?.shadowed ? (
      <Badge tone="warn" dot={false}>
        Shadowed
      </Badge>
    ) : undefined,
  };

  if (profile.isError) {
    return (
      <PageFrame {...frame}>
        {isAgentUnreachable(profile.error) ? (
          <ConductorDown
            what="The playbook could not be read"
            onRetry={() => void profile.refetch()}
            retrying={profile.isFetching}
          />
        ) : (
          <Empty title="Could not read the agent profile" hint={errorMessage(profile.error)} />
        )}
      </PageFrame>
    );
  }

  if (profile.isPending) {
    return (
      <PageFrame {...frame}>
        <TableSkeleton rows={6} cols={2} />
      </PageFrame>
    );
  }

  if (!creating && !playbook) {
    return (
      <PageFrame {...frame}>
        <Empty
          icon={FileCode2}
          title="No such playbook"
          hint={`Nothing on this conductor is called /${name}. It may have been deleted or renamed.`}
          action={
            <Button type="button" size="sm" variant="outline" onClick={back}>
              Back to playbooks
            </Button>
          }
        />
      </PageFrame>
    );
  }

  return (
    <PageFrame {...frame}>
      <StaleAlert reason={profile.data?.staleReason} />
      <PlaybookEditor
        key={creating ? "new" : name}
        embedded
        playbook={playbook}
        saving={creating ? createPb.isPending : updatePb.isPending}
        deleting={deletePb.isPending}
        onSubmit={(draft) => {
          setSaveError("");
          if (creating) createPb.mutate(draft);
          else updatePb.mutate(draft);
        }}
        onDelete={playbook ? () => deletePb.mutate(playbook.name) : undefined}
        onCancel={back}
        error={saveError}
        {...editorProps}
      />
    </PageFrame>
  );
}

/** Everything the editor needs besides the playbook itself: the catalogues its pickers offer. */
function useEditorContext(): Omit<PlaybookEditorProps, "playbook" | "onSubmit" | "onCancel"> {
  const profile = useProfile();
  const secretList = useQuery({ queryKey: ["secrets"], queryFn: () => secrets.listSecrets({}) });
  const skillList = useQuery({ queryKey: ["agent", "skills"], queryFn: () => agent.listSkills({}) });
  const mcpList = useQuery({ queryKey: ["agent", "mcp"], queryFn: () => agent.listMcpServers({}) });
  const { agents } = useAgents();
  // Both only fill in Runs on, so a conductor or control plane that cannot answer leaves
  // the editor working with the providers this build knows.
  const providerList = useQuery({
    queryKey: ["sandboxProviders"],
    queryFn: () => sandboxProviders.listSandboxProviders({}),
    retry: false,
  });
  const runsOn = useQuery({ queryKey: ["agent", "runsOn"], queryFn: () => agent.getRunsOn({}), retry: false });

  return {
    agents,
    profileDefault: {
      agent: profile.data?.profile?.agent ?? "",
      model: profile.data?.profile?.model ?? "",
      effort: profile.data?.profile?.effort ?? "",
    },
    secretNames: (secretList.data?.secrets ?? []).map((s) => s.name),
    secretsUnknown: secretList.isError || secretList.isPending,
    skillNames: (skillList.data?.skills ?? []).map((s) => s.name),
    skillsUnknown: skillList.isError || skillList.isPending,
    mcpNames: (mcpList.data?.servers ?? []).map((m) => m.name),
    disabledMcpNames: (mcpList.data?.servers ?? []).filter((m) => !m.enabled).map((m) => m.name),
    mcpUnknown: mcpList.isError || mcpList.isPending,
    sandboxProviders: providerList.data?.providers,
    defaultRunsOn: runsOn.data?.runsOn?.runsOn,
  };
}

function StaleAlert({ reason }: { reason?: string }) {
  if (!reason) return null;
  return (
    <Alert variant="warn" title="The conductor is running an older profile than the saved assistant would produce">
      The saved assistant would not apply: {reason}
    </Alert>
  );
}

function ModelCell({ p }: { p: PlaybookDefinition }) {
  if (!p.model && !p.agent) return <span className="text-faint">Assistant default</span>;
  return (
    <span className="font-mono text-fg">
      {p.model || p.agent}
      {p.effort ? <span className="text-faint"> · {p.effort}</span> : null}
    </span>
  );
}

function runsOnLabel(runsOn: string): string {
  if (!runsOn) return "Default";
  if (runsOn === RUNS_ON_NODES) return "Nodes";
  return runsOn.charAt(0).toUpperCase() + runsOn.slice(1);
}

function Reaches({ p }: { p: PlaybookDefinition }) {
  const parts = [
    count((p.skills ?? []).length, "skill"),
    count((p.mcpServers ?? []).length, "MCP server"),
    count((p.repos ?? []).length, "repo"),
    count((p.secrets ?? []).length + (p.userSecrets ?? []).length, "secret"),
  ].filter(Boolean);
  if (parts.length === 0) return <span className="text-faint">-</span>;
  return <span>{parts.join(" · ")}</span>;
}

function count(n: number, noun: string): string {
  return n === 0 ? "" : `${n} ${noun}${n === 1 ? "" : "s"}`;
}

function Triggers({ p }: { p: PlaybookDefinition }) {
  const chips = [
    (p.slackChannels ?? []).length > 0 ? "Slack" : "",
    p.linear ? "Linear" : "",
    p.github ? "GitHub" : "",
  ].filter(Boolean);
  if (chips.length === 0) return <span className="text-xs text-faint">Chat</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {chips.map((c) => (
        <Chip key={c}>{c}</Chip>
      ))}
    </div>
  );
}

function draftToProto(draft: PlaybookDraft): PlaybookDefinition {
  return create(PlaybookDefinitionSchema, {
    name: draft.name,
    image: draft.image,
    systemPrompt: draft.systemPrompt,
    allowedTools: draft.allowedTools,
    maxTurns: draft.maxTurns,
    timeout: draft.timeout,
    model: draft.model,
    agent: draft.agent,
    effort: draft.effort,
    labels: draft.labels,
    priority: draft.priority,
    resources: {
      cpu: draft.resources.cpu,
      memoryMb: draft.resources.memoryMb,
      pids: draft.resources.pids,
    },
    secrets: draft.secrets,
    userSecrets: draft.userSecrets,
    repos: draft.repos.map((r) => ({ name: r.name, url: r.url, defaultBranch: r.defaultBranch })),
    git: draft.git,
    slackChannels: draft.slackChannels,
    linear: draft.linear,
    github: draft.github,
    githubRequires: draft.githubRequires,
    interactive: draft.interactive,
    docker: draft.docker,
    browser: draft.browser,
    skills: draft.skills,
    mcpServers: draft.mcpServers,
    env: draft.env,
    runsOn: draft.runsOn,
    sandbox: { boat: { ...draft.sandbox.boat }, modal: { ...draft.sandbox.modal } },
  });
}

function promptHint(prompt: string): string {
  for (const raw of prompt.split("\n")) {
    const line = raw.trim();
    if (line === "" || line.startsWith("#")) continue;
    return line.length > HINT_CHARS ? `${line.slice(0, HINT_CHARS)}…` : line;
  }
  return "";
}
