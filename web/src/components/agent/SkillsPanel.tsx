import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { SkillDefinition } from "../../gen/podium/agent/v1/agent_pb";
import { useAgents } from "../../hooks/useAgents";
import { agent, errorMessage, isAgentUnreachable, secrets } from "../../lib/client";
import { relative } from "../../lib/format";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { TableSkeleton } from "../Skeleton";
import { useToast } from "../Toast";
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

  return (
    <div className="space-y-4">
      {isAgentUnreachable(profile.error) ? (
        <p className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
          podium-agent is not reachable. Check its /readyz on PODIUM_AGENT_LISTEN.
        </p>
      ) : null}

      {profile.data?.staleReason ? (
        <p className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
          The conductor is running an older profile than this database holds, because the
          stored one would not load: {profile.data.staleReason}
        </p>
      ) : null}

      <div className="flex flex-wrap items-center gap-3">
        <h1 className="text-base font-semibold">Skills</h1>
        <button
          type="button"
          data-testid="skill-new"
          onClick={() => {
            setSaveError(undefined);
            setEditing({});
          }}
          className="ml-auto rounded-md bg-primary px-3 py-1.5 text-xs font-medium text-primary-foreground"
        >
          New skill
        </button>
      </div>

      <p className="max-w-3xl text-xs text-muted">
        A skill is one job the bot can do, and its image is the unit of capability: what a
        turn can reach at all is decided by what is in the image. A skill saved here takes
        effect on the <span className="text-fg">next turn, with no restart</span> — the
        conductor rebuilds its profile on every write. A{" "}
        <code className="font-mono">skills/&lt;name&gt;.yaml</code> in the profile directory
        is read at start-up only, so changing one of those still needs a restart.
      </p>

      {profile.isPending ? <TableSkeleton rows={3} cols={2} /> : null}

      {!profile.isPending && running.length === 0 ? (
        <Empty title="No skills" hint="Nothing is loaded. Create one above." />
      ) : null}

      <ul className="space-y-2">
        {running.map((s) => (
          <li
            key={`${s.origin}:${s.name}`}
            data-testid="skill-row"
            className="rounded border border-border bg-panel px-3 py-2"
          >
            <div className="flex flex-wrap items-start gap-3">
              <div className="min-w-0 flex-1 space-y-1">
                <p
                  className="truncate font-mono text-sm text-fg"
                  title={s.image}
                  data-testid="skill-image"
                >
                  {s.image}
                </p>
                <p className="text-xs">
                  <span className="font-mono text-accent">/{s.name}</span>
                  {s.model ? <span className="text-muted"> · {s.model}</span> : null}
                </p>
                {promptHint(s.systemPrompt) ? (
                  <p className="text-xs text-muted">{promptHint(s.systemPrompt)}</p>
                ) : null}
              </div>
              {/* A file skill opens too, read-only. It cannot be changed here — the files
                  win — but "you may not edit this" and "you may not look at this" are very
                  different rules, and only the first one was ever intended. */}
              <button
                type="button"
                aria-label={`${s.editable ? "Edit" : "View"} ${s.name}`}
                onClick={() => {
                  setSaveError(undefined);
                  setEditing({ skill: s });
                }}
                className="rounded border border-border px-2 py-1 text-xs text-muted hover:text-fg"
              >
                {s.editable ? "Edit" : "View"}
              </button>
            </div>

            <div className="mt-2 flex flex-wrap items-center gap-1.5">
              {s.editable ? null : <Badge tone="idle">file · read-only</Badge>}
              {s.name === p?.defaultSkill ? <Badge tone="ok">default skill</Badge> : null}
              {s.name === chatDefault ? <Chip>chat default</Chip> : null}
              {s.linear ? <Chip>Linear tickets</Chip> : null}
              <Chip>
                {s.allowedTools.length} {s.allowedTools.length === 1 ? "tool" : "tools"}
              </Chip>
              <Chip>
                {s.maxTurns} turns · {s.timeout || "default"}
              </Chip>
              {s.slackChannels.map((c) => (
                <Chip key={c}>#{c}</Chip>
              ))}
              {s.secrets.map((sec) => (
                <span
                  key={`${sec.name}:${sec.key}`}
                  data-testid="skill-secret"
                  className="inline-block rounded bg-raised px-1.5 py-0.5 font-mono text-xs text-muted"
                >
                  {sec.name}
                </span>
              ))}
              {s.updatedBy ? (
                <span className="ml-auto text-xs text-muted">
                  by {s.updatedBy}
                  {s.updatedAt ? ` · ${relative(s.updatedAt)}` : ""}
                </span>
              ) : null}
            </div>

            {s.editable ? null : (
              <p className="mt-2 text-xs text-muted">
                Defined by{" "}
                <code className="font-mono text-fg">skills/{s.name}.yaml</code> on the
                conductor&apos;s host. Change it by editing that file and restarting
                podium-agent; this screen will not write over it.
              </p>
            )}
          </li>
        ))}
      </ul>

      {shadowed.length > 0 ? (
        <section className="space-y-2">
          <h2 className="text-sm font-medium text-warn">Shadowed</h2>
          <p className="max-w-3xl text-xs text-muted">
            A file of the same name defines these, and the files win. They never run. Open one
            to see what it holds and delete it, or rename the file.
          </p>
          <ul className="space-y-2">
            {shadowed.map((s) => (
              <li
                key={`shadowed:${s.name}`}
                data-testid="skill-shadowed"
                className="rounded border border-warn/40 bg-warn/10 px-3 py-2"
              >
                <div className="flex flex-wrap items-center gap-3">
                  <div className="min-w-0 flex-1">
                    <p className="truncate font-mono text-sm text-fg">{s.image}</p>
                    <p className="text-xs text-warn">
                      <span className="font-mono">/{s.name}</span> never runs:{" "}
                      <code className="font-mono">skills/{s.name}.yaml</code> defines the same
                      name and the file wins.
                    </p>
                  </div>
                  <button
                    type="button"
                    aria-label={`Review ${s.name}`}
                    onClick={() => {
                      setSaveError(undefined);
                      setEditing({ skill: s });
                    }}
                    className="rounded border border-border px-2 py-1 text-xs text-muted hover:text-fg"
                  >
                    Review
                  </button>
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
