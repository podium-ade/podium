import { useState } from "react";
import type { ReactNode } from "react";
import type { AgentProfile } from "../../gen/podium/agent/v1/agent_pb";
import { relative } from "../../lib/format";
import { Badge } from "../Badge";
import { Skeleton } from "../Skeleton";

/** The four profile.yaml keys a browser may override, as the API names them. */
export const FIELDS = {
  displayName: "display_name",
  model: "model",
  defaultSkill: "default_skill",
  chatDefaultSkill: "chat_default_skill",
} as const;

export type ProfileFields = {
  displayName: string;
  model: string;
  defaultSkill: string;
  chatDefaultSkill: string;
};

export type ProfileCardProps = {
  profile?: AgentProfile;
  /** The skills that are actually loaded, for the two default pickers. */
  skills: string[];
  loading?: boolean;
  saving?: boolean;
  onSave: (fields: ProfileFields) => void;
};

/**
 * ProfileCard edits the bot's identity.
 *
 * Every field here is an **override** of profile.yaml, not a replacement for it: the file
 * stays on the conductor's host and stays the default, and an empty field means "use what
 * the file says". That is why each row shows the file's value beside the input — an
 * operator has to be able to see what they are overriding, and get back to it in one click.
 */
export function ProfileCard({ profile, skills, loading, saving, onSave }: ProfileCardProps) {
  const overridden = new Set(profile?.overridden ?? []);
  const held = (key: string, effective: string) => (overridden.has(key) ? effective : "");

  const [displayName, setDisplayName] = useState(() =>
    held(FIELDS.displayName, profile?.displayName ?? ""),
  );
  const [model, setModel] = useState(() => held(FIELDS.model, profile?.model ?? ""));
  const [defaultSkill, setDefaultSkill] = useState(() =>
    held(FIELDS.defaultSkill, profile?.defaultSkill ?? ""),
  );
  const [chatDefaultSkill, setChatDefaultSkill] = useState(() =>
    held(FIELDS.chatDefaultSkill, profile?.chatDefaultSkill ?? ""),
  );

  if (loading) {
    return (
      <section className="space-y-3 rounded border border-border bg-panel p-4" aria-busy="true">
        <Skeleton className="h-5 w-40" />
        <Skeleton className="h-8 w-full" />
        <Skeleton className="h-8 w-full" />
      </section>
    );
  }

  return (
    <form
      data-testid="profile-card"
      className="space-y-4 rounded border border-border bg-panel p-4"
      onSubmit={(e) => {
        e.preventDefault();
        onSave({ displayName, model, defaultSkill, chatDefaultSkill });
      }}
    >
      <header className="flex flex-wrap items-baseline gap-3">
        <h2 className="text-sm font-semibold">{profile?.name || "—"}</h2>
        <span className="text-xs text-muted">
          Loaded from <code className="font-mono text-fg">{profile?.profileDir || "—"}</code>
        </span>
        {profile?.updatedBy ? (
          <span className="ml-auto text-xs text-muted">
            changed by {profile.updatedBy}
            {profile.updatedAt ? ` · ${relative(profile.updatedAt)}` : ""}
          </span>
        ) : null}
      </header>

      <p className="max-w-2xl text-xs text-muted">
        The profile&apos;s name and system prompt come from{" "}
        <code className="font-mono">profile.yaml</code> and are not editable here — the name
        labels every session already recorded. The four fields below are overrides: clear one
        and the file&apos;s value applies again.
      </p>

      <Field
        label="Display name"
        fileValue={profile?.fileDisplayName ?? ""}
        overridden={overridden.has(FIELDS.displayName)}
        onUseFile={() => setDisplayName("")}
      >
        <input
          aria-label="Display name"
          value={displayName}
          onChange={(e) => setDisplayName(e.target.value)}
          placeholder={profile?.fileDisplayName || "the file's value"}
          className="w-full max-w-sm rounded border border-border bg-bg px-2 py-1 text-xs outline-none focus:border-accent"
        />
      </Field>

      <Field
        label="Model"
        fileValue={profile?.fileModel ?? ""}
        overridden={overridden.has(FIELDS.model)}
        onUseFile={() => setModel("")}
      >
        <input
          aria-label="Model"
          value={model}
          onChange={(e) => setModel(e.target.value)}
          placeholder={profile?.fileModel || "the file's value"}
          className="w-full max-w-sm rounded border border-border bg-bg px-2 py-1 font-mono text-xs outline-none focus:border-accent"
        />
      </Field>

      <Field
        label="Default skill"
        fileValue={profile?.fileDefaultSkill ?? ""}
        overridden={overridden.has(FIELDS.defaultSkill)}
        onUseFile={() => setDefaultSkill("")}
        hint="What runs when no chip, no slash prefix and no channel picks a skill."
      >
        <SkillSelect
          label="Default skill"
          value={defaultSkill}
          skills={skills}
          fileValue={profile?.fileDefaultSkill ?? ""}
          onChange={setDefaultSkill}
        />
      </Field>

      <Field
        label="Chat default skill"
        fileValue={profile?.fileChatDefaultSkill ?? ""}
        overridden={overridden.has(FIELDS.chatDefaultSkill)}
        onUseFile={() => setChatDefaultSkill("")}
        hint="What a web chat starts with. It is a preference: a slash prefix a human types still wins."
      >
        <SkillSelect
          label="Chat default skill"
          value={chatDefaultSkill}
          skills={skills}
          fileValue={profile?.fileChatDefaultSkill ?? ""}
          onChange={setChatDefaultSkill}
        />
      </Field>

      <button
        type="submit"
        disabled={saving}
        className="rounded bg-accent px-3 py-1.5 text-xs font-medium text-bg disabled:opacity-50"
      >
        {saving ? "Saving…" : "Save profile"}
      </button>
    </form>
  );
}

function Field({
  label,
  fileValue,
  overridden,
  hint,
  onUseFile,
  children,
}: {
  label: string;
  fileValue: string;
  overridden: boolean;
  hint?: string;
  onUseFile: () => void;
  children: ReactNode;
}) {
  return (
    <div className="space-y-1 text-xs">
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-fg">{label}</span>
        {overridden ? <Badge tone="warn">overriding the file</Badge> : null}
      </div>
      <div className="flex flex-wrap items-center gap-2">
        {children}
        {overridden ? (
          <button
            type="button"
            onClick={onUseFile}
            className="rounded border border-border px-2 py-1 text-muted hover:text-fg"
          >
            Use the file value
          </button>
        ) : null}
      </div>
      <p className="text-muted">
        <code className="font-mono">profile.yaml</code>:{" "}
        {fileValue ? <span className="text-fg">{fileValue}</span> : <span>unset</span>}
        {hint ? ` · ${hint}` : null}
      </p>
    </div>
  );
}

function SkillSelect({
  label,
  value,
  skills,
  fileValue,
  onChange,
}: {
  label: string;
  value: string;
  skills: string[];
  fileValue: string;
  onChange: (v: string) => void;
}) {
  // A skill that is no longer loaded must still be selectable to be seen; dropping it would
  // silently rewrite the override the moment the form is saved.
  const options = skills.includes(value) || value === "" ? skills : [value, ...skills];
  return (
    <select
      aria-label={label}
      value={value}
      onChange={(e) => onChange(e.target.value)}
      className="w-full max-w-sm rounded border border-border bg-bg px-2 py-1 font-mono text-xs outline-none focus:border-accent"
    >
      <option value="">{fileValue ? `the file's value (${fileValue})` : "the file's value"}</option>
      {options.map((s) => (
        <option key={s} value={s}>
          {s}
        </option>
      ))}
    </select>
  );
}
