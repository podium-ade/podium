import { useState } from "react";
import type { ReactNode } from "react";
import { FileCode2, Undo2 } from "lucide-react";
import type { AgentBackend, AgentProfile } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { AgentPicker } from "./AgentPicker";
import { relative } from "../../lib/format";
import { cn } from "../../lib/utils";
import { Badge, Chip } from "../Badge";
import { Skeleton } from "../Skeleton";
import { Button } from "../ui/button";
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "../ui/card";
import { Input } from "../ui/input";
import { Label } from "../ui/label";

/** The profile.yaml keys a browser may override, as the API names them. */
export const FIELDS = {
  displayName: "display_name",
  model: "model",
  agent: "agent",
  effort: "effort",
  defaultPlaybook: "default_playbook",
} as const;

export type ProfileFields = {
  displayName: string;
  model: string;
  agent: string;
  effort: string;
  defaultPlaybook: string;
};

export type ProfileCardProps = {
  profile?: AgentProfile;
  /** The backend catalogue, for the model picker. Empty while it loads. */
  agents: AgentBackend[];
  loading?: boolean;
  saving?: boolean;
  onSave: (fields: ProfileFields) => void;
};

/**
 * ProfileCard edits the ASSISTANT: the thing a conversation talks to, answering in the
 * conductor's own process. Its name and its model are here, and so — read-only — is what it
 * may execute. Which playbook a piece of work runs is the host's decision, not a setting.
 *
 * Every editable field is an **override** of profile.yaml, not a replacement for it: the
 * file stays on the conductor's host and stays the default, and an empty field means "use
 * what the file says". That is why each row shows the file's value beside the input — an
 * operator has to be able to see what they are overriding, and get back to it in one click.
 */
export function ProfileCard({ profile, agents, loading, saving, onSave }: ProfileCardProps) {
  const overridden = new Set(profile?.overridden ?? []);
  const held = (key: string, effective: string) => (overridden.has(key) ? effective : "");

  const [displayName, setDisplayName] = useState(() =>
    held(FIELDS.displayName, profile?.displayName ?? ""),
  );
  // agent, model and effort are one control and therefore one piece of state. They are also
  // three independent overrides on the wire, so an operator who overrides only the effort
  // still gets the file's model — which is why the choice seeds from each field's own
  // override rather than from the effective profile.
  const [choice, setChoice] = useState<AgentChoice>(() => ({
    agent: held(FIELDS.agent, profile?.agent ?? ""),
    model: held(FIELDS.model, profile?.model ?? ""),
    effort: held(FIELDS.effort, profile?.effort ?? ""),
  }));
  // default_playbook is still an override the API accepts, but it is not a setting here: the
  // host picks a playbook per piece of work. The current override is sent back unchanged so
  // saving the name or the model cannot silently clear it.
  const defaultPlaybook = held(FIELDS.defaultPlaybook, profile?.defaultPlaybook ?? "");

  if (loading) {
    return (
      <Card className="max-w-4xl" aria-busy="true">
        <CardHeader>
          <div>
            <Skeleton className="h-4 w-32" />
            <Skeleton className="mt-2 h-3 w-56" />
          </div>
        </CardHeader>
        <CardContent className="space-y-5">
          {[0, 1, 2].map((i) => (
            <div key={i} className="space-y-2">
              <Skeleton className="h-3 w-28" />
              <Skeleton className="h-9 w-full max-w-sm" />
              <Skeleton className="h-2.5 w-44" />
            </div>
          ))}
        </CardContent>
      </Card>
    );
  }

  const overrideCount = [...overridden].filter((k) => k !== FIELDS.defaultPlaybook).length;

  return (
    <form
      data-testid="profile-card"
      onSubmit={(e) => {
        e.preventDefault();
        onSave({
          displayName,
          model: choice.model,
          agent: choice.agent,
          effort: choice.effort,
          defaultPlaybook,
        });
      }}
    >
      <Card className="max-w-4xl">
        <CardHeader>
          <div>
            <CardTitle>{profile?.name || "—"}</CardTitle>
            <p className="text-xs text-muted">
              Loaded from <code className="font-mono text-fg">{profile?.profileDir || "—"}</code>
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <Badge tone={overrideCount > 0 ? "warn" : "idle"}>
              {overrideCount === 0
                ? "no overrides"
                : `${overrideCount} ${overrideCount === 1 ? "override" : "overrides"}`}
            </Badge>
            {profile?.updatedBy ? (
              <Chip>
                changed by {profile.updatedBy}
                {profile.updatedAt ? ` · ${relative(profile.updatedAt)}` : ""}
              </Chip>
            ) : null}
          </div>
        </CardHeader>

        <CardContent className="pb-0">
          <p className="max-w-2xl pb-1 text-xs leading-relaxed text-muted">
            The assistant answers a conversation in the conductor&apos;s own process, and starts
            a task from one of your playbooks when the work needs a machine. Its name and its
            prompt come from <code className="font-mono">profile.yaml</code> and are not editable
            here — the name labels every session already recorded. The fields below are
            overrides: clear one and the file&apos;s value applies again.
          </p>

          <Field
            id="profile-display-name"
            label="Display name"
            fileValue={profile?.fileDisplayName ?? ""}
            overridden={overridden.has(FIELDS.displayName)}
            onUseFile={() => setDisplayName("")}
            hint="What it calls itself in a chat header and in Slack."
          >
            <Input
              id="profile-display-name"
              value={displayName}
              onChange={(e) => setDisplayName(e.target.value)}
              placeholder={profile?.fileDisplayName || "the file's value"}
              className="h-8 max-w-sm text-xs"
            />
          </Field>

          <Field
            label="Model"
            fileValue={fileTriple(profile)}
            overridden={
              overridden.has(FIELDS.model) ||
              overridden.has(FIELDS.agent) ||
              overridden.has(FIELDS.effort)
            }
            onUseFile={() => setChoice(INHERIT)}
            hint="What the assistant answers on, and what a playbook runs on unless it names its own."
          >
            <AgentPicker
              label="Profile"
              value={choice}
              onChange={setChoice}
              agents={agents}
              inherit={{ label: "Use profile.yaml's", hint: "the file's value" }}
              inherited={{
                agent: profile?.fileAgent ?? "",
                model: profile?.fileModel ?? "",
                effort: profile?.fileEffort ?? "",
              }}
            />
          </Field>

          {/* Read-only, and deliberately: what the thing running beside the master key may
              execute belongs in a repository, beside a review, rather than behind a form. */}
          <Field
            label="Skills and turn cap"
            fileValue=""
            overridden={false}
            hint="What the assistant may execute, and how many steps one of its turns gets. profile.yaml only — a browser cannot change either."
          >
            <p className="text-xs text-fg">
              {(profile?.skills ?? []).length === 0 ? (
                <span className="text-muted">no skills</span>
              ) : (
                <span className="font-mono">{(profile?.skills ?? []).join(", ")}</span>
              )}
              <span className="text-muted"> · </span>
              {/* Unset is a decision and not a blank: the assistant delegates, so the cap
                  worth having is on the container it starts. */}
              {profile?.maxTurns ? (
                <>
                  <span className="tabular">{profile.maxTurns}</span>
                  <span className="text-muted"> turns</span>
                </>
              ) : (
                <span className="text-muted">no turn limit</span>
              )}
            </p>
          </Field>
        </CardContent>

        <CardFooter>
          <Button type="submit" size="sm" disabled={saving}>
            {saving ? "Saving…" : "Save profile"}
          </Button>
          <span className="text-xs text-muted">It applies to the next turn.</span>
        </CardFooter>
      </Card>
    </form>
  );
}

/** fileTriple is what profile.yaml says about the backend, on one line. */
function fileTriple(profile?: AgentProfile): string {
  const parts = [profile?.fileAgent, profile?.fileModel, profile?.fileEffort].filter(Boolean);
  return parts.join(" · ");
}

/**
 * Field is the file-versus-override distinction, made structural: the control holds the
 * override, and the line under it always says where the value in force actually comes from.
 *
 * An overridden row is tinted and carries the revert; an inherited one says "from
 * profile.yaml" and nothing else, because there is nothing to undo.
 */
function Field({
  id,
  label,
  fileValue,
  overridden,
  hint,
  onUseFile,
  children,
}: {
  /** The control's id, when it has one — the label points at it. */
  id?: string;
  label: string;
  fileValue: string;
  overridden: boolean;
  hint?: string;
  /** Absent for a row profile.yaml owns outright, which has no override to undo. */
  onUseFile?: () => void;
  children: ReactNode;
}) {
  return (
    <div
      className={cn(
        "-mx-5 grid gap-x-6 gap-y-2 border-t border-hairline px-5 py-4 sm:grid-cols-[15rem_1fr]",
        overridden && "bg-warn/4",
      )}
    >
      <div className="space-y-1.5">
        <div className="flex flex-wrap items-center gap-2">
          {id ? (
            <Label htmlFor={id}>{label}</Label>
          ) : (
            <span className="text-xs font-medium text-muted">{label}</span>
          )}
          {overridden ? <Badge tone="warn">overriding the file</Badge> : null}
        </div>
        {hint ? <p className="text-2xs leading-relaxed text-faint">{hint}</p> : null}
      </div>
      <div className="min-w-0 space-y-2">
        <div className="flex flex-wrap items-center gap-2">{children}</div>
        <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-2xs text-faint">
          <FileCode2 aria-hidden className="size-3.5 shrink-0" />
          {onUseFile === undefined ? (
            <span>
              from <code className="font-mono">profile.yaml</code>
            </span>
          ) : overridden ? (
            <>
              <span>
                <code className="font-mono">profile.yaml</code> says{" "}
                {fileValue ? <span className="text-muted">{fileValue}</span> : <span>nothing</span>}
              </span>
              <Button type="button" variant="ghost" size="xs" onClick={onUseFile}>
                <Undo2 />
                Use the file value
              </Button>
            </>
          ) : (
            <span>
              in force: {fileValue ? <span className="text-muted">{fileValue}</span> : "unset"}, from{" "}
              <code className="font-mono">profile.yaml</code>
            </span>
          )}
        </p>
      </div>
    </div>
  );
}


