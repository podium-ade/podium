import { FileCode2, Puzzle } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { AgentSkill } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { humanBytes } from "../../lib/format";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageHeader } from "../PageHeader";
import { Skeleton } from "../Skeleton";
import { Alert } from "../ui/alert";
import { Tooltip } from "../ui/tooltip";
import { ConductorDown } from "./ConductorDown";

/**
 * SkillsPanel lists the Agent Skills on the conductor's host. They are directories with a
 * SKILL.md under PODIUM_AGENT_SKILLS_DIR. This screen does not write one.
 */
export function SkillsPanel() {
  const list = useQuery({ queryKey: ["agent", "skills"], queryFn: () => agent.listSkills({}) });
  const skills = list.data?.skills ?? [];

  if (list.isError && !isAgentUnreachable(list.error)) {
    return <Empty icon={Puzzle} title="Could not read the skills" hint={errorMessage(list.error)} />;
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Skills"
        description={
          <>
            An Agent Skill is a <code className="font-mono">SKILL.md</code> and its files: a
            procedure somebody else wrote, which the model loads when a task matches its
            description. A playbook names the ones its turns may load, and names nothing by
            default.
          </>
        }
        meta={
          skills.length > 0 ? (
            <Chip className="tabular">
              {skills.length} {skills.length === 1 ? "skill" : "skills"}
            </Chip>
          ) : undefined
        }
      />

      {list.isError && isAgentUnreachable(list.error) ? (
        <ConductorDown
          what="The skills could not be read"
          onRetry={() => void list.refetch()}
          retrying={list.isFetching}
        />
      ) : null}

      <Alert variant="warn" role="note" title="A skill runs in the turn's container with the turn's credentials">
        It is instructions and shell commands the model is told to follow, sitting beside that
        turn's GitHub token and model credential. The digest below proves the bytes a turn gets
        are the bytes on disk; it proves nothing about who wrote them. Read a skill before you
        grant it to a playbook a public channel can reach.
      </Alert>

      {list.isPending ? <SkillsSkeleton /> : null}

      {!list.isPending && skills.length === 0 ? (
        <Empty
          icon={Puzzle}
          title="No skills"
          hint={
            list.data?.skillsDir
              ? `Put a directory with a SKILL.md in ${list.data.skillsDir} on the conductor's host.`
              : "This conductor has no PODIUM_AGENT_SKILLS_DIR, so it delivers none."
          }
        />
      ) : null}

      {skills.length > 0 ? (
        <ul className="space-y-2.5">
          {skills.map((s) => (
            <SkillRow key={s.name} skill={s} />
          ))}
        </ul>
      ) : null}

      {list.data?.skillsDir ? (
        <p className="text-2xs leading-relaxed text-faint">
          This conductor reads <code className="font-mono">{list.data.skillsDir}</code> on its
          own host. Those are files: they cannot be changed here, and a playbook that names a
          skill not in that directory fails that turn.
        </p>
      ) : null}
    </div>
  );
}

function SkillRow({ skill }: { skill: AgentSkill }) {
  return (
    <li
      data-testid="skill-row"
      className="rounded-xl border border-border bg-card px-4 py-3.5 shadow-xs"
    >
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="min-w-0 flex-1 space-y-1.5">
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
