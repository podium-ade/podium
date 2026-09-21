import { useState } from "react";
import { FileCode2, Pencil, Plus, Puzzle, Trash2 } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { AgentSkill } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { humanBytes } from "../../lib/format";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageHeader } from "../PageHeader";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { Tooltip } from "../ui/tooltip";
import { ConductorDown } from "./ConductorDown";
import type { SkillBundle } from "../../lib/skillZip";
import { SkillEditor, type SkillFile } from "./SkillEditor";

/**
 * SkillsPanel is the Agent Skills on this conductor. A folder that contains SKILL.md
 * installs as one skill, other files included. A folder of markdown files and no
 * SKILL.md installs one skill per file.
 */
export function SkillsPanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<AgentSkill | "new">();
  const [saveError, setSaveError] = useState("");

  const list = useQuery({ queryKey: ["agent", "skills"], queryFn: () => agent.listSkills({}) });
  const skills = list.data?.skills ?? [];
  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "skills"] });

  const upload = useMutation({
    mutationFn: (v: { content: Uint8Array; filename: string; replace: boolean }) => agent.uploadSkill(v),
  });
  const remove = useMutation({
    mutationFn: (name: string) => agent.deleteSkill({ name }),
    onSuccess: async (_res, name) => {
      toast(`${name} removed.`, "ok");
      await reload();
    },
    onError: (err) => toast(errorMessage(err)),
  });

  async function saveOne(file: SkillFile, replace: boolean) {
    await saveUpload(
      { content: new TextEncoder().encode(file.markdown), filename: file.name, replace },
      file.name,
    );
  }

  async function saveBundles(bundles: SkillBundle[], replace: boolean) {
    await saveEach(
      bundles.map((b) => ({ content: b.content, filename: b.filename, replace })),
      replace,
    );
  }

  async function createMany(files: SkillFile[]) {
    await saveEach(
      files.map((file) => ({
        content: new TextEncoder().encode(file.markdown),
        filename: file.name,
        replace: false,
      })),
      false,
    );
  }

  async function saveUpload(
    v: { content: Uint8Array; filename: string; replace: boolean },
    fallbackName: string,
  ) {
    await saveEach([v], v.replace, fallbackName);
  }

  async function saveEach(
    items: { content: Uint8Array; filename: string; replace: boolean }[],
    replace: boolean,
    fallbackName?: string,
  ) {
    setSaveError("");
    let ok = 0;
    let lastName = fallbackName ?? "";
    let lastErr = "";
    for (const item of items) {
      try {
        const res = await upload.mutateAsync(item);
        lastName = res.skill?.name ?? item.filename;
        ok++;
      } catch (err) {
        lastErr = errorMessage(err);
      }
    }
    if (ok > 0) {
      const noun = ok === 1 ? lastName : `${ok} skills`;
      toast(replace ? `${noun} saved.` : `${noun} added.`, "ok");
      setEditing(undefined);
      await reload();
    }
    if (lastErr) setSaveError(lastErr);
  }

  if (list.isError && !isAgentUnreachable(list.error)) {
    return <Empty icon={Puzzle} title="Could not read the skills" hint={errorMessage(list.error)} />;
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Skills"
        description="A folder the model can load mid-turn: SKILL.md and the files it names. A playbook names which skills a turn may use."
        meta={
          skills.length > 0 ? (
            <Chip className="tabular">
              {skills.length} {skills.length === 1 ? "skill" : "skills"}
            </Chip>
          ) : undefined
        }
        actions={
          <Button
            type="button"
            size="sm"
            data-testid="skill-new"
            onClick={() => {
              setSaveError("");
              setEditing("new");
            }}
          >
            <Plus />
            New skill
          </Button>
        }
      />

      {list.isError && isAgentUnreachable(list.error) ? (
        <ConductorDown
          what="The skills could not be read"
          onRetry={() => void list.refetch()}
          retrying={list.isFetching}
        />
      ) : null}

      {editing ? (
        <SkillEditor
          key={editing === "new" ? "new" : editing.name}
          existing={
            editing === "new"
              ? undefined
              : {
                  name: editing.name,
                  markdown: editing.markdown,
                  fileCount: editing.fileCount,
                  files: editing.files,
                }
          }
          saving={upload.isPending}
          deleting={remove.isPending}
          error={saveError}
          onSubmit={(file) => void saveOne(file, editing !== "new")}
          onSubmitMany={(files) => void createMany(files)}
          onSubmitBundles={(bundles) => void saveBundles(bundles, editing !== "new")}
          onDelete={
            editing === "new"
              ? undefined
              : () => {
                  remove.mutate(editing.name, {
                    onSuccess: () => setEditing(undefined),
                  });
                }
          }
          onCancel={() => {
            setEditing(undefined);
            setSaveError("");
          }}
        />
      ) : null}

      {list.isPending && !editing ? <SkillsSkeleton /> : null}

      {!list.isPending && !editing && skills.length === 0 ? (
        <Empty
          icon={Puzzle}
          title="No skills"
          hint="Nothing for a turn to pick up yet."
          action={
            <Button type="button" size="sm" onClick={() => setEditing("new")}>
              <Plus />
              New skill
            </Button>
          }
        />
      ) : null}

      {!editing && skills.length > 0 ? (
        <ul className="space-y-2.5">
          {skills.map((s) => (
            <SkillRow
              key={s.name}
              skill={s}
              deleting={remove.isPending}
              onOpen={() => {
                setSaveError("");
                setEditing(s);
              }}
              onDelete={() => remove.mutate(s.name)}
            />
          ))}
        </ul>
      ) : null}
    </div>
  );
}

function SkillRow({
  skill,
  deleting,
  onOpen,
  onDelete,
}: {
  skill: AgentSkill
  deleting: boolean
  onOpen: () => void
  onDelete: () => void
}) {
  return (
    <li
      data-testid="skill-row"
      className="rounded-xl border border-border bg-card px-3 py-2.5 shadow-xs transition-colors hover:border-accent/40 hover:bg-raised/40"
    >
      <div className="flex items-start gap-2">
        <button
          type="button"
          data-testid="skill-open"
          className="min-w-0 flex-1 cursor-pointer space-y-1.5 rounded-lg px-2 py-1.5 text-left transition-colors hover:bg-raised/60 focus-visible:bg-raised/60 focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none active:bg-raised"
          onClick={onOpen}
        >
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-medium text-fg">{skill.name}</span>
            <Badge tone="idle" dot={false}>
              <FileCode2 className="size-3" />
              host directory
            </Badge>
          </div>
          {skill.description ? (
            <p className="max-w-2xl text-xs leading-relaxed text-muted">{skill.description}</p>
          ) : null}
          {skill.problem ? (
            <p className="max-w-2xl text-xs leading-relaxed text-err">{skill.problem}</p>
          ) : null}
        </button>
        <div className="flex shrink-0 items-center gap-1 pt-1">
          <Button
            type="button"
            variant="outline"
            size="sm"
            data-testid="skill-edit"
            aria-label={`Edit ${skill.name}`}
            onClick={onOpen}
          >
            <Pencil />
            Edit
          </Button>
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            data-testid="skill-delete"
            aria-label={`Delete ${skill.name}`}
            disabled={deleting}
            onClick={onDelete}
            className="hover:bg-err/12 hover:text-err"
          >
            <Trash2 />
          </Button>
        </div>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-hairline pt-2.5">
        <Chip className="tabular">{humanBytes(skill.sizeBytes)}</Chip>
        <Chip className="tabular">
          {skill.fileCount} {skill.fileCount === 1 ? "file" : "files"}
        </Chip>
        {skill.sha256 ? (
          <Tooltip label={skill.sha256}>
            <Chip className="font-mono">{skill.sha256.slice(0, 12)}…</Chip>
          </Tooltip>
        ) : null}
        {skill.playbooks.length > 0 ? (
          <Chip>{skill.playbooks.map((p) => `/${p}`).join(" ")}</Chip>
        ) : (
          <Chip>no playbook names it</Chip>
        )}
      </div>
    </li>
  );
}

function SkillsSkeleton() {
  return (
    <ul aria-busy="true" aria-label="Loading" className="space-y-2.5">
      {[0, 1, 2].map((i) => (
        <li key={i} className="space-y-3 rounded-xl border border-border bg-card px-4 py-3.5">
          <Skeleton className="h-3.5 w-1/3" />
          <Skeleton className="h-3 w-3/5" />
          <div className="flex gap-2">
            <Skeleton className="h-4 w-16" />
            <Skeleton className="h-4 w-14" />
            <Skeleton className="h-4 w-28" />
          </div>
        </li>
      ))}
    </ul>
  );
}
