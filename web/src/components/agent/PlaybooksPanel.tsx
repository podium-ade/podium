import { FileCode2, Sparkles } from "lucide-react";
import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import type { PlaybookDefinition } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import { agent, errorMessage, isAgentUnreachable, secrets } from "../../lib/client";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageHeader } from "../PageHeader";
import { TableSkeleton } from "../Skeleton";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { ConductorDown } from "./ConductorDown";
import { PlaybookEditor } from "./PlaybookEditor";
import { ReloadProfileDirButton } from "./ReloadProfileDirButton";

/** How much of a prompt's first line the list shows. */
const HINT_CHARS = 160;

/**
 * PlaybooksPanel lists the playbooks in the profile directory. They are files on the
 * conductor's host — edit the YAML and press Re-read the files. This screen does not
 * write one.
 */
export function PlaybooksPanel() {
  const [viewing, setViewing] = useState<PlaybookDefinition>();

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

  if (profile.isError && !isAgentUnreachable(profile.error)) {
    return <Empty title="Could not read the agent profile" hint={errorMessage(profile.error)} />;
  }

  if (viewing) {
    return (
      <PlaybookEditor
        playbook={viewing}
        readOnly
        agents={agents}
        profileDefault={{
          agent: profile.data?.profile?.agent ?? "",
          model: profile.data?.profile?.model ?? "",
          effort: profile.data?.profile?.effort ?? "",
        }}
        secretNames={(secretList.data?.secrets ?? []).map((s) => s.name)}
        secretsUnknown={secretList.isError || secretList.isPending}
        skillNames={(skillList.data?.skills ?? []).map((s) => s.name)}
        skillsUnknown={skillList.isError || skillList.isPending}
        mcpNames={(mcpList.data?.servers ?? []).map((m) => m.name)}
        disabledMcpNames={(mcpList.data?.servers ?? []).filter((m) => !m.enabled).map((m) => m.name)}
        mcpUnknown={mcpList.isError || mcpList.isPending}
        onSubmit={() => undefined}
        onCancel={() => setViewing(undefined)}
      />
    );
  }

  const running = profile.data?.playbooks ?? [];
  const p = profile.data?.profile;

  return (
    <div className="space-y-5">
      <PageHeader
        title="Playbooks"
        description="A playbook is a machine job the assistant can start: an image, a workspace and the tools that come with them. It picks one per piece of work — a conversation never runs one directly."
        actions={<ReloadProfileDirButton />}
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

      <Alert variant="info" title="Playbooks are files on the conductor's host">
        Each one is a <code className="font-mono">playbooks/&lt;name&gt;.yaml</code> in the
        profile directory. Edit the file and press <span className="font-medium">Re-read the
        files</span> — no restart.
      </Alert>

      {profile.isPending ? <TableSkeleton rows={3} cols={3} /> : null}

      {!profile.isPending && running.length === 0 ? (
        <Empty
          icon={Sparkles}
          title="No playbooks"
          hint="Nothing is loaded, so the bot has no job it can do. Drop a playbooks/<name>.yaml in the profile directory and re-read the files."
        />
      ) : null}

      <ul className="space-y-2.5">
        {running.map((s) => (
          <li
            key={s.name}
            data-testid="playbook-row"
            className="rounded-xl border border-border bg-card px-4 py-3.5 shadow-xs"
          >
            <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
              <div className="min-w-0 flex-1 space-y-1.5">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-sm font-medium text-fg">/{s.name}</span>
                  <Badge tone="idle" dot={false}>
                    <FileCode2 aria-hidden className="size-3" />
                    file
                  </Badge>
                  {s.name === p?.defaultPlaybook ? <Badge tone="ok">default playbook</Badge> : null}
                  {s.linear ? <Chip>Linear tickets</Chip> : null}
                </div>
                <p
                  className="truncate font-mono text-xs text-muted"
                  title={s.image}
                  data-testid="playbook-image"
                >
                  {s.image}
                </p>
                {promptHint(s.systemPrompt) ? (
                  <p className="max-w-2xl text-xs leading-relaxed text-muted">
                    {promptHint(s.systemPrompt)}
                  </p>
                ) : null}
              </div>
              <Button
                type="button"
                variant="outline"
                size="sm"
                aria-label={`View ${s.name}`}
                onClick={() => setViewing(s)}
              >
                View
              </Button>
            </div>

            <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-hairline pt-2.5">
              {s.model ? <Chip className="font-mono">{s.model}</Chip> : <Chip>the profile&apos;s model</Chip>}
              {s.effort ? <Chip>{s.effort} effort</Chip> : null}
              <Chip>
                {s.allowedTools.length} {s.allowedTools.length === 1 ? "tool" : "tools"}
              </Chip>
              <Chip className="tabular">
                {s.maxTurns} turns · {s.timeout || "default"}
              </Chip>
              {s.slackChannels.map((c) => (
                <Chip key={c}>#{c}</Chip>
              ))}
              {s.secrets.map((sec) => (
                <span
                  key={`${sec.name}:${sec.key}`}
                  data-testid="playbook-secret"
                  className="inline-flex w-fit items-center rounded-md border border-border bg-raised/70 px-1.5 py-0.5 font-mono text-2xs whitespace-nowrap text-muted"
                >
                  {sec.name}
                </span>
              ))}
            </div>

            <p className="mt-2.5 text-2xs leading-relaxed text-faint">
              Defined by <code className="font-mono text-muted">playbooks/{s.name}.yaml</code> on
              the conductor&apos;s host. Change it by editing that file and pressing Re-read the
              files.
            </p>
          </li>
        ))}
      </ul>
    </div>
  );
}

/**
 * promptHint is the first line of a prompt that says something — the same rule the conductor
 * applies for the chat chip, because "# The analyst" says less than the sentence under it.
 */
function promptHint(prompt: string): string {
  for (const raw of prompt.split("\n")) {
    const line = raw.trim();
    if (line === "" || line.startsWith("#")) continue;
    return line.length > HINT_CHARS ? `${line.slice(0, HINT_CHARS)}…` : line;
  }
  return "";
}
