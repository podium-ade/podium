import { useState } from "react";
import { Database, FileCode2, Plus, Sparkles } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { SkillDefinition } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import { agent, errorMessage, isAgentUnreachable, secrets } from "../../lib/client";
import { relative } from "../../lib/format";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageHeader } from "../PageHeader";
import { TableSkeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { ConductorDown } from "./ConductorDown";
import { SkillEditor, type SkillDraft } from "./SkillEditor";

/** How much of a prompt's first line the list shows. */
const HINT_CHARS = 160;

type Editing = { skill?: SkillDefinition } | undefined;

/**
 * SkillsPanel is where a skill is defined: its image, its prompt, its tools, its limits and
 * the secrets it names — in the browser, instead of a YAML file on the conductor's host.
 *
 * Two kinds of skill live here and they are not equal. A `skills/<name>.yaml` in the profile
 * directory is authoritative for its name: it is read-only on this screen, and a stored skill
 * of the same name is shadowed and never runs. Everything else is stored in the conductor's
 * database and is editable here.
 *
 * The list never deletes. Deleting a skill is only offered inside the editor, where the
 * definition being destroyed is on the screen with the button.
 */
export function SkillsPanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<Editing>();
  const [saveError, setSaveError] = useState<string>();

  const profile = useQuery({
    queryKey: ["agent", "profile"],
    queryFn: () => agent.getProfile({}),
  });
  // The existing secrets surface, reused rather than rebuilt: names and metadata only, which
  // is all ListSecrets carries and all this screen ever needs.
  const secretList = useQuery({
    queryKey: ["secrets"],
    queryFn: () => secrets.listSecrets({}),
  });
  const { agents } = useAgents();

  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "profile"] });

  const create = useMutation({
    mutationFn: (skill: SkillDraft) => agent.createSkill({ skill }),
    onSuccess: async (_res, skill) => {
      toast(`${skill.name} created. It runs on the next turn.`, "ok");
      setEditing(undefined);
      setSaveError(undefined);
      await reload();
    },
    onError: (err) => setSaveError(errorMessage(err)),
  });

  const update = useMutation({
    mutationFn: (skill: SkillDraft) => agent.updateSkill({ skill }),
    onSuccess: async (_res, skill) => {
      toast(`${skill.name} saved. It runs on the next turn.`, "ok");
      setEditing(undefined);
      setSaveError(undefined);
      await reload();
    },
    onError: (err) => setSaveError(errorMessage(err)),
  });

  const remove = useMutation({
    mutationFn: (name: string) => agent.deleteSkill({ name }),
    onSuccess: async (_res, name) => {
      toast(`${name} deleted.`, "ok");
      setEditing(undefined);
      setSaveError(undefined);
      await reload();
    },
    // A refusal keeps the operator in the editor, beside the skill it is about, rather than
    // in a toast over a list the skill is still in.
    onError: (err) => setSaveError(errorMessage(err)),
  });

  if (profile.isError && !isAgentUnreachable(profile.error)) {
    return <Empty title="Could not read the agent profile" hint={errorMessage(profile.error)} />;
  }

  if (editing) {
    const saving = create.isPending || update.isPending;
    const target = editing.skill;
    return (
      <SkillEditor
        skill={target}
        readOnly={target !== undefined && !target.editable}
        agents={agents}
        profileDefault={{
          agent: profile.data?.profile?.agent ?? "",
          model: profile.data?.profile?.model ?? "",
          effort: profile.data?.profile?.effort ?? "",
        }}
        secretNames={(secretList.data?.secrets ?? []).map((s) => s.name)}
        secretsUnknown={secretList.isError || secretList.isPending}
        saving={saving}
        deleting={remove.isPending}
        error={saveError}
        onSubmit={(draft) => {
          setSaveError(undefined);
          if (target) update.mutate(draft);
          else create.mutate(draft);
        }}
        onDelete={
          target
            ? () => {
                setSaveError(undefined);
                remove.mutate(target.name);
              }
            : undefined
        }
        onCancel={() => {
          setSaveError(undefined);
          setEditing(undefined);
        }}
      />
    );
  }

  const all = profile.data?.skills ?? [];
  const running = all.filter((s) => !s.shadowed);
  const shadowed = all.filter((s) => s.shadowed);
  const p = profile.data?.profile;
  const chatDefault = p?.chatDefaultSkill || p?.defaultSkill || "";

  const open = (skill: SkillDefinition | undefined) => {
    setSaveError(undefined);
    setEditing({ skill });
  };

  return (
    <div className="space-y-5">
      <PageHeader
        title="Skills"
        description="A skill is one job the bot can do, and its image is the unit of capability: what a turn can reach at all is decided by what is in the image."
        actions={
          <Button type="button" size="sm" data-testid="skill-new" onClick={() => open(undefined)}>
            <Plus />
            New skill
          </Button>
        }
      />

      {isAgentUnreachable(profile.error) ? (
        <ConductorDown
          what="The skills could not be read"
          onRetry={() => void profile.refetch()}
          retrying={profile.isFetching}
        />
      ) : null}

      {profile.data?.staleReason ? (
        <Alert variant="warn" title="The conductor is running an older profile than this database holds">
          The stored one would not load: {profile.data.staleReason}
        </Alert>
      ) : null}

      <Alert variant="info" title="A skill saved here takes effect on the next turn, with no restart">
        The conductor rebuilds its profile on every write. A{" "}
        <code className="font-mono">skills/&lt;name&gt;.yaml</code> in the profile directory is
        read at start-up only, so changing one of those still needs a restart.
      </Alert>

      {profile.isPending ? <TableSkeleton rows={3} cols={3} /> : null}

      {!profile.isPending && running.length === 0 ? (
        <Empty
          icon={Sparkles}
          title="No skills"
          hint="Nothing is loaded, so the bot has no job it can do. Create one, or drop a skills/<name>.yaml in the profile directory and restart the conductor."
          action={
            <Button type="button" size="sm" onClick={() => open(undefined)}>
              <Plus />
              New skill
            </Button>
          }
        />
      ) : null}

      <ul className="space-y-2.5">
        {running.map((s) => (
          <li
            key={`${s.origin}:${s.name}`}
            data-testid="skill-row"
            className="rounded-xl border border-border bg-card px-4 py-3.5 shadow-xs"
          >
            <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
              <div className="min-w-0 flex-1 space-y-1.5">
                <div className="flex flex-wrap items-center gap-2">
                  <span className="font-mono text-sm font-medium text-fg">/{s.name}</span>
                  <Provenance skill={s} />
                  {s.name === p?.defaultSkill ? <Badge tone="ok">default skill</Badge> : null}
                  {s.name === chatDefault ? <Chip>chat default</Chip> : null}
                  {s.linear ? <Chip>Linear tickets</Chip> : null}
                </div>
                {/* The image is the unit of capability: what a turn of this skill can do at
                    all is decided by what is in the image, before any prompt or tool list is
                    read. */}
                <p
                  className="truncate font-mono text-xs text-muted"
                  title={s.image}
                  data-testid="skill-image"
                >
                  {s.image}
                </p>
                {promptHint(s.systemPrompt) ? (
                  <p className="max-w-2xl text-xs leading-relaxed text-muted">
                    {promptHint(s.systemPrompt)}
                  </p>
                ) : null}
              </div>
              {/* A file skill opens too, read-only. It cannot be changed here — the files
                  win — but "you may not edit this" and "you may not look at this" are very
                  different rules, and only the first one was ever intended. */}
              <Button
                type="button"
                variant="outline"
                size="sm"
                aria-label={`${s.editable ? "Edit" : "View"} ${s.name}`}
                onClick={() => open(s)}
              >
                {s.editable ? "Edit" : "View"}
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
                  data-testid="skill-secret"
                  className="inline-flex w-fit items-center rounded-md border border-border bg-raised/70 px-1.5 py-0.5 font-mono text-2xs whitespace-nowrap text-muted"
                >
                  {sec.name}
                </span>
              ))}
              {s.updatedBy ? (
                <span className="ml-auto shrink-0 text-2xs text-faint">
                  by {s.updatedBy}
                  {s.updatedAt ? ` · ${relative(s.updatedAt)}` : ""}
                </span>
              ) : null}
            </div>

            {s.editable ? null : (
              <p className="mt-2.5 text-2xs leading-relaxed text-faint">
                Defined by <code className="font-mono text-muted">skills/{s.name}.yaml</code> on
                the conductor&apos;s host. Change it by editing that file and restarting
                podium-agent; this screen will not write over it.
              </p>
            )}
          </li>
        ))}
      </ul>

      {shadowed.length > 0 ? (
        <section className="space-y-2.5">
          <div className="space-y-1">
            <h2 className="text-sm font-semibold text-fg">Shadowed</h2>
            <p className="max-w-3xl text-xs leading-relaxed text-muted">
              A file of the same name defines these, and the files win. They never run. Open
              one to see what it holds and delete it, or rename the file.
            </p>
          </div>
          <ul className="space-y-2.5">
            {shadowed.map((s) => (
              <li
                key={`shadowed:${s.name}`}
                data-testid="skill-shadowed"
                className="rounded-xl border border-warn/40 bg-warn/8 px-4 py-3.5"
              >
                <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
                  <div className="min-w-0 flex-1 space-y-1.5">
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-mono text-sm font-medium text-fg">/{s.name}</span>
                      <Badge tone="warn">never runs</Badge>
                    </div>
                    <p className="truncate font-mono text-xs text-muted" title={s.image}>
                      {s.image}
                    </p>
                    <p className="text-xs leading-relaxed text-warn">
                      <code className="font-mono">skills/{s.name}.yaml</code> defines the same
                      name and the file wins, so this stored definition is dead weight.
                    </p>
                  </div>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    aria-label={`Review ${s.name}`}
                    onClick={() => open(s)}
                  >
                    Review
                  </Button>
                </div>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </div>
  );
}

/**
 * Provenance is the one thing about a skill that is genuinely confusing, so it is said in
 * words rather than left to be inferred from whether the button says Edit or View.
 */
function Provenance({ skill }: { skill: SkillDefinition }) {
  if (!skill.editable) {
    return (
      <Badge tone="idle" dot={false}>
        <FileCode2 aria-hidden className="size-3" />
        file · read-only
      </Badge>
    );
  }
  if (skill.origin === "file") {
    return (
      <Badge tone="idle" dot={false}>
        <FileCode2 aria-hidden className="size-3" />
        file
      </Badge>
    );
  }
  return (
    <Badge tone="idle" dot={false}>
      <Database aria-hidden className="size-3" />
      database
    </Badge>
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
