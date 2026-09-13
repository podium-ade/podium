import { useId, useMemo, useState, type ReactNode } from "react";
import { ChevronLeft, ChevronRight, Plus, Trash2 } from "lucide-react";
import { Link } from "react-router";
import type { AgentBackend, PlaybookDefinition } from "../../gen/podium/agent/v1/agent_pb";
import { INHERIT, type AgentChoice } from "../../lib/agents";
import { cn } from "../../lib/utils";
import { Chip } from "../Badge";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../ui/card";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../ui/collapsible";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Switch } from "../ui/switch";
import { Textarea } from "../ui/textarea";
import { AgentPicker } from "./AgentPicker";

/** The same expression the conductor holds a playbook name to. A name is also typed after a slash in Slack. */
export const PLAYBOOK_NAME_RE = /^[a-z][a-z0-9-]{0,31}$/;

/** PlaybookDraft is what the editor hands back: the request message, in plain fields. */
export type PlaybookDraft = {
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
  priority: number;
  resources: { cpu: number; memoryMb: number; pids: number };
  secrets: { name: string; target: string; key: string }[];
  repos: { name: string; url: string; defaultBranch: string }[];
  /** Undefined inherits the profile's persona, which is what a message field must be to mean that. */
  git?: { name: string; email: string };
  slackChannels: string[];
  linear: boolean;
  interactive: boolean;
  skills: string[];
  mcpServers: string[];
  env: Record<string, string>;
};

export type PlaybookEditorProps = {
  /** Undefined creates; a definition edits it. Only a stored playbook is ever passed. */
  playbook?: PlaybookDefinition;
  /** The backend catalogue, for the model picker. Empty while it loads. */
  agents: AgentBackend[];
  /** What this playbook runs on when it names nothing: the profile's own triple. */
  profileDefault: AgentChoice;
  /** The names SecretService already holds, for the picker. Never a value: there is no read API. */
  secretNames: string[];
  /** True when the secret list could not be read, so "not registered" cannot be claimed. */
  secretsUnknown?: boolean;
  /**
   * The Agent Skills this conductor can actually deliver, for the allow-list. A name not in
   * here is still accepted — profiles.Load deliberately does not check that a named skill
   * exists, so a playbook stays loadable on a machine that has none — and the form warns
   * instead of refusing.
   */
  skillNames?: string[];
  /** The names of skills that exist and are turned off. Naming one fails the turn. */
  disabledSkillNames?: string[];
  /** True when the skill list could not be read, so "not installed" cannot be claimed. */
  skillsUnknown?: boolean;
  /**
   * The MCP servers registered on this conductor, for the same kind of allow-list. A name
   * not in here is accepted for the same reason a skill's is — a playbook file has to load
   * on a machine with no registry — and the form warns instead of refusing.
   */
  mcpNames?: string[];
  /** The names of registered servers that are turned off. Naming one fails the turn. */
  disabledMcpNames?: string[];
  /** True when the MCP list could not be read, so "not registered" cannot be claimed. */
  mcpUnknown?: boolean;
  saving?: boolean;
  deleting?: boolean;
  /**
   * readOnly renders a file playbook: every control disabled, nothing to save and nothing to
   * delete. The files are authoritative for the names they hold, so this screen shows one
   * and never writes it — but showing it is the point, because a definition you cannot read
   * is harder to work with than one you merely cannot change.
   */
  readOnly?: boolean;
  /** The server's refusal, shown verbatim: its rules are the only rules. */
  error?: string;
  onSubmit: (draft: PlaybookDraft) => void;
  /** Deletes the playbook being edited. Absent while creating: there is nothing to delete. */
  onDelete?: () => void;
  onCancel: () => void;
};

type Row = { id: number; a: string; b: string; c: string };

// A monotonic id per row, so React keys survive a row being removed from the middle. It is
// module level rather than a ref because a ref may not be read while rendering.
let nextRowId = 0;
const row = (a = "", b = "", c = ""): Row => ({ id: nextRowId++, a, b, c });

/**
 * PlaybookEditor is the whole of a playbook in one form: which image runs it, what it is told,
 * which tools it may use, and which stored secrets it names.
 *
 * It is grouped rather than listed, because the fields answer four different questions —
 * what this playbook IS, what RUNS it, what it can REACH, and what ROUTES to it — and a wall of
 * inputs makes an operator read all of them to change one. The parts most playbooks never set
 * are folded away, and fold themselves back open when the playbook in front of you uses them.
 *
 * Naming a secret here is not a privilege the editor hands out. A task spec names secrets
 * the same way and nothing in Podium authorises which names a caller may use — so there is
 * no allow-list here, and adding one would only be theatre. What the picker does is stop a
 * typo: a playbook naming a secret the control plane does not hold fails admission on its first
 * turn, and saying so now is cheaper than finding out in a thread.
 */
export function PlaybookEditor({
  playbook,
  agents,
  profileDefault,
  secretNames,
  secretsUnknown,
  skillNames,
  disabledSkillNames,
  skillsUnknown,
  mcpNames,
  disabledMcpNames,
  mcpUnknown,
  saving,
  deleting,
  error,
  onSubmit,
  onDelete,
  onCancel,
  readOnly,
}: PlaybookEditorProps) {
  const creating = playbook === undefined;
  // A shadowed row is a stored playbook a playbooks/<name>.yaml has since claimed. The conductor
  // refuses to write over one, so the form shows what it holds and offers only the delete.
  const shadowed = playbook?.shadowed ?? false;
  const locked = shadowed || (readOnly ?? false);
  const uid = useId();

  const [name, setName] = useState(playbook?.name ?? "");
  const [image, setImage] = useState(playbook?.image ?? "");
  const [prompt, setPrompt] = useState(playbook?.systemPrompt ?? "");
  const [tools, setTools] = useState((playbook?.allowedTools ?? []).join("\n"));
  const [maxTurns, setMaxTurns] = useState(String(playbook?.maxTurns || 50));
  const [timeoutText, setTimeoutText] = useState(playbook?.timeout || "30m");
  const [choice, setChoice] = useState<AgentChoice>(() =>
    playbook ? { agent: playbook.agent, model: playbook.model, effort: playbook.effort } : INHERIT,
  );
  const [labels, setLabels] = useState((playbook?.labels ?? []).join(", "));
  const [priority, setPriority] = useState(String(playbook?.priority ?? 0));
  const [channels, setChannels] = useState((playbook?.slackChannels ?? []).join(", "));
  const [linear, setLinear] = useState(playbook?.linear ?? false);
  const [interactive, setInteractive] = useState(playbook?.interactive ?? false);
  const [skills, setSkills] = useState((playbook?.skills ?? []).join("\n"));
  const [mcpServers, setMcpServers] = useState((playbook?.mcpServers ?? []).join("\n"));
  const [cpu, setCpu] = useState(String(playbook?.resources?.cpu ?? ""));
  const [memoryMb, setMemoryMb] = useState(String(playbook?.resources?.memoryMb ?? ""));
  const [pids, setPids] = useState(String(playbook?.resources?.pids ?? ""));
  const [secretRows, setSecretRows] = useState<Row[]>(() =>
    (playbook?.secrets ?? []).map((s) => row(s.name, s.target || "env", s.key)),
  );
  const [envRows, setEnvRows] = useState<Row[]>(() =>
    Object.entries(playbook?.env ?? {}).map(([k, v]) => row(k, v)),
  );
  const [repoRows, setRepoRows] = useState<Row[]>(() =>
    (playbook?.repos ?? []).map((r) => row(r.name, r.url, r.defaultBranch)),
  );
  const [gitName, setGitName] = useState(playbook?.git?.name ?? "");
  const [gitEmail, setGitEmail] = useState(playbook?.git?.email ?? "");
  const [tried, setTried] = useState(false);
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  const registered = useMemo(() => new Set(secretNames), [secretNames]);
  const installed = useMemo(() => new Set(skillNames ?? []), [skillNames]);
  const disabled = useMemo(() => new Set(disabledSkillNames ?? []), [disabledSkillNames]);
  const registeredMcp = useMemo(() => new Set(mcpNames ?? []), [mcpNames]);
  const disabledMcp = useMemo(() => new Set(disabledMcpNames ?? []), [disabledMcpNames]);

  const toolList = splitLines(tools);
  const skillList = splitLines(skills);
  const mcpList = splitLines(mcpServers);
  const nameOk = PLAYBOOK_NAME_RE.test(name);
  const problems: string[] = [];
  if (!nameOk) problems.push("name");
  if (image.trim() === "") problems.push("image");
  if (prompt.trim() === "") problems.push("prompt");
  if (toolList.length === 0) problems.push("tools");

  const capped = Number(cpu) > 0 || Number(memoryMb) > 0 || Number(pids) > 0;
  // Undefined and not a pair of empty strings: an empty persona would be a persona, and what
  // an empty form means is "inherit the profile's".
  const persona =
    gitName.trim() === "" && gitEmail.trim() === ""
      ? undefined
      : { name: gitName.trim(), email: gitEmail.trim() };
  if (persona && (persona.name === "" || persona.email === "")) problems.push("commit author");

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
      priority: Number(priority) || 0,
      resources: { cpu: Number(cpu) || 0, memoryMb: Number(memoryMb) || 0, pids: Number(pids) || 0 },
      secrets: secretRows
        .filter((r) => r.a.trim() !== "")
        .map((r) => ({ name: r.a.trim(), target: r.b || "env", key: r.c.trim() })),
      repos: repoRows
        .filter((r) => r.a.trim() !== "" || r.b.trim() !== "")
        .map((r) => ({ name: r.a.trim(), url: r.b.trim(), defaultBranch: r.c.trim() })),
      git: persona,
      slackChannels: splitList(channels),
      linear,
      interactive,
      skills: skillList,
      mcpServers: mcpList,
      env: Object.fromEntries(
        envRows.filter((r) => r.a.trim() !== "").map((r) => [r.a.trim(), r.b]),
      ),
    });
  }

  return (
    <form
      data-testid="playbook-editor"
      className="space-y-5 pb-2"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <header className="space-y-3">
        <button
          type="button"
          onClick={onCancel}
          className="-ml-1 inline-flex items-center gap-1 rounded-md px-1 py-0.5 text-xs text-muted transition-colors hover:text-fg focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none"
        >
          <ChevronLeft className="size-3.5" />
          Playbooks
        </button>
        <div className="min-w-0 space-y-1.5">
          <h1 className="text-xl leading-tight font-semibold tracking-tight text-fg">
            {creating ? "New playbook" : `/${playbook.name}`}
          </h1>
          <p className="max-w-2xl text-sm leading-relaxed text-muted">
            {locked ? (
              <>
                Defined by <code className="font-mono">playbooks/{playbook?.name}.yaml</code> on the
                conductor&apos;s host. The files win, so this is read-only here — edit the file
                and restart the conductor, or make a new playbook to change one in the browser.
              </>
            ) : (
              <>
                Stored in the conductor&apos;s database and validated by exactly the rules a{" "}
                <code className="font-mono">playbooks/&lt;name&gt;.yaml</code> is held to.
              </>
            )}
          </p>
        </div>
      </header>

      {playbook && shadowed ? (
        <Alert variant="warn" title={`playbooks/${playbook.name}.yaml defines this name, and the file wins`}>
          This stored definition never runs and cannot be written over. It is shown so you can
          see what deleting it throws away.
        </Alert>
      ) : null}

      {/* One fieldset rather than a disabled prop on every input: a shadowed playbook and a
          file playbook are both read-only as a whole, and no field of either could usefully be
          changed. It disables the picker's buttons too, which a per-input prop would miss. */}
      <fieldset disabled={locked} className="space-y-5">
        <Section
          title="Identity"
          hint="What this playbook is called, what image runs it, and what it is told."
        >
          <div className="grid gap-4 sm:grid-cols-[minmax(0,14rem)_minmax(0,1fr)]">
            <Field
              id={`${uid}-name`}
              label="Playbook name"
              hint={
                creating
                  ? "Lower case, digits and dashes; this is what a human types after a slash in Slack."
                  : "A playbook keeps the name it was created with."
              }
              error={tried && !nameOk ? "A name must match ^[a-z][a-z0-9-]{0,31}$." : undefined}
            >
              <Input
                id={`${uid}-name`}
                value={name}
                disabled={!creating}
                aria-invalid={tried && !nameOk}
                onChange={(e) => setName(e.target.value)}
                placeholder="reporter"
                className="font-mono text-xs"
              />
            </Field>

            {/* The image is the unit of capability: what a turn of this playbook can do at all
                is decided by what is in the image, before any prompt or tool list is read. */}
            <Field
              id={`${uid}-image`}
              label="Image"
              hint={
                <>
                  Any image you supply. It has to implement the turn-brief protocol — read the
                  brief off <code className="font-mono">PODIUM_AGENT_TURN</code> and emit the
                  runner&apos;s message events — and{" "}
                  <code className="font-mono">
                    FROM ghcr.io/podium-ade/podium-agent-runtime
                  </code>{" "}
                  is the easy way to get one that does. Podium does not pick an image for you.
                </>
              }
              error={tried && image.trim() === "" ? "An image is required." : undefined}
            >
              <Input
                id={`${uid}-image`}
                value={image}
                aria-invalid={tried && image.trim() === ""}
                onChange={(e) => setImage(e.target.value)}
                placeholder="ghcr.io/example/my-agent-runtime:latest"
                className="font-mono text-xs"
              />
            </Field>
          </div>

          <Field
            id={`${uid}-prompt`}
            label="System prompt"
            hint={
              <>
                The prompt itself, not a path. <code className="font-mono">file:</code> only
                works in a YAML file, which has a directory beside it to resolve against.
              </>
            }
            error={tried && prompt.trim() === "" ? "A prompt is required." : undefined}
          >
            <Textarea
              id={`${uid}-prompt`}
              value={prompt}
              aria-invalid={tried && prompt.trim() === ""}
              onChange={(e) => setPrompt(e.target.value)}
              rows={8}
              spellCheck={false}
              placeholder="What this playbook is for, and how it should behave."
              className="font-mono text-xs"
            />
          </Field>
        </Section>

        <Section
          title="Execution"
          hint="What runs a turn of this playbook, how far it may go, and where it may land."
        >
          <div className="grid gap-4 sm:grid-cols-2">
            <div className="space-y-1.5">
              <Label htmlFor={`${uid}-tools`}>Allowed tools</Label>
              <Textarea
                id={`${uid}-tools`}
                value={tools}
                aria-invalid={tried && toolList.length === 0}
                onChange={(e) => setTools(e.target.value)}
                rows={6}
                spellCheck={false}
                placeholder={"read\ngrep\nglob\nbash"}
                className="font-mono text-xs"
              />
              <p className="text-2xs leading-relaxed text-faint">
                One per line, in the harness&apos;s own names — <Mono>read</Mono>,{" "}
                <Mono>grep</Mono>, <Mono>glob</Mono>, <Mono>bash</Mono>, <Mono>edit</Mono>,{" "}
                <Mono>write</Mono>, <Mono>webfetch</Mono>, <Mono>list</Mono>, <Mono>patch</Mono>,{" "}
                <Mono>task</Mono>. At least one is required.
              </p>
              {tried && toolList.length === 0 ? (
                <p className="text-xs text-err">Name at least one tool.</p>
              ) : null}
            </div>

            <div className="space-y-4">
              <div className="space-y-1.5">
                {/* Not a <Label>: the picker is three controls behind one trigger, and each
                    one names itself. */}
                <span className="block text-xs font-medium text-muted">Model</span>
                <AgentPicker
                  label="Playbook"
                  value={choice}
                  onChange={setChoice}
                  agents={agents}
                  inherit={{
                    label: "Inherit from the profile",
                    hint: "whatever the profile is set to",
                  }}
                  inherited={profileDefault}
                />
              </div>

              <div className="grid grid-cols-2 gap-3">
                <Field
                  id={`${uid}-turns`}
                  label="Max turns"
                  hint="How many times the model may act before the turn is cut off."
                >
                  <Input
                    id={`${uid}-turns`}
                    type="number"
                    min={1}
                    value={maxTurns}
                    onChange={(e) => setMaxTurns(e.target.value)}
                    className="tabular"
                  />
                </Field>
                <Field id={`${uid}-timeout`} label="Timeout" hint="A Go duration: 30m, 900s, 2h.">
                  <Input
                    id={`${uid}-timeout`}
                    value={timeoutText}
                    onChange={(e) => setTimeoutText(e.target.value)}
                    placeholder="30m"
                    className="font-mono text-xs"
                  />
                </Field>
              </div>
            </div>
          </div>

          <div className="grid gap-3 sm:grid-cols-[2fr_1fr]">
            <Field
              id={`${uid}-labels`}
              label="Node labels"
              hint="Comma separated. A turn only runs on a node with them; leave it empty to run anywhere."
            >
              <Input
                id={`${uid}-labels`}
                value={labels}
                onChange={(e) => setLabels(e.target.value)}
                placeholder="linux, amd64"
                className="font-mono text-xs"
              />
            </Field>
            <Field
              id={`${uid}-priority`}
              label="Queue priority"
              hint="Higher is claimed first when the fleet is full. 0 is the default; negative waits behind everything else."
            >
              <Input
                id={`${uid}-priority`}
                type="number"
                min={-1000}
                max={1000}
                value={priority}
                onChange={(e) => setPriority(e.target.value)}
                className="tabular"
              />
            </Field>
          </div>

          <Disclosure
            label="Resource limits"
            summary={capped ? "capped" : "uncapped"}
            defaultOpen={capped}
          >
            <div className="grid grid-cols-3 gap-3">
              <Field id={`${uid}-cpu`} label="CPU" hint="Cores.">
                <Input
                  id={`${uid}-cpu`}
                  type="number"
                  step="0.5"
                  min={0}
                  value={cpu}
                  onChange={(e) => setCpu(e.target.value)}
                  className="tabular"
                />
              </Field>
              <Field id={`${uid}-mem`} label="Memory MB" hint="Hard limit; the kernel kills it.">
                <Input
                  id={`${uid}-mem`}
                  type="number"
                  min={0}
                  value={memoryMb}
                  onChange={(e) => setMemoryMb(e.target.value)}
                  className="tabular"
                />
              </Field>
              <Field id={`${uid}-pids`} label="PIDs" hint="Processes the container may fork.">
                <Input
                  id={`${uid}-pids`}
                  type="number"
                  min={0}
                  value={pids}
                  onChange={(e) => setPids(e.target.value)}
                  className="tabular"
                />
              </Field>
            </div>
            <p className="text-2xs text-faint">Blank or zero leaves the resource uncapped.</p>
          </Disclosure>
        </Section>

        <Section
          title="Access"
          hint="What a turn of this playbook can reach beyond its own image."
        >
          <div className="space-y-2.5">
            <div className="space-y-1">
              <h3 className="text-xs font-medium text-fg">Secrets</h3>
              <p className="max-w-2xl text-2xs leading-relaxed text-faint">
                A playbook names a stored secret; it never holds one. The value is written on the{" "}
                <Link to="/secrets" className="text-accent hover:underline">
                  Secrets
                </Link>{" "}
                screen, encrypted by podium-server, and handed straight to the container that
                runs the turn. Nothing in this UI can read one back.
              </p>
            </div>
            <datalist id="podium-secret-names">
              {secretNames.map((n) => (
                <option key={n} value={n} />
              ))}
            </datalist>
            {secretRows.length > 0 ? (
              <div className="hidden gap-2 px-1 text-2xs tracking-wide text-faint uppercase sm:flex">
                <span className="w-full max-w-xs">Stored secret</span>
                <span className="w-24">Target</span>
                <span className="w-full max-w-xs">Key or path</span>
              </div>
            ) : null}
            {secretRows.map((r, i) => {
              const missing = r.a.trim() !== "" && !secretsUnknown && !registered.has(r.a.trim());
              return (
                <div key={r.id} className="space-y-1" data-testid="secret-row">
                  <div className="flex flex-wrap items-center gap-2">
                    <Input
                      aria-label={`Secret name ${i + 1}`}
                      list="podium-secret-names"
                      value={r.a}
                      aria-invalid={missing}
                      onChange={(e) => setSecretRows(patch(secretRows, r.id, { a: e.target.value }))}
                      placeholder="podium.agent.github_token"
                      className="h-8 max-w-xs font-mono text-xs"
                    />
                    <select
                      aria-label={`Secret target ${i + 1}`}
                      value={r.b || "env"}
                      onChange={(e) => setSecretRows(patch(secretRows, r.id, { b: e.target.value }))}
                      className="h-8 w-24 rounded-md border border-input bg-bg px-2 font-mono text-xs text-fg outline-none focus-visible:border-accent/60 focus-visible:ring-2 focus-visible:ring-ring/35 disabled:opacity-50"
                    >
                      <option value="env">env</option>
                      <option value="file">file</option>
                    </select>
                    <Input
                      aria-label={`Secret key ${i + 1}`}
                      value={r.c}
                      onChange={(e) => setSecretRows(patch(secretRows, r.id, { c: e.target.value }))}
                      placeholder={r.b === "file" ? "/podium/secrets/thing.json" : "GITHUB_TOKEN"}
                      className="h-8 max-w-xs font-mono text-xs"
                    />
                    <RemoveRow
                      label={`Remove secret ${i + 1}`}
                      onClick={() => setSecretRows(secretRows.filter((x) => x.id !== r.id))}
                    />
                  </div>
                  {missing ? (
                    <p className="text-xs text-warn">
                      No secret named <Mono>{r.a.trim()}</Mono> is registered — a turn of this
                      playbook will fail admission.{" "}
                      <Link to="/secrets" className="text-accent hover:underline">
                        Register it
                      </Link>
                      .
                    </p>
                  ) : null}
                </div>
              );
            })}
            <AddRow
              label="Add a secret"
              onClick={() => setSecretRows([...secretRows, row("", "env", "")])}
            />
            {secretNames.length === 0 && !secretsUnknown ? (
              <p className="text-2xs text-faint">
                No secrets are registered on this control plane yet.{" "}
                <Link to="/secrets" className="text-accent hover:underline">
                  Add one
                </Link>{" "}
                and it will appear here.
              </p>
            ) : null}
          </div>

          <div className="space-y-2.5">
            <div className="space-y-1">
              <h3 className="text-xs font-medium text-fg">Agent Skills</h3>
              <p className="max-w-2xl text-2xs leading-relaxed text-faint">
                One name per line, out of the{" "}
                <Link to="/agent/skills" className="text-accent hover:underline">
                  Skills
                </Link>{" "}
                library. A skill is instructions and scripts somebody else wrote, and they run
                in this playbook&apos;s container with this playbook&apos;s credentials. Naming
                none — the default — is enforced and not merely unset: the turn is handed a
                permission map that denies every skill, the harness&apos;s own included.
              </p>
            </div>
            <datalist id="podium-skill-names">
              {(skillNames ?? []).map((n) => (
                <option key={n} value={n} />
              ))}
            </datalist>
            <Textarea
              id={`${uid}-skills`}
              aria-label="Agent Skills"
              value={skills}
              onChange={(e) => setSkills(e.target.value)}
              rows={3}
              spellCheck={false}
              placeholder={"pr-review\nrelease-notes"}
              className="max-w-md font-mono text-xs"
            />
            {skillList
              .filter((n) => !skillsUnknown && !installed.has(n))
              .map((n) => (
                <p key={n} className="text-xs text-warn" data-testid="skill-missing">
                  No skill named <Mono>{n}</Mono> is installed on this conductor — a turn of this
                  playbook will fail.{" "}
                  <Link to="/agent/skills" className="text-accent hover:underline">
                    Add it
                  </Link>
                  .
                </p>
              ))}
            {skillList
              .filter((n) => disabled.has(n))
              .map((n) => (
                <p key={n} className="text-xs text-warn" data-testid="skill-disabled">
                  <Mono>{n}</Mono> is installed but disabled — a turn of this playbook will fail
                  rather than run without it.
                </p>
              ))}
            {(skillNames ?? []).length === 0 && !skillsUnknown ? (
              <p className="text-2xs text-faint">
                No skills are installed on this conductor yet.{" "}
                <Link to="/agent/skills" className="text-accent hover:underline">
                  Add one
                </Link>{" "}
                and it will appear here.
              </p>
            ) : null}
          </div>

          <div className="space-y-2.5">
            <div className="space-y-1">
              <h3 className="text-xs font-medium text-fg">MCP servers</h3>
              <p className="max-w-2xl text-2xs leading-relaxed text-faint">
                One name per line, out of the{" "}
                <Link to="/agent/mcp" className="text-accent hover:underline">
                  MCP
                </Link>{" "}
                registry. Naming one here is what gives a turn of this playbook that
                server&apos;s tools <em>and</em> its stored token, so name only what this
                playbook&apos;s work needs. Naming none — the default — means the turn has no
                MCP tools beyond the ones the conductor wires up itself.
              </p>
            </div>
            <datalist id="podium-mcp-names">
              {(mcpNames ?? []).map((n) => (
                <option key={n} value={n} />
              ))}
            </datalist>
            <Textarea
              id={`${uid}-mcp`}
              aria-label="MCP servers"
              value={mcpServers}
              onChange={(e) => setMcpServers(e.target.value)}
              rows={2}
              spellCheck={false}
              placeholder={"linear"}
              className="max-w-md font-mono text-xs"
            />
            {mcpList
              .filter((n) => !mcpUnknown && !registeredMcp.has(n))
              .map((n) => (
                <p key={n} className="text-xs text-warn" data-testid="mcp-missing">
                  No MCP server named <Mono>{n}</Mono> is registered on this conductor — a turn
                  of this playbook will fail.{" "}
                  <Link to="/agent/mcp" className="text-accent hover:underline">
                    Add it
                  </Link>
                  .
                </p>
              ))}
            {mcpList
              .filter((n) => disabledMcp.has(n))
              .map((n) => (
                <p key={n} className="text-xs text-warn" data-testid="mcp-disabled">
                  <Mono>{n}</Mono> is registered but disabled — a turn of this playbook will
                  fail rather than run without it.
                </p>
              ))}
            {(mcpNames ?? []).length === 0 && !mcpUnknown ? (
              <p className="text-2xs text-faint">
                No MCP servers are registered on this conductor yet.{" "}
                <Link to="/agent/mcp" className="text-accent hover:underline">
                  Add one
                </Link>{" "}
                and it will appear here.
              </p>
            ) : null}
          </div>

          <Disclosure
            label="Environment variables"
            summary={countOf(envRows, "variable")}
            defaultOpen={envRows.length > 0}
          >
            {envRows.map((r, i) => (
              <div key={r.id} className="flex flex-wrap items-center gap-2" data-testid="env-row">
                <Input
                  aria-label={`Environment key ${i + 1}`}
                  value={r.a}
                  onChange={(e) => setEnvRows(patch(envRows, r.id, { a: e.target.value }))}
                  placeholder="PODIUM_AGENT_DRY_RUN"
                  className="h-8 max-w-xs font-mono text-xs"
                />
                <Input
                  aria-label={`Environment value ${i + 1}`}
                  value={r.b}
                  onChange={(e) => setEnvRows(patch(envRows, r.id, { b: e.target.value }))}
                  placeholder="1"
                  className="h-8 max-w-xs font-mono text-xs"
                />
                <RemoveRow
                  label={`Remove environment ${i + 1}`}
                  onClick={() => setEnvRows(envRows.filter((x) => x.id !== r.id))}
                />
              </div>
            ))}
            <AddRow label="Add a variable" onClick={() => setEnvRows([...envRows, row()])} />
            <p className="text-2xs text-faint">
              Plain environment, never a credential — it is stored and shown in clear.
            </p>
          </Disclosure>

          <Disclosure
            label="Repositories"
            summary={countOf(repoRows, "repository", "repositories")}
            defaultOpen={repoRows.length > 0}
          >
            {repoRows.map((r, i) => (
              <div key={r.id} className="flex flex-wrap items-center gap-2" data-testid="repo-row">
                <Input
                  aria-label={`Repository name ${i + 1}`}
                  value={r.a}
                  onChange={(e) => setRepoRows(patch(repoRows, r.id, { a: e.target.value }))}
                  placeholder="podium"
                  className="h-8 max-w-40 font-mono text-xs"
                />
                <Input
                  aria-label={`Repository URL ${i + 1}`}
                  value={r.b}
                  onChange={(e) => setRepoRows(patch(repoRows, r.id, { b: e.target.value }))}
                  placeholder="https://github.com/example/podium.git"
                  className="h-8 max-w-md font-mono text-xs"
                />
                <Input
                  aria-label={`Repository default branch ${i + 1}`}
                  value={r.c}
                  onChange={(e) => setRepoRows(patch(repoRows, r.id, { c: e.target.value }))}
                  placeholder="main"
                  className="h-8 max-w-32 font-mono text-xs"
                />
                <RemoveRow
                  label={`Remove repository ${i + 1}`}
                  onClick={() => setRepoRows(repoRows.filter((x) => x.id !== r.id))}
                />
              </div>
            ))}
            <AddRow label="Add a repository" onClick={() => setRepoRows([...repoRows, row()])} />
            <div className="flex flex-wrap items-center gap-2 pt-1">
              <Input
                aria-label="Commit author name"
                value={gitName}
                onChange={(e) => setGitName(e.target.value)}
                placeholder="Ada Lovelace"
                className="h-8 max-w-40 text-xs"
              />
              <Input
                aria-label="Commit author email"
                value={gitEmail}
                onChange={(e) => setGitEmail(e.target.value)}
                placeholder="1234+ada@users.noreply.github.com"
                className="h-8 max-w-md font-mono text-xs"
              />
            </div>
            <p className="text-2xs text-faint">
              Who this playbook&rsquo;s commits are by. Empty inherits the profile&rsquo;s. GitHub links a
              commit to an account by the email, so use one that belongs to the account whose token
              this playbook pushes with &mdash; an address owned by nobody leaves every commit
              unattributed, which is what blocks a Vercel deployment.
            </p>
          </Disclosure>
        </Section>

        <Section title="Routing" hint="What sends work to this playbook without anybody typing its name.">
          <Field
            id={`${uid}-channels`}
            label="Slack channels"
            hint="Channel ids this playbook is the default for. Two playbooks may not claim one channel."
          >
            <Input
              id={`${uid}-channels`}
              value={channels}
              onChange={(e) => setChannels(e.target.value)}
              placeholder="C01ABCDEF"
              className="font-mono text-xs"
            />
          </Field>

          <div className="flex items-start gap-3 rounded-lg border border-hairline px-3 py-2.5">
            <Switch
              id={`${uid}-interactive`}
              aria-label="Ask and wait in the same turn"
              checked={interactive}
              onCheckedChange={setInteractive}
              className="mt-0.5"
            />
            <div className="min-w-0 space-y-0.5">
              <Label htmlFor={`${uid}-interactive`} className="text-fg">
                Ask a question and wait in the same turn
              </Label>
              <p className="text-2xs leading-relaxed text-faint">
                Off by default. When on, the agent can ask you something and keep the
                container — clones, a Docker daemon, a browser — until you reply. Waiting
                still counts against the timeout.
              </p>
            </div>
          </div>

          <div className="flex items-start gap-3 rounded-lg border border-hairline px-3 py-2.5">
            <Switch
              id={`${uid}-linear`}
              aria-label="Runs Linear tickets"
              checked={linear}
              onCheckedChange={setLinear}
              className="mt-0.5"
            />
            <div className="min-w-0 space-y-0.5">
              <Label htmlFor={`${uid}-linear`} className="text-fg">
                This is the playbook Linear tickets run
              </Label>
              <p className="text-2xs leading-relaxed text-faint">
                At most one playbook may set it: a ticket has no channel and no slash prefix to
                choose with.
              </p>
            </div>
          </div>
        </Section>
      </fieldset>

      {error ? (
        <Alert variant="destructive" role="alert" title="The conductor refused this playbook">
          {error}
        </Alert>
      ) : null}

      {tried && problems.length > 0 ? (
        <Alert variant="warn" role="note">
          Nothing was sent: the {problems.join(", ")} {problems.length === 1 ? "field" : "fields"}{" "}
          {problems.length === 1 ? "needs" : "need"} a value first.
        </Alert>
      ) : null}

      <div className="sticky bottom-0 z-10 flex flex-wrap items-center gap-2 rounded-xl border border-border bg-panel/95 px-4 py-3 shadow-md backdrop-blur">
        {locked ? null : (
          <Button type="submit" size="sm" disabled={saving || deleting}>
            {saving ? "Saving…" : creating ? "Create playbook" : "Save playbook"}
          </Button>
        )}
        <Button type="button" variant="outline" size="sm" onClick={onCancel}>
          {readOnly ? "Back" : "Cancel"}
        </Button>
        {locked ? null : (
          <p className="hidden text-2xs text-faint sm:block">
            A save takes effect on the next turn — running turns keep the definition they
            started with.
          </p>
        )}
        {/* Delete is here and nowhere else. It is a decision to take with the definition it
            destroys in front of you, not from a row in a list one misclick wide. */}
        {onDelete && !creating && !readOnly ? (
          <div className="ml-auto">
            <Button
              type="button"
              variant="danger"
              size="sm"
              aria-label={`Delete ${playbook.name}`}
              disabled={deleting}
              onClick={() => setConfirmingDelete(true)}
            >
              <Trash2 />
              {deleting ? "Deleting…" : "Delete playbook"}
            </Button>
            <Dialog open={confirmingDelete} onOpenChange={setConfirmingDelete}>
              <DialogContent>
                <DialogHeader>
                  <DialogTitle>Delete /{playbook.name}?</DialogTitle>
                  <DialogDescription>
                    The definition goes with it — prompt, tools, limits and the secrets it
                    names. Anything that routes to this playbook stops working on the next turn.
                    There is no undo.
                  </DialogDescription>
                </DialogHeader>
                <DialogFooter>
                  <Button
                    type="button"
                    variant="outline"
                    size="sm"
                    onClick={() => setConfirmingDelete(false)}
                  >
                    Keep
                  </Button>
                  <Button
                    type="button"
                    variant="destructive"
                    size="sm"
                    aria-label={`Confirm deleting ${playbook.name}`}
                    disabled={deleting}
                    onClick={() => {
                      setConfirmingDelete(false);
                      onDelete();
                    }}
                  >
                    {deleting ? "Deleting…" : "Delete playbook"}
                  </Button>
                </DialogFooter>
              </DialogContent>
            </Dialog>
          </div>
        ) : null}
      </div>
    </form>
  );
}

function Section({
  title,
  hint,
  children,
}: {
  title: string;
  hint: string;
  children: ReactNode;
}) {
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>{title}</CardTitle>
          <CardDescription>{hint}</CardDescription>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">{children}</CardContent>
    </Card>
  );
}

function Field({
  id,
  label,
  hint,
  error,
  children,
}: {
  id: string;
  label: string;
  hint?: ReactNode;
  error?: string;
  children: ReactNode;
}) {
  return (
    <div className="min-w-0 space-y-1.5">
      <Label htmlFor={id}>{label}</Label>
      {children}
      {hint ? <p className="text-2xs leading-relaxed text-faint">{hint}</p> : null}
      {error ? <p className="text-xs text-err">{error}</p> : null}
    </div>
  );
}

/**
 * Disclosure folds away a part most playbooks never set. It opens itself when the playbook in
 * front of you does use it, so nothing is ever hidden from the operator reading a definition
 * — only from the one creating a plain playbook.
 */
function Disclosure({
  label,
  summary,
  defaultOpen,
  children,
}: {
  label: string;
  summary: string;
  defaultOpen: boolean;
  children: ReactNode;
}) {
  const [open, setOpen] = useState(defaultOpen);
  return (
    <Collapsible
      open={open}
      onOpenChange={setOpen}
      className="overflow-hidden rounded-lg border border-hairline"
    >
      <CollapsibleTrigger asChild>
        <button
          type="button"
          className="flex w-full items-center gap-2 px-3 py-2 text-left transition-colors hover:bg-raised/50 focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none disabled:opacity-50"
        >
          <ChevronRight
            aria-hidden
            className={cn("size-3.5 text-faint transition-transform duration-150", open && "rotate-90")}
          />
          <span className="text-xs font-medium text-fg">{label}</span>
          <Chip className="ml-auto">{summary}</Chip>
        </button>
      </CollapsibleTrigger>
      <CollapsibleContent className="space-y-2.5 border-t border-hairline p-3">
        {children}
      </CollapsibleContent>
    </Collapsible>
  );
}

function AddRow({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button type="button" variant="outline" size="xs" onClick={onClick}>
      <Plus />
      {label}
    </Button>
  );
}

function RemoveRow({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      onClick={onClick}
      className="hover:bg-err/12 hover:text-err"
    >
      <Trash2 />
    </Button>
  );
}

function Mono({ children }: { children: ReactNode }) {
  return <code className="font-mono">{children}</code>;
}

function countOf(rows: Row[], one: string, many = ""): string {
  const n = rows.filter((r) => r.a.trim() !== "" || r.b.trim() !== "").length;
  if (n === 0) return "none";
  return `${n} ${n === 1 ? one : many || `${one}s`}`;
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
