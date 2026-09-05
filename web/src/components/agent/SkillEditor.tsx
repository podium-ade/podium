import { useMemo, useState } from "react";
import { Link } from "react-router";
import type { AgentBackend, SkillDefinition } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { AgentPicker } from "./AgentPicker";

/** The same expression the conductor holds a skill name to. A name is also typed after a slash in Slack. */
export const SKILL_NAME_RE = /^[a-z][a-z0-9-]{0,31}$/;

/** SkillDraft is what the editor hands back: the request message, in plain fields. */
export type SkillDraft = {
  name: string;
  image: string;
  systemPrompt: string;
  allowedTools: string[];
  maxTurns: number;
  timeout: string;
  model: string;
  agent: string;
  effort: string;
  labels: string[];
  resources: { cpu: number; memoryMb: number; pids: number };
  secrets: { name: string; target: string; key: string }[];
  repos: { name: string; url: string; defaultBranch: string }[];
  slackChannels: string[];
  linear: boolean;
  env: Record<string, string>;
};

export type SkillEditorProps = {
  /** Undefined creates; a definition edits it. Only a stored skill is ever passed. */
  skill?: SkillDefinition;
  /** The backend catalogue, for the model picker. Empty while it loads. */
  agents: AgentBackend[];
  /** What this skill runs on when it names nothing: the profile's own triple. */
  profileDefault: AgentChoice;
  /** The names SecretService already holds, for the picker. Never a value: there is no read API. */
  secretNames: string[];
  /** True when the secret list could not be read, so "not registered" cannot be claimed. */
  secretsUnknown?: boolean;
  saving?: boolean;
  deleting?: boolean;
  /**
   * readOnly disables every control. The only thing that reaches it is a SHADOWED stored
   * skill: it never runs, and writing to it would write to the half that is not in force.
   * A file skill is not read-only — saving one rewrites its skills/<name>.yaml.
   */
  readOnly?: boolean;
  /** The server's refusal, shown verbatim: its rules are the only rules. */
  error?: string;
  onSubmit: (draft: SkillDraft) => void;
  /** Deletes the skill being edited. Absent while creating: there is nothing to delete. */
  onDelete?: () => void;
  onCancel: () => void;
};

type Row = { id: number; a: string; b: string; c: string };

// A monotonic id per row, so React keys survive a row being removed from the middle. It is
// module level rather than a ref because a ref may not be read while rendering.
let nextRowId = 0;
const row = (a = "", b = "", c = ""): Row => ({ id: nextRowId++, a, b, c });

const FIELD =
  "w-full rounded border border-border bg-bg px-2 py-1 font-mono text-xs outline-none focus:border-accent";

/**
 * SkillEditor is the whole of a skill in one form: which image runs it, what it is told,
 * which tools it may use, and which stored secrets it names.
 *
 * Naming a secret here is not a privilege the editor hands out. A task spec names secrets
 * the same way and nothing in Podium authorises which names a caller may use — so there is
 * no allow-list here, and adding one would only be theatre. What the picker does is stop a
 * typo: a skill naming a secret the control plane does not hold fails admission on its first
 * turn, and saying so now is cheaper than finding out in a thread.
 */
export function SkillEditor({
  skill,
  agents,
  profileDefault,
  secretNames,
  secretsUnknown,
  saving,
  deleting,
  error,
  onSubmit,
  onDelete,
  onCancel,
  readOnly,
}: SkillEditorProps) {
  const creating = skill === undefined;
  // A shadowed row is a stored skill a skills/<name>.yaml has since claimed. The conductor
  // refuses to write over one, so the form shows what it holds and offers only the delete.
  const shadowed = skill?.shadowed ?? false;

  const [name, setName] = useState(skill?.name ?? "");
  const [image, setImage] = useState(skill?.image ?? "");
  const [prompt, setPrompt] = useState(skill?.systemPrompt ?? "");
  const [tools, setTools] = useState((skill?.allowedTools ?? []).join("\n"));
  const [maxTurns, setMaxTurns] = useState(String(skill?.maxTurns || 50));
  const [timeoutText, setTimeoutText] = useState(skill?.timeout || "30m");
  const [choice, setChoice] = useState<AgentChoice>(() =>
    skill
      ? { agent: skill.agent, model: skill.model, effort: skill.effort }
      : INHERIT,
  );
  const [labels, setLabels] = useState((skill?.labels ?? []).join(", "));
  const [channels, setChannels] = useState((skill?.slackChannels ?? []).join(", "));
  const [linear, setLinear] = useState(skill?.linear ?? false);
  const [cpu, setCpu] = useState(String(skill?.resources?.cpu ?? ""));
  const [memoryMb, setMemoryMb] = useState(String(skill?.resources?.memoryMb ?? ""));
  const [pids, setPids] = useState(String(skill?.resources?.pids ?? ""));
  const [secretRows, setSecretRows] = useState<Row[]>(() =>
    (skill?.secrets ?? []).map((s) => row(s.name, s.target || "env", s.key)),
  );
  const [envRows, setEnvRows] = useState<Row[]>(() =>
    Object.entries(skill?.env ?? {}).map(([k, v]) => row(k, v)),
  );
  const [repoRows, setRepoRows] = useState<Row[]>(() =>
    (skill?.repos ?? []).map((r) => row(r.name, r.url, r.defaultBranch)),
  );
  const [tried, setTried] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  const registered = useMemo(() => new Set(secretNames), [secretNames]);

  const toolList = splitLines(tools);
  const nameOk = SKILL_NAME_RE.test(name);
  const problems: string[] = [];
  if (!nameOk) problems.push("name");
  if (image.trim() === "") problems.push("image");
  if (prompt.trim() === "") problems.push("prompt");
  if (toolList.length === 0) problems.push("tools");

  function submit() {
    setTried(true);
    if (problems.length > 0 || saving) return;
    onSubmit({
      name: name.trim(),
      image: image.trim(),
      systemPrompt: prompt,
      allowedTools: toolList,
      maxTurns: Number(maxTurns) || 0,
      timeout: timeoutText.trim(),
      model: choice.model.trim(),
      agent: choice.agent,
      effort: choice.effort,
      labels: splitList(labels),
      resources: { cpu: Number(cpu) || 0, memoryMb: Number(memoryMb) || 0, pids: Number(pids) || 0 },
      secrets: secretRows
        .filter((r) => r.a.trim() !== "")
        .map((r) => ({ name: r.a.trim(), target: r.b || "env", key: r.c.trim() })),
      repos: repoRows
        .filter((r) => r.a.trim() !== "" || r.b.trim() !== "")
        .map((r) => ({ name: r.a.trim(), url: r.b.trim(), defaultBranch: r.c.trim() })),
      slackChannels: splitList(channels),
      linear,
      env: Object.fromEntries(
        envRows.filter((r) => r.a.trim() !== "").map((r) => [r.a.trim(), r.b]),
      ),
    });
  }

  return (
    <form
      data-testid="skill-editor"
      className="space-y-4 rounded border border-border bg-panel p-4"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="flex flex-wrap items-center gap-3">
        <h2 className="text-sm font-semibold">
          {creating
            ? "New skill"
            : shadowed
              ? `${skill.name} · shadowed`
              : readOnly
                ? skill.name
                : `Edit ${skill.name}`}
        </h2>
        <span className="text-xs text-muted">
          {shadowed ? (
            <>
              A stored skill a file of the same name overrides. It never runs, so there is
              nothing here to change — deleting it is what this screen is for.
            </>
          ) : skill?.origin === "file" ? (
            <>
              Saving writes{" "}
              <code className="font-mono">skills/{skill.name}.yaml</code> on the
              conductor&apos;s host. The file&apos;s comments are not preserved, and a
              deployment that redeploys that directory will overwrite what you save.
            </>
          ) : (
            <>
              Stored in the conductor&apos;s database and validated by exactly the rules a{" "}
              <code className="font-mono">skills/&lt;name&gt;.yaml</code> is held to.
            </>
          )}
        </span>
      </div>

      {skill && shadowed ? (
        <p className="rounded border border-warn/40 bg-warn/10 px-3 py-2 text-xs text-warn">
          <code className="font-mono">skills/{skill.name}.yaml</code> on the conductor&apos;s
          host defines this name and the file wins, so this stored definition never runs and
          cannot be written over. It is shown so you can see what deleting it throws away.
        </p>
      ) : null}

      {/* One fieldset rather than a disabled prop on every input: a shadowed skill and a
          file skill are both read-only as a whole, and no field of either could usefully be
          changed. It disables the picker's buttons too, which a per-input prop would miss. */}
      <fieldset disabled={shadowed || readOnly} className="space-y-4">
        {/* The image is the unit of capability: what a turn of this skill can do at all is
            decided by what is in the image, before any prompt or tool list is read. */}
        <label className="flex flex-col gap-1 text-xs">
          <span className="text-fg">Image</span>
          <input
            aria-label="Image"
            value={image}
            onChange={(e) => setImage(e.target.value)}
            placeholder="ghcr.io/example/my-agent-runtime:latest"
            className="w-full max-w-2xl rounded border border-border bg-bg px-2 py-1.5 font-mono text-sm outline-none focus:border-accent"
          />
          <span className="max-w-2xl text-muted">
            Any image you supply. It has to implement the turn-brief protocol — read the brief
            off <code className="font-mono">PODIUM_AGENT_TURN</code> and emit the runner&apos;s
            message events — and{" "}
            <code className="font-mono">FROM ghcr.io/alvaroibarguen/podium-agent-runtime</code>{" "}
            is the easy way to get one that does. Podium does not pick an image for you.
          </span>
          {tried && image.trim() === "" ? <span className="text-err">An image is required.</span> : null}
        </label>

        <div className="grid gap-3 sm:grid-cols-2">
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-fg">Name</span>
            <input
              aria-label="Skill name"
              value={name}
              disabled={!creating}
              onChange={(e) => setName(e.target.value)}
              placeholder="reporter"
              className={`${FIELD} max-w-sm disabled:opacity-60`}
            />
            <span className="text-muted">
              {creating
                ? "Lower case, digits and dashes; this is what a human types after a slash in Slack."
                : "A skill keeps the name it was created with."}
            </span>
            {tried && !nameOk ? (
              <span className="text-err">A name must match ^[a-z][a-z0-9-]{"{0,31}"}$.</span>
            ) : null}
          </label>

          <div className="flex flex-col gap-1 text-xs">
            <span className="text-fg">Agent and model</span>
            <AgentPicker
              label="Skill"
              value={choice}
              onChange={setChoice}
              agents={agents}
              inherit={{ label: "Inherit from the profile", hint: "whatever the profile is set to" }}
              inherited={profileDefault}
            />
          </div>
        </div>

        <label className="flex flex-col gap-1 text-xs">
          <span className="text-fg">System prompt</span>
          <textarea
            aria-label="System prompt"
            value={prompt}
            onChange={(e) => setPrompt(e.target.value)}
            rows={8}
            spellCheck={false}
            placeholder="What this skill is for, and how it should behave."
            className={FIELD}
          />
          <span className="text-muted">
            The prompt itself, not a path. <code className="font-mono">file:</code> only works in
            a YAML file, which has a directory beside it to resolve against.
          </span>
          {tried && prompt.trim() === "" ? (
            <span className="text-err">A prompt is required.</span>
          ) : null}
        </label>

        <div className="grid gap-3 sm:grid-cols-2">
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-fg">Allowed tools</span>
            <textarea
              aria-label="Allowed tools"
              value={tools}
              onChange={(e) => setTools(e.target.value)}
              rows={5}
              spellCheck={false}
              placeholder={"Read\nGrep\nGlob\nBash"}
              className={FIELD}
            />
            <span className="text-muted">One per line. At least one is required.</span>
            {tried && toolList.length === 0 ? (
              <span className="text-err">Name at least one tool.</span>
            ) : null}
          </label>

          <div className="space-y-3">
            <div className="grid grid-cols-2 gap-3">
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-fg">Max turns</span>
                <input
                  aria-label="Max turns"
                  type="number"
                  min={1}
                  value={maxTurns}
                  onChange={(e) => setMaxTurns(e.target.value)}
                  className={FIELD}
                />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-fg">Timeout</span>
                <input
                  aria-label="Timeout"
                  value={timeoutText}
                  onChange={(e) => setTimeoutText(e.target.value)}
                  placeholder="30m"
                  className={FIELD}
                />
              </label>
            </div>
            <div className="grid grid-cols-3 gap-3">
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-fg">CPU</span>
                <input
                  aria-label="CPU"
                  type="number"
                  step="0.5"
                  min={0}
                  value={cpu}
                  onChange={(e) => setCpu(e.target.value)}
                  className={FIELD}
                />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-fg">Memory MB</span>
                <input
                  aria-label="Memory MB"
                  type="number"
                  min={0}
                  value={memoryMb}
                  onChange={(e) => setMemoryMb(e.target.value)}
                  className={FIELD}
                />
              </label>
              <label className="flex flex-col gap-1 text-xs">
                <span className="text-fg">PIDs</span>
                <input
                  aria-label="PIDs"
                  type="number"
                  min={0}
                  value={pids}
                  onChange={(e) => setPids(e.target.value)}
                  className={FIELD}
                />
              </label>
            </div>
            <p className="text-xs text-muted">Blank or zero leaves the resource uncapped.</p>
          </div>
        </div>

        <section className="space-y-2">
          <h3 className="text-xs font-medium text-fg">Secrets</h3>
          <p className="max-w-2xl text-xs text-muted">
            A skill names a stored secret; it never holds one. The value is written on the{" "}
            <Link to="/secrets" className="text-accent hover:underline">
              Secrets
            </Link>{" "}
            screen, encrypted by podium-server, and handed straight to the container that runs
            the turn. Nothing in this UI can read one back.
          </p>
          <datalist id="podium-secret-names">
            {secretNames.map((n) => (
              <option key={n} value={n} />
            ))}
          </datalist>
          {secretRows.map((r, i) => {
            const missing = r.a.trim() !== "" && !secretsUnknown && !registered.has(r.a.trim());
            return (
              <div key={r.id} className="space-y-1" data-testid="secret-row">
                <div className="flex flex-wrap items-center gap-2">
                  <input
                    aria-label={`Secret name ${i + 1}`}
                    list="podium-secret-names"
                    value={r.a}
                    onChange={(e) => setSecretRows(patch(secretRows, r.id, { a: e.target.value }))}
                    placeholder="podium.agent.github_token"
                    className={`${FIELD} max-w-xs`}
                  />
                  <select
                    aria-label={`Secret target ${i + 1}`}
                    value={r.b || "env"}
                    onChange={(e) => setSecretRows(patch(secretRows, r.id, { b: e.target.value }))}
                    className={`${FIELD} max-w-24`}
                  >
                    <option value="env">env</option>
                    <option value="file">file</option>
                  </select>
                  <input
                    aria-label={`Secret key ${i + 1}`}
                    value={r.c}
                    onChange={(e) => setSecretRows(patch(secretRows, r.id, { c: e.target.value }))}
                    placeholder={r.b === "file" ? "/podium/secrets/thing.json" : "GITHUB_TOKEN"}
                    className={`${FIELD} max-w-xs`}
                  />
                  <button
                    type="button"
                    aria-label={`Remove secret ${i + 1}`}
                    onClick={() => setSecretRows(secretRows.filter((x) => x.id !== r.id))}
                    className="rounded border border-border px-2 py-1 text-xs text-muted hover:border-err hover:text-err"
                  >
                    Remove
                  </button>
                </div>
                {missing ? (
                  <p className="text-xs text-warn">
                    No secret named <code className="font-mono">{r.a.trim()}</code> is registered
                    — a turn of this skill will fail admission.{" "}
                    <Link to="/secrets" className="text-accent hover:underline">
                      Register it
                    </Link>
                    .
                  </p>
                ) : null}
              </div>
            );
          })}
          <button
            type="button"
            onClick={() => setSecretRows([...secretRows, row("", "env", "")])}
            className="rounded border border-border px-2 py-1 text-xs text-muted hover:text-fg"
          >
            Add a secret
          </button>
          {secretNames.length === 0 && !secretsUnknown ? (
            <p className="text-xs text-muted">
              No secrets are registered on this control plane yet.{" "}
              <Link to="/secrets" className="text-accent hover:underline">
                Add one
              </Link>{" "}
              and it will appear here.
            </p>
          ) : null}
        </section>

        <section className="space-y-2">
          <h3 className="text-xs font-medium text-fg">Environment</h3>
          {envRows.map((r, i) => (
            <div key={r.id} className="flex flex-wrap items-center gap-2" data-testid="env-row">
              <input
                aria-label={`Environment key ${i + 1}`}
                value={r.a}
                onChange={(e) => setEnvRows(patch(envRows, r.id, { a: e.target.value }))}
                placeholder="PODIUM_AGENT_DRY_RUN"
                className={`${FIELD} max-w-xs`}
              />
              <input
                aria-label={`Environment value ${i + 1}`}
                value={r.b}
                onChange={(e) => setEnvRows(patch(envRows, r.id, { b: e.target.value }))}
                placeholder="1"
                className={`${FIELD} max-w-xs`}
              />
              <button
                type="button"
                aria-label={`Remove environment ${i + 1}`}
                onClick={() => setEnvRows(envRows.filter((x) => x.id !== r.id))}
                className="rounded border border-border px-2 py-1 text-xs text-muted hover:border-err hover:text-err"
              >
                Remove
              </button>
            </div>
          ))}
          <button
            type="button"
            onClick={() => setEnvRows([...envRows, row()])}
            className="rounded border border-border px-2 py-1 text-xs text-muted hover:text-fg"
          >
            Add a variable
          </button>
          <p className="text-xs text-muted">
            Plain environment, never a credential — it is stored and shown in clear.
          </p>
        </section>

        <section className="space-y-2">
          <h3 className="text-xs font-medium text-fg">Repositories</h3>
          {repoRows.map((r, i) => (
            <div key={r.id} className="flex flex-wrap items-center gap-2" data-testid="repo-row">
              <input
                aria-label={`Repository name ${i + 1}`}
                value={r.a}
                onChange={(e) => setRepoRows(patch(repoRows, r.id, { a: e.target.value }))}
                placeholder="podium"
                className={`${FIELD} max-w-40`}
              />
              <input
                aria-label={`Repository URL ${i + 1}`}
                value={r.b}
                onChange={(e) => setRepoRows(patch(repoRows, r.id, { b: e.target.value }))}
                placeholder="https://github.com/example/podium.git"
                className={`${FIELD} max-w-md`}
              />
              <input
                aria-label={`Repository default branch ${i + 1}`}
                value={r.c}
                onChange={(e) => setRepoRows(patch(repoRows, r.id, { c: e.target.value }))}
                placeholder="main"
                className={`${FIELD} max-w-32`}
              />
              <button
                type="button"
                aria-label={`Remove repository ${i + 1}`}
                onClick={() => setRepoRows(repoRows.filter((x) => x.id !== r.id))}
                className="rounded border border-border px-2 py-1 text-xs text-muted hover:border-err hover:text-err"
              >
                Remove
              </button>
            </div>
          ))}
          <button
            type="button"
            onClick={() => setRepoRows([...repoRows, row()])}
            className="rounded border border-border px-2 py-1 text-xs text-muted hover:text-fg"
          >
            Add a repository
          </button>
        </section>

        <div className="grid gap-3 sm:grid-cols-2">
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-fg">Node labels</span>
            <input
              aria-label="Node labels"
              value={labels}
              onChange={(e) => setLabels(e.target.value)}
              placeholder="linux, amd64"
              className={FIELD}
            />
            <span className="text-muted">Comma separated. A turn only runs on a node with them.</span>
          </label>
          <label className="flex flex-col gap-1 text-xs">
            <span className="text-fg">Slack channels</span>
            <input
              aria-label="Slack channels"
              value={channels}
              onChange={(e) => setChannels(e.target.value)}
              placeholder="C01ABCDEF"
              className={FIELD}
            />
            <span className="text-muted">
              Channel ids this skill is the default for. Two skills may not claim one channel.
            </span>
          </label>
        </div>

        <label className="flex items-center gap-2 text-xs">
          <input
            type="checkbox"
            aria-label="Runs Linear tickets"
            checked={linear}
            onChange={(e) => setLinear(e.target.checked)}
          />
          <span className="text-fg">This is the skill Linear tickets run</span>
          <span className="text-muted">
            At most one skill may set it: a ticket has no channel and no slash prefix to choose with.
          </span>
        </label>
      </fieldset>

      {error ? (
        <p role="alert" className="rounded border border-err/40 bg-err/10 px-3 py-2 text-xs text-err">
          {error}
        </p>
      ) : null}

      <div className="flex flex-wrap items-center gap-2">
        {shadowed || readOnly ? null : (
          <button
            type="submit"
            disabled={saving || deleting}
            className="rounded bg-accent px-3 py-1.5 text-xs font-medium text-bg disabled:opacity-50"
          >
            {saving ? "Saving…" : creating ? "Create skill" : "Save skill"}
          </button>
        )}
        <button
          type="button"
          onClick={onCancel}
          className="rounded border border-border px-3 py-1.5 text-xs text-muted hover:text-fg"
        >
          {readOnly ? "Back" : "Cancel"}
        </button>
        {/* Delete is here and nowhere else. It is a decision to take with the definition it
            destroys in front of you, not from a row in a list one misclick wide. */}
        {onDelete && !creating && !readOnly ? (
          <span className="ml-auto">
            <DeleteControl
              name={skill.name}
              confirming={confirmingDelete}
              pending={deleting ?? false}
              onAsk={() => setConfirmingDelete(true)}
              onCancel={() => setConfirmingDelete(false)}
              onConfirm={() => {
                setConfirmingDelete(false);
                onDelete();
              }}
            />
          </span>
        ) : null}
      </div>
    </form>
  );
}

/** The inline two-step the rest of the UI uses. A modal confirm is not the house style. */
function DeleteControl({
  name,
  confirming,
  pending,
  onAsk,
  onCancel,
  onConfirm,
}: {
  name: string;
  confirming: boolean;
  pending: boolean;
  onAsk: () => void;
  onCancel: () => void;
  onConfirm: () => void;
}) {
  if (!confirming) {
    return (
      <button
        type="button"
        aria-label={`Delete ${name}`}
        disabled={pending}
        onClick={onAsk}
        className="rounded border border-border px-3 py-1.5 text-xs text-muted hover:border-err hover:text-err disabled:opacity-50"
      >
        {pending ? "Deleting…" : "Delete skill"}
      </button>
    );
  }
  return (
    <span className="flex items-center gap-2 text-xs">
      <span className="text-muted">Delete {name}?</span>
      <button
        type="button"
        aria-label={`Confirm deleting ${name}`}
        disabled={pending}
        onClick={onConfirm}
        className="rounded border border-err/60 px-3 py-1.5 text-err disabled:opacity-50"
      >
        {pending ? "Deleting…" : "Yes, delete"}
      </button>
      <button
        type="button"
        onClick={onCancel}
        className="rounded border border-border px-3 py-1.5 text-muted hover:text-fg"
      >
        Keep
      </button>
    </span>
  );
}

function patch(rows: Row[], id: number, fields: Partial<Row>): Row[] {
  return rows.map((r) => (r.id === id ? { ...r, ...fields } : r));
}

/** Newlines only: a tool like Bash(git:*,gh:*) would not survive a comma split. */
function splitLines(text: string): string[] {
  return text
    .split("\n")
    .map((s) => s.trim())
    .filter(Boolean);
}

function splitList(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((s) => s.trim())
    .filter(Boolean);
}
