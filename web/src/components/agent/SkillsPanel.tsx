import { useMemo, useState, type MouseEvent } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Plus, Puzzle, Search, SearchX } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { AgentSkill } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, humanBytes, relative } from "../../lib/format";
import type { SkillBundle } from "../../lib/skillZip";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageFrame } from "../PageHeader";
import { TableSkeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { Tooltip } from "../ui/tooltip";
import { ConductorDown } from "./ConductorDown";
import { RowActions } from "./RowActions";
import { SkillEditor, type SkillFile } from "./SkillEditor";
import { useViewer } from "../../lib/identity";
import { canManageInfra } from "../../lib/rbac";

const LIST = "/agent/skills";

function skillPath(name: string): string {
  return `${LIST}/${encodeURIComponent(name)}`;
}

function useSkills() {
  return useQuery({ queryKey: ["agent", "skills"], queryFn: () => agent.listSkills({}) });
}

/**
 * SkillsPanel is the Agent Skills on this conductor, one row each, the way Tasks lists tasks.
 * A row opens the skill on its own page (SkillPage). A folder that contains SKILL.md installs
 * as one skill, other files included; a folder of markdown files and no SKILL.md installs one
 * skill per file.
 */
export function SkillsPanel() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const navigate = useNavigate();
  const qc = useQueryClient();
  const list = useSkills();
  const [filter, setFilter] = useState("");

  const all = useMemo(() => list.data?.skills ?? [], [list.data]);
  const rows = useMemo(() => {
    const q = filter.trim().toLowerCase();
    if (!q) return all;
    return all.filter((s) =>
      [s.name, s.description, ...(s.playbooks ?? [])].some((v) => (v ?? "").toLowerCase().includes(q)),
    );
  }, [all, filter]);

  const deleteSkill = async (name: string) => {
    await agent.deleteSkill({ name });
    await qc.invalidateQueries({ queryKey: ["agent", "skills"] });
  };

  if (list.isError && !isAgentUnreachable(list.error)) {
    return (
      <PageFrame title="Skills">
        <Empty icon={Puzzle} title="Could not read the skills" hint={errorMessage(list.error)} />
      </PageFrame>
    );
  }

  const openRow = (e: MouseEvent<HTMLTableRowElement>, name: string) => {
    // The name cell holds the real link; anything that is already a control keeps its click.
    if ((e.target as HTMLElement).closest("a,button")) return;
    void navigate(skillPath(name));
  };

  return (
    <PageFrame
      title="Skills"
      lead="Reusable know-how your assistants pick up when a job calls for it."
      actions={
        manage ? (
          <Button type="button" size="sm" data-testid="skill-new" onClick={() => void navigate(`${LIST}/new`)}>
            <Plus />
            New skill
          </Button>
        ) : null
      }
    >
      {list.isError && isAgentUnreachable(list.error) ? (
        <ConductorDown
          what="The skills could not be read"
          onRetry={() => void list.refetch()}
          retrying={list.isFetching}
        />
      ) : null}

      {list.isPending ? <TableSkeleton rows={4} cols={4} /> : null}

      {!list.isPending && !list.isError && all.length === 0 ? (
        <Empty
          icon={Puzzle}
          title="No skills"
          hint="Nothing for a turn to pick up yet."
          action={
            manage ? (
              <Button type="button" size="sm" onClick={() => void navigate(`${LIST}/new`)}>
                <Plus />
                New skill
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
              aria-label="Filter skills"
              placeholder="Filter by name, description or playbook"
              value={filter}
              onChange={(e) => setFilter(e.target.value)}
              className="pl-9"
            />
          </div>

          {rows.length === 0 ? (
            <Empty
              icon={SearchX}
              title="No skill matches"
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
                  <TableHead>Skill</TableHead>
                  <TableHead>Size</TableHead>
                  <TableHead>Used by</TableHead>
                  <TableHead>Updated</TableHead>
                  <TableHead pinned />
                </TableRow>
              </TableHeader>
              <TableBody>
                {rows.map((s) => (
                  <TableRow
                    key={`${s.name}:${s.origin}:${s.shadowed}`}
                    data-testid="skill-row"
                    onClick={manage ? (e) => openRow(e, s.name) : undefined}
                    className={manage ? "h-[4.5rem] cursor-pointer" : "h-[4.5rem]"}
                  >
                    <TableCell className="max-w-[32rem]">
                      <div className="flex items-center gap-2">
                        {manage ? (
                          <Link
                            to={skillPath(s.name)}
                            aria-label={`Edit ${s.name}`}
                            className="rounded-sm font-mono text-sm font-medium text-fg outline-none hover:underline focus-visible:ring-2 focus-visible:ring-ring/50"
                          >
                            {s.name}
                          </Link>
                        ) : (
                          <span className="font-mono text-sm font-medium text-fg">{s.name}</span>
                        )}
                        {s.problem ? (
                          <Badge tone="err" dot={false}>
                            Won&apos;t load
                          </Badge>
                        ) : null}
                        {s.shadowed ? (
                          <Badge tone="warn" dot={false}>
                            Shadowed
                          </Badge>
                        ) : null}
                      </div>
                      {s.description ? (
                        <p className="mt-0.5 truncate text-xs text-muted" title={s.description}>
                          {s.description}
                        </p>
                      ) : null}
                      {s.problem ? <p className="mt-0.5 text-xs text-err">{s.problem}</p> : null}
                    </TableCell>
                    <TableCell className="tabular text-xs whitespace-nowrap text-muted">
                      {s.fileCount} {s.fileCount === 1 ? "file" : "files"}
                      <div className="text-2xs text-faint">{humanBytes(s.sizeBytes)}</div>
                    </TableCell>
                    <TableCell>
                      <UsedBy playbooks={s.playbooks ?? []} />
                    </TableCell>
                    <TableCell
                      className="text-xs whitespace-nowrap text-muted"
                      title={s.uploadedAt ? absolute(s.uploadedAt) : undefined}
                    >
                      {s.uploadedAt ? relative(s.uploadedAt) : "-"}
                      {s.uploadedBy ? <div className="truncate text-2xs text-faint">{s.uploadedBy}</div> : null}
                    </TableCell>
                    <TableCell pinned className="w-0 pr-2 text-right">
                      <RowActions
                        noun="skill"
                        name={s.name}
                        href={manage ? skillPath(s.name) : undefined}
                        testId="skill"
                        onDelete={manage ? () => deleteSkill(s.name) : undefined}
                        deleteDescription={deleteText(s)}
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
 * SkillPage is one skill, or a new one at /agent/skills/new: the editor on its own page, with
 * the list one click back.
 */
export function SkillPage() {
  const { name: rawName } = useParams();
  const creating = rawName === undefined;
  const name = rawName ?? "";
  const qc = useQueryClient();
  const toast = useToast();
  const navigate = useNavigate();
  const list = useSkills();
  const [saveError, setSaveError] = useState("");

  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "skills"] });
  const back = () => void navigate(LIST);

  const upload = useMutation({
    mutationFn: (v: { content: Uint8Array; filename: string; replace: boolean }) => agent.uploadSkill(v),
  });
  const remove = useMutation({
    mutationFn: (n: string) => agent.deleteSkill({ name: n }),
    onSuccess: async (_res, n) => {
      toast(`${n} removed.`, "ok");
      await reload();
      void navigate(LIST, { replace: true });
    },
    onError: (err) => setSaveError(errorMessage(err)),
  });

  async function saveEach(
    items: { content: Uint8Array; filename: string; replace: boolean }[],
    replace: boolean,
  ) {
    setSaveError("");
    const saved: string[] = [];
    let lastErr = "";
    for (const item of items) {
      try {
        const res = await upload.mutateAsync(item);
        saved.push(res.skill?.name ?? item.filename);
      } catch (err) {
        lastErr = errorMessage(err);
      }
    }
    if (saved.length > 0) {
      const noun = saved.length === 1 ? saved[0] : `${saved.length} skills`;
      toast(replace ? `${noun} saved.` : `${noun} added.`, "ok");
      await reload();
      // One skill lands on its own page; several go back to the list that now shows them.
      if (creating && !lastErr) {
        void navigate(saved.length === 1 ? skillPath(saved[0]!) : LIST, { replace: true });
      }
    }
    if (lastErr) setSaveError(lastErr);
  }

  const encode = (file: SkillFile, replace: boolean) => ({
    content: new TextEncoder().encode(file.markdown),
    filename: file.name,
    replace,
  });

  const all = list.data?.skills ?? [];
  const skill = creating
    ? undefined
    : all.find((s) => s.name === name && !s.shadowed) ?? all.find((s) => s.name === name);

  const frame = {
    title: creating ? "New skill" : name,
    back: { to: LIST, label: "Skills" },
    meta: skill ? <SkillMeta skill={skill} /> : undefined,
  };

  if (list.isError) {
    return (
      <PageFrame {...frame}>
        {isAgentUnreachable(list.error) ? (
          <ConductorDown
            what="The skill could not be read"
            onRetry={() => void list.refetch()}
            retrying={list.isFetching}
          />
        ) : (
          <Empty icon={Puzzle} title="Could not read the skills" hint={errorMessage(list.error)} />
        )}
      </PageFrame>
    );
  }

  if (list.isPending) {
    return (
      <PageFrame {...frame}>
        <TableSkeleton rows={6} cols={2} />
      </PageFrame>
    );
  }

  if (!creating && !skill) {
    return (
      <PageFrame {...frame}>
        <Empty
          icon={Puzzle}
          title="No such skill"
          hint={`Nothing on this conductor is called ${name}. It may have been deleted or renamed.`}
          action={
            <Button type="button" size="sm" variant="outline" onClick={back}>
              Back to skills
            </Button>
          }
        />
      </PageFrame>
    );
  }

  const replace = !creating;
  return (
    <PageFrame {...frame}>
      {skill?.problem ? <p className="text-sm text-err">{skill.problem}</p> : null}
      <SkillEditor
        key={creating ? "new" : name}
        embedded
        existing={
          skill
            ? { name: skill.name, markdown: skill.markdown, fileCount: skill.fileCount, files: skill.files }
            : undefined
        }
        saving={upload.isPending}
        deleting={remove.isPending}
        error={saveError}
        onSubmit={(file) => void saveEach([encode(file, replace)], replace)}
        onSubmitMany={(files) => void saveEach(files.map((f) => encode(f, false)), false)}
        onSubmitBundles={(bundles: SkillBundle[]) =>
          void saveEach(
            bundles.map((b) => ({ content: b.content, filename: b.filename, replace })),
            replace,
          )
        }
        onDelete={skill ? () => remove.mutate(skill.name) : undefined}
        onCancel={back}
      />
    </PageFrame>
  );
}

function SkillMeta({ skill }: { skill: AgentSkill }) {
  return (
    <>
      {skill.sha256 ? (
        <Tooltip label={skill.sha256}>
          <Chip className="hidden font-mono sm:inline-flex">{skill.sha256.slice(0, 12)}…</Chip>
        </Tooltip>
      ) : null}
      {skill.shadowed ? (
        <Badge tone="warn" dot={false}>
          Shadowed
        </Badge>
      ) : null}
    </>
  );
}

function UsedBy({ playbooks }: { playbooks: string[] }) {
  if (playbooks.length === 0) return <span className="text-xs text-faint">No playbook</span>;
  return (
    <div className="flex flex-wrap gap-1">
      {playbooks.map((p) => (
        <Chip key={p} className="font-mono">
          /{p}
        </Chip>
      ))}
    </div>
  );
}

function deleteText(s: AgentSkill): string {
  const named = s.playbooks ?? [];
  const who =
    named.length === 0
      ? "No playbook names it."
      : `${named.map((p) => `/${p}`).join(", ")} ${named.length === 1 ? "names" : "name"} it and will fail ${named.length === 1 ? "its" : "their"} next turn until the name is removed.`;
  return `Its directory and every file in it go. ${who} There is no undo.`;
}
