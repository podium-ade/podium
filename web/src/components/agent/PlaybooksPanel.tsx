import { FileCode2, Plus, Sparkles } from "lucide-react";
import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { create } from "@bufbuild/protobuf";
import type { PlaybookDefinition } from "../../gen/podium/agent/v1/agent_pb";
import { PlaybookDefinitionSchema } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import { agent, errorMessage, isAgentUnreachable, secrets } from "../../lib/client";
import type { PlaybookDraft } from "../../lib/playbook";
import { cn } from "../../lib/utils";
import { Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageHeader } from "../PageHeader";
import { TableSkeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { ConductorDown } from "./ConductorDown";
import { PlaybookEditor } from "./PlaybookEditor";
import { ReloadProfileDirButton } from "./ReloadProfileDirButton";

const HINT_CHARS = 160;

/**
 * PlaybooksPanel is a two-pane editor of playbooks/<name>.yaml: pick one (or New), then
 * Form or YAML. Saves write the file on the conductor and re-read the profile directory.
 */
export function PlaybooksPanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<PlaybookDefinition | "new">();
  const [saveError, setSaveError] = useState("");

  const profile = useQuery({
    queryKey: ["agent", "profile"],
    queryFn: () => agent.getProfile({}),
  });
  const secretList = useQuery({
    queryKey: ["secrets"],
    queryFn: () => secrets.listSecrets({}),
  });
  const skillList = useQuery({
    queryKey: ["agent", "skills"],
    queryFn: () => agent.listSkills({}),
  });
  const mcpList = useQuery({
    queryKey: ["agent", "mcp"],
    queryFn: () => agent.listMcpServers({}),
  });
  const { agents } = useAgents();

  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "profile"] });

  const createPb = useMutation({
    mutationFn: (draft: PlaybookDraft) => agent.createPlaybook({ playbook: draftToProto(draft) }),
    onSuccess: async (res) => {
      toast(`/${res.playbook?.name} written.`, "ok");
      setSaveError("");
      await reload();
      if (res.playbook) setEditing(res.playbook);
    },
    onError: (err) => setSaveError(errorMessage(err)),
  });
  const updatePb = useMutation({
    mutationFn: (draft: PlaybookDraft) => agent.updatePlaybook({ playbook: draftToProto(draft) }),
    onSuccess: async (res) => {
      toast(`/${res.playbook?.name} saved.`, "ok");
      setSaveError("");
      await reload();
      if (res.playbook) setEditing(res.playbook);
    },
    onError: (err) => setSaveError(errorMessage(err)),
  });
  const deletePb = useMutation({
    mutationFn: (name: string) => agent.deletePlaybook({ name }),
    onSuccess: async (_res, name) => {
      toast(`/${name} deleted.`, "ok");
      setEditing(undefined);
      setSaveError("");
      await reload();
    },
    onError: (err) => setSaveError(errorMessage(err)),
  });

  if (profile.isError && !isAgentUnreachable(profile.error)) {
    return <Empty title="Could not read the agent profile" hint={errorMessage(profile.error)} />;
  }

  const running = profile.data?.playbooks ?? [];
  const selected =
    editing === "new" ? undefined : editing ? running.find((p) => p.name === editing.name) ?? editing : undefined;

  const editorProps = {
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
    error: saveError,
  };

  return (
    <div className="flex min-h-[36rem] flex-col gap-5">
      <PageHeader
        title="Playbooks"
        description="A job on a node: image, prompt, tools. The assistant starts one, or a /name in chat does."
        actions={
          <div className="flex items-center gap-2">
            <ReloadProfileDirButton />
            <Button
              type="button"
              size="sm"
              data-testid="playbook-new"
              onClick={() => {
                setSaveError("");
                setEditing("new");
              }}
            >
              <Plus />
              New playbook
            </Button>
          </div>
        }
      />

      {isAgentUnreachable(profile.error) ? (
        <ConductorDown
          what="The playbooks could not be read"
          onRetry={() => void profile.refetch()}
          retrying={profile.isFetching}
        />
      ) : null}

      {profile.data?.staleReason ? (
        <Alert variant="warn" title="The conductor is running an older profile than the stored overrides would produce">
          The overrides would not apply: {profile.data.staleReason}
        </Alert>
      ) : null}

      {profile.isPending ? <TableSkeleton rows={3} cols={3} /> : null}

      {!profile.isPending && running.length === 0 && editing !== "new" ? (
        <Empty
          icon={Sparkles}
          title="No playbooks"
          hint="Nothing for the assistant to start yet."
          action={
            <Button type="button" size="sm" data-testid="playbook-new-empty" onClick={() => setEditing("new")}>
              <Plus />
              New playbook
            </Button>
          }
        />
      ) : null}

      {editing === "new" && running.length === 0 && !profile.isPending ? (
        <PlaybookEditor
          key="new"
          playbook={undefined}
          saving={createPb.isPending}
          deleting={false}
          onSubmit={(draft) => {
            setSaveError("");
            createPb.mutate(draft);
          }}
          onCancel={() => {
            setEditing(undefined);
            setSaveError("");
          }}
          {...editorProps}
        />
      ) : null}

      {running.length > 0 && !profile.isPending ? (
      <div className="grid min-h-0 flex-1 gap-5 lg:grid-cols-[18rem_minmax(0,1fr)] lg:items-start">
        <aside className="min-w-0 space-y-2">
            <ul className="space-y-1.5">
              {running.map((s) => (
                <li key={s.name}>
                  <button
                    type="button"
                    data-testid="playbook-row"
                    aria-label={`Edit ${s.name}`}
                    onClick={() => {
                      setSaveError("");
                      setEditing(s);
                    }}
                    className={cn(
                      "w-full rounded-lg border px-3 py-2.5 text-left transition-colors",
                      selected?.name === s.name
                        ? "border-accent/50 bg-accent/10"
                        : "border-border bg-card hover:bg-raised/60",
                    )}
                  >
                    <div className="flex items-center gap-2">
                      <span className="font-mono text-sm font-medium text-fg">/{s.name}</span>
                      {s.linear ? <Chip>Linear</Chip> : null}
                    </div>
                    <p className="mt-1 truncate font-mono text-2xs text-muted" title={s.image} data-testid="playbook-image">
                      {s.image}
                    </p>
                    {promptHint(s.systemPrompt) ? (
                      <p className="mt-1 line-clamp-2 text-2xs leading-relaxed text-faint">
                        {promptHint(s.systemPrompt)}
                      </p>
                    ) : null}
                  </button>
                </li>
              ))}
            </ul>
        </aside>

        <section className="min-w-0">
          {editing === undefined || editing === "new" ? (
            editing === "new" ? (
            <PlaybookEditor
              key="new"
              embedded
              playbook={undefined}
              saving={createPb.isPending}
              onSubmit={(draft) => {
                setSaveError("");
                createPb.mutate(draft);
              }}
              onCancel={() => {
                setEditing(undefined);
                setSaveError("");
              }}
              {...editorProps}
            />
            ) : (
            <div className="rounded-xl border border-dashed border-border px-6 py-16 text-center">
              <FileCode2 className="mx-auto size-8 text-faint" />
              <p className="mt-3 text-sm text-muted">Select a playbook, or create one.</p>
            </div>
            )
          ) : (
            <PlaybookEditor
              key={editing.name}
              embedded
              playbook={selected}
              saving={updatePb.isPending}
              deleting={deletePb.isPending}
              onSubmit={(draft) => {
                setSaveError("");
                updatePb.mutate(draft);
              }}
              onDelete={
                selected
                  ? () => deletePb.mutate(selected.name)
                  : undefined
              }
              onCancel={() => {
                setEditing(undefined);
                setSaveError("");
              }}
              {...editorProps}
            />
          )}
        </section>
      </div>
      ) : null}
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
    repos: draft.repos.map((r) => ({ name: r.name, url: r.url, defaultBranch: r.defaultBranch })),
    git: draft.git,
    slackChannels: draft.slackChannels,
    linear: draft.linear,
    interactive: draft.interactive,
    docker: draft.docker,
    browser: draft.browser,
    skills: draft.skills,
    mcpServers: draft.mcpServers,
    env: draft.env,
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
