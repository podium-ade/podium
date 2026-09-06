import { useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { FileCode2, RotateCcw, SlidersHorizontal } from "lucide-react";
import { Chip } from "./Badge";
import { useToast } from "./Toast";
import { Field, FieldProblems } from "./submit/Field";
import { Section } from "./submit/Section";
import { KeyValueEditor, SecretsEditor, type EnvRow, type SecretRow } from "./submit/editors";
import { SpecDigest, SpecPreview } from "./submit/SpecPreview";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { Switch } from "./ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "./ui/tabs";
import { Textarea } from "./ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "./ui/toggle-group";
import { Code, connectCode, errorMessage, tasks } from "../lib/client";
import {
  docToYaml,
  fieldsToDoc,
  parseSpecValue,
  parseSpecYaml,
  splitServerProblems,
  EMPTY_FIELDS,
  type Fields,
  type SpecDoc,
  type SpecInit,
} from "../lib/spec";

type Mode = "form" | "yaml";

type SectionId = "env" | "placement" | "limits" | "secrets" | "hardening" | "sidecars";

/** The spec fields the simple `Fields` shape does not carry. */
export interface Advanced {
  secrets: { name: string; target: string; key: string }[];
  hardening: { readOnlyRootfs: boolean; capabilities: string[] };
  pids: string;
}

// pkg/spec.AllowedCapabilities. The executor drops every capability; these are the only ones a
// spec may ask back for, so the control is a fixed set rather than free text.
const CAPABILITIES = [
  "CHOWN",
  "DAC_OVERRIDE",
  "FOWNER",
  "SETUID",
  "SETGID",
  "NET_BIND_SERVICE",
  "KILL",
];

const MONO = "font-mono text-xs";
const NUM = "tabular h-8 text-xs";

export function SpecForm({
  initialFields = EMPTY_FIELDS,
  initialYaml = "",
  initialMode = "form",
  initialAdvanced,
  rerunOf,
}: {
  initialFields?: Fields;
  initialYaml?: string;
  initialMode?: Mode;
  initialAdvanced?: Advanced;
  rerunOf?: string;
}) {
  const navigate = useNavigate();
  const toast = useToast();
  const [mode, setMode] = useState<Mode>(initialMode);
  const [fields, setFields] = useState<Fields>(initialFields);
  const [envRows, setEnvRows] = useState<EnvRow[]>(() => splitEnv(initialFields.env));
  const [secretRows, setSecretRows] = useState<SecretRow[]>(() =>
    (initialAdvanced?.secrets ?? []).map((s, i) => ({ id: i + 1, ...s })),
  );
  const [hardening, setHardening] = useState(
    initialAdvanced?.hardening ?? { readOnlyRootfs: false, capabilities: [] as string[] },
  );
  const [pids, setPids] = useState(initialAdvanced?.pids ?? "");
  // The YAML is a projection of the form until someone types in it; from then on it is the
  // document, because rebuilding it from the fields would silently delete whatever the form
  // cannot express — a sidecar, most of all.
  const [yaml, setYaml] = useState(initialYaml);
  // A re-run arrives with both a filled form and the spec it came from, and those agree — so
  // the document only takes over when the caller says the form cannot hold this spec.
  const [yamlEdited, setYamlEdited] = useState(
    initialMode === "yaml" && initialYaml.trim() !== "",
  );
  const [attempted, setAttempted] = useState(false);
  const [serverProblems, setServerProblems] = useState<string[]>([]);
  const [failure, setFailure] = useState<string>();
  const [open, setOpen] = useState<Partial<Record<SectionId, boolean>>>({});

  const set = <K extends keyof Fields>(key: K, value: Fields[K]) =>
    setFields((prev) => ({ ...prev, [key]: value }));

  const built = useMemo(
    () => buildDoc({ ...fields, env: joinEnv(envRows) }, secretRows, hardening, pids),
    [fields, envRows, secretRows, hardening, pids],
  );
  const formYaml = useMemo(() => docToYaml(built.doc), [built.doc]);
  const specYaml = yamlEdited ? yaml : formYaml;

  const parsed = useMemo(
    () => (yamlEdited ? parseSpecYaml(specYaml) : parseSpecValue(built.doc)),
    [yamlEdited, specYaml, built],
  );
  const localProblems = useMemo(
    () => (yamlEdited ? parsed.problems : [...built.problems, ...parsed.problems]),
    [yamlEdited, parsed, built],
  );

  // Nothing is red before the first submit: the operator is told what is wrong once they say
  // they are done, and from then on the form keeps up with them as they fix it.
  const problems = useMemo(
    () => (attempted ? [...localProblems, ...serverProblems] : []),
    [attempted, localProblems, serverProblems],
  );

  const create = useMutation({
    mutationFn: (spec: SpecInit) => tasks.createTask({ spec }),
    onSuccess: (res) => {
      const id = res.task?.id;
      if (!id) return;
      toast(`Task ${id} created.`, "ok");
      void navigate(`/tasks/${id}`);
    },
    onError: (err) => {
      // pkg/spec.Validate reports every problem at once and errors.Join renders them one per
      // line, so an InvalidArgument is a list, not a sentence. Splitting it is the difference
      // between fixing one mistake per round trip and fixing all of them.
      if (connectCode(err) === Code.InvalidArgument) {
        setServerProblems(splitServerProblems(errorMessage(err)));
        return;
      }
      setFailure(errorMessage(err));
    },
  });

  const submit = () => {
    setAttempted(true);
    setServerProblems([]);
    setFailure(undefined);
    // Whatever is wrong is already on screen, at the field that carries it.
    if (built.problems.length > 0 && !yamlEdited) return;
    if (!parsed.spec) return;
    create.mutate(parsed.spec);
  };

  // A problem is addressed by the path it names — "image: is required",
  // "secrets[0].key is required", "resources.cpu must be positive" — so the same string can be
  // both listed and shown under the control it belongs to. When the YAML has been hand-edited
  // the fields are not the document, so nothing is pinned to them.
  const byPath = useMemo(() => {
    const map = new Map<string, string[]>();
    if (yamlEdited) return map;
    for (const problem of problems) {
      const head = problem.split(/[:\s]/)[0] ?? "";
      if (head === "") continue;
      const keys = new Set([
        head,
        head.replace(/\[\d+\]/g, ""),
        head.split(".")[0].replace(/\[\d+\]/g, ""),
      ]);
      for (const key of keys) map.set(key, [...(map.get(key) ?? []), problem]);
    }
    return map;
  }, [problems, yamlEdited]);

  const at = (path: string) => byPath.get(path) ?? [];
  const count = (...paths: string[]) => paths.reduce((n, path) => n + at(path).length, 0);

  const labels = fields.labels
    .split(",")
    .map((l) => l.trim())
    .filter((l) => l !== "");
  const limitsSet = [fields.timeout, fields.maxAttempts, fields.cpu, fields.memoryMb, pids].some(
    (v) => v.trim() !== "",
  );

  const filled: Record<SectionId, boolean> = {
    env: envRows.length > 0,
    placement: labels.length > 0,
    limits: limitsSet || fields.retryOnNodeLoss,
    secrets: secretRows.length > 0,
    hardening: hardening.readOnlyRootfs || hardening.capabilities.length > 0,
    sidecars: false,
  };
  const bad: Record<SectionId, number> = {
    env: count("env"),
    placement: count("labels"),
    limits: count("timeout", "max_attempts", "resources", "retry_on_node_loss"),
    secrets: count("secrets"),
    hardening: count("hardening"),
    sidecars: count("sidecars"),
  };
  const section = (id: SectionId) => ({
    problems: bad[id],
    open: open[id] ?? (filled[id] || bad[id] > 0),
    onOpenChange: (v: boolean) => setOpen((prev) => ({ ...prev, [id]: v })),
  });

  const blocked = attempted && problems.length > 0;

  return (
    <form
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <fieldset disabled={create.isPending} className="contents">
        <Tabs value={mode} onValueChange={(v) => setMode(v as Mode)}>
          <div className="flex flex-wrap items-center gap-3">
            <TabsList>
              <TabsTrigger value="form">
                <SlidersHorizontal />
                Form
              </TabsTrigger>
              <TabsTrigger value="yaml">
                <FileCode2 />
                YAML
              </TabsTrigger>
            </TabsList>
            <span className="text-2xs text-faint">Two views of one spec.</span>
            {rerunOf ? (
              <Chip className="ml-auto">
                pre-filled from <span className="font-mono text-muted">{rerunOf}</span>
              </Chip>
            ) : null}
          </div>

          <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_21rem] xl:items-start">
            <div className="min-w-0 space-y-4">
              {failure ? (
                <Alert role="alert" variant="destructive" title="CreateTask failed">
                  <p>{failure}</p>
                  <p className="mt-1 opacity-80">
                    Nothing was queued. The spec is still here — submit it again once the server
                    answers.
                  </p>
                </Alert>
              ) : null}

              {mode === "form" && problems.length > 0 ? <ProblemList problems={problems} /> : null}

              <TabsContent value="form" className="space-y-4">
                {yamlEdited ? (
                  <Alert role="alert" variant="warn" title="The YAML document is the spec">
                    <p>
                      This spec is edited as YAML, and that document is what goes to the server —
                      it can hold things these fields cannot, a sidecar most of all. Rebuilding
                      from the fields discards whatever they cannot express.
                    </p>
                    <Button
                      type="button"
                      variant="outline"
                      size="sm"
                      className="mt-2"
                      onClick={() => {
                        setYamlEdited(false);
                        setYaml("");
                      }}
                    >
                      <RotateCcw />
                      Use these fields instead
                    </Button>
                  </Alert>
                ) : null}

                <Card>
                  <CardHeader>
                    <div>
                      <CardTitle>Container</CardTitle>
                      <CardDescription>
                        What runs. Everything under it is optional and has a working default.
                      </CardDescription>
                    </div>
                  </CardHeader>
                  <CardContent className="space-y-4">
                    <Field
                      id="spec-image"
                      label="Image"
                      required
                      problems={tails(at("image"))}
                      hint="Pulled on the node that runs the task, so a tag resolves there and not here."
                    >
                      {(a) => (
                        <Input
                          {...a}
                          value={fields.image}
                          onChange={(e) => set("image", e.target.value)}
                          placeholder="alpine:3"
                          spellCheck={false}
                          autoCapitalize="off"
                          autoCorrect="off"
                          className="h-10 font-mono text-sm"
                        />
                      )}
                    </Field>

                    <Field
                      id="spec-command"
                      label="Command"
                      problems={tails(at("command"))}
                      hint={
                        <>
                          Empty runs the image&apos;s own entrypoint. One line per argument, so a
                          shell command is three lines: <code className="font-mono">sh</code>,{" "}
                          <code className="font-mono">-c</code>, then the script.
                        </>
                      }
                    >
                      {(a) => (
                        <Textarea
                          {...a}
                          value={fields.command}
                          onChange={(e) => set("command", e.target.value)}
                          rows={5}
                          spellCheck={false}
                          placeholder={"sh\n-c\necho hello"}
                          className={MONO}
                        />
                      )}
                    </Field>

                    <Field
                      id="spec-working-dir"
                      label="Working directory"
                      problems={tails(at("working_dir"))}
                      hint="Defaults to whatever the image sets."
                      className="max-w-sm"
                    >
                      {(a) => (
                        <Input
                          {...a}
                          value={fields.workingDir}
                          onChange={(e) => set("workingDir", e.target.value)}
                          placeholder="/workspace"
                          spellCheck={false}
                          className={MONO}
                        />
                      )}
                    </Field>
                  </CardContent>
                </Card>

                <Section
                  title="Environment"
                  description="Variables the task starts with"
                  summary={
                    envRows.length > 0
                      ? `${envRows.length} ${envRows.length === 1 ? "variable" : "variables"}`
                      : undefined
                  }
                  {...section("env")}
                >
                  <div className="space-y-3">
                    <FieldProblems problems={at("env")} />
                    <KeyValueEditor rows={envRows} onChange={setEnvRows} />
                    <p className="text-2xs leading-relaxed text-muted">
                      Names must be shell identifiers. A value here is stored with the task and
                      shown on its detail screen — anything that must not be read back belongs in
                      Secrets.
                    </p>
                  </div>
                </Section>

                <Section
                  title="Placement"
                  description="Which nodes may run this"
                  summary={labels.length > 0 ? labels.join(", ") : undefined}
                  {...section("placement")}
                >
                  <div className="space-y-3">
                    <Field
                      id="spec-labels"
                      label="Labels"
                      problems={tails(at("labels"))}
                      hint="Comma separated. The task waits for a node that carries every one of them; no labels means any online node."
                    >
                      {(a) => (
                        <Input
                          {...a}
                          value={fields.labels}
                          onChange={(e) => set("labels", e.target.value)}
                          placeholder="linux/arm64, browser"
                          spellCheck={false}
                          className={MONO}
                        />
                      )}
                    </Field>
                    {labels.length > 0 ? (
                      <div className="flex flex-wrap items-center gap-1.5">
                        <span className="text-2xs text-faint">Requires</span>
                        {labels.map((l) => (
                          <Chip key={l} className="font-mono">
                            {l}
                          </Chip>
                        ))}
                      </div>
                    ) : null}
                  </div>
                </Section>

                <Section
                  title="Limits and retries"
                  description="How long it may run, how much it may use, and what happens if it does not finish"
                  summary={limitsSummary(fields, pids)}
                  {...section("limits")}
                >
                  <div className="space-y-4">
                    <div className="grid gap-3 sm:grid-cols-3">
                      <Field
                        id="spec-timeout"
                        label="Timeout"
                        problems={tails(at("timeout"))}
                        hint="1h30m, 90s"
                      >
                        {(a) => (
                          <Input
                            {...a}
                            value={fields.timeout}
                            onChange={(e) => set("timeout", e.target.value)}
                            placeholder="1h"
                            className={`${NUM} font-mono`}
                          />
                        )}
                      </Field>
                      <Field
                        id="spec-max-attempts"
                        label="Max attempts"
                        problems={tails(at("max_attempts"))}
                        hint="1 = no retry"
                      >
                        {(a) => (
                          <Input
                            {...a}
                            value={fields.maxAttempts}
                            onChange={(e) => set("maxAttempts", e.target.value)}
                            placeholder="1"
                            inputMode="numeric"
                            className={NUM}
                          />
                        )}
                      </Field>
                      <Field
                        id="spec-pids"
                        label="PID limit"
                        problems={tails(at("resources.pids"))}
                        hint="0 = unlimited"
                      >
                        {(a) => (
                          <Input
                            {...a}
                            value={pids}
                            onChange={(e) => setPids(e.target.value)}
                            placeholder="0"
                            inputMode="numeric"
                            className={NUM}
                          />
                        )}
                      </Field>
                      <Field
                        id="spec-cpu"
                        label="CPU cores"
                        problems={tails(at("resources.cpu"))}
                        hint="0 = unlimited"
                      >
                        {(a) => (
                          <Input
                            {...a}
                            value={fields.cpu}
                            onChange={(e) => set("cpu", e.target.value)}
                            placeholder="0"
                            inputMode="decimal"
                            className={NUM}
                          />
                        )}
                      </Field>
                      <Field
                        id="spec-memory"
                        label="Memory (MB)"
                        problems={tails(at("resources.memory_mb"))}
                        hint="0 = unlimited"
                      >
                        {(a) => (
                          <Input
                            {...a}
                            value={fields.memoryMb}
                            onChange={(e) => set("memoryMb", e.target.value)}
                            placeholder="0"
                            inputMode="numeric"
                            className={NUM}
                          />
                        )}
                      </Field>
                    </div>

                    <div className="flex items-start gap-3 rounded-lg border border-hairline bg-raised/40 px-3 py-2.5">
                      <Switch
                        id="spec-retry-node-loss"
                        aria-labelledby="spec-retry-node-loss-label"
                        checked={fields.retryOnNodeLoss}
                        onCheckedChange={(v) => set("retryOnNodeLoss", v)}
                        className="mt-0.5 border-border"
                      />
                      <div className="space-y-0.5">
                        <Label
                          id="spec-retry-node-loss-label"
                          htmlFor="spec-retry-node-loss"
                          className="text-fg"
                        >
                          Re-run on another node if this one goes offline mid-task
                        </Label>
                        <p className="text-2xs leading-relaxed text-muted">
                          Off unless the task is safe to run twice: the first attempt may have
                          finished its work before the node went quiet.
                        </p>
                      </div>
                    </div>
                  </div>
                </Section>

                <Section
                  title="Secrets"
                  description="Stored values to hand the task"
                  summary={
                    secretRows.length > 0
                      ? `${secretRows.length} ${secretRows.length === 1 ? "secret" : "secrets"}`
                      : undefined
                  }
                  {...section("secrets")}
                >
                  <div className="space-y-3">
                    <SecretsEditor
                      rows={secretRows}
                      onChange={setSecretRows}
                      problems={(i, f) => tails(at(`secrets[${i}].${f}`))}
                    />
                    <p className="text-2xs leading-relaxed text-muted">
                      A ref names a secret, never its value: the server resolves it on the way to
                      the node. <span className="font-mono">env</span> sets the variable named by
                      the key; <span className="font-mono">file</span> mounts the value at the
                      key&apos;s absolute path, and{" "}
                      <span className="font-mono">/podium/secrets</span> is the tmpfs every task
                      gets.
                    </p>
                  </div>
                </Section>

                <Section
                  title="Hardening"
                  description="The two dials on the sandbox"
                  summary={hardeningSummary(hardening)}
                  {...section("hardening")}
                >
                  <div className="space-y-4">
                    <FieldProblems problems={at("hardening")} />
                    <div className="flex items-start gap-3 rounded-lg border border-hairline bg-raised/40 px-3 py-2.5">
                      <Switch
                        id="spec-read-only-rootfs"
                        aria-labelledby="spec-read-only-rootfs-label"
                        checked={hardening.readOnlyRootfs}
                        onCheckedChange={(v) =>
                          setHardening((prev) => ({ ...prev, readOnlyRootfs: v }))
                        }
                        className="mt-0.5 border-border"
                      />
                      <div className="space-y-0.5">
                        <Label
                          id="spec-read-only-rootfs-label"
                          htmlFor="spec-read-only-rootfs"
                          className="text-fg"
                        >
                          Read-only root filesystem
                        </Label>
                        <p className="text-2xs leading-relaxed text-muted">
                          The image&apos;s own layers become unwritable. Anything the task needs to
                          write has to go somewhere it mounts itself.
                        </p>
                      </div>
                    </div>
                    <div className="space-y-1.5">
                      <Label>Capabilities</Label>
                      <ToggleGroup
                        type="multiple"
                        value={hardening.capabilities}
                        onValueChange={(v) =>
                          setHardening((prev) => ({ ...prev, capabilities: v }))
                        }
                        className="w-full flex-wrap justify-start"
                      >
                        {CAPABILITIES.map((c) => (
                          <ToggleGroupItem
                            key={c}
                            value={c}
                            className="font-mono data-[state=on]:bg-accent/15 data-[state=on]:text-accent"
                          >
                            {c}
                          </ToggleGroupItem>
                        ))}
                      </ToggleGroup>
                      <p className="text-2xs leading-relaxed text-muted">
                        Every capability is dropped before the task starts. These seven are the
                        only ones a spec may ask back for; anything else is a node-level decision
                        and is rejected here.
                      </p>
                    </div>
                  </div>
                </Section>

                <Section
                  title="Sidecars"
                  description="Containers started beside the task"
                  summary="YAML only"
                  {...section("sidecars")}
                >
                  <div className="space-y-3">
                    <p className="text-xs leading-relaxed text-muted">
                      A sidecar is a sibling container started before the task and reachable from
                      it by the name it is keyed under — a database, a browser, a Docker daemon.
                      Each one carries its own image, command, readiness probe and limits, which is
                      more structure than this form should pretend to flatten, so sidecars are
                      written in the spec document itself.
                    </p>
                    <Button type="button" variant="outline" size="sm" onClick={() => setMode("yaml")}>
                      <FileCode2 />
                      Edit the spec as YAML
                    </Button>
                  </div>
                </Section>
              </TabsContent>

              <TabsContent value="yaml">
                <Card>
                  <CardHeader>
                    <div>
                      <CardTitle id="spec-yaml-label">Task spec YAML</CardTitle>
                      <CardDescription>
                        {yamlEdited
                          ? "Edited by hand, so this document is the spec. The form's fields no longer feed it."
                          : "Written from the form as you fill it in. Type here and this document becomes the spec instead."}
                      </CardDescription>
                    </div>
                    {yamlEdited ? (
                      <Button
                        type="button"
                        variant="outline"
                        size="sm"
                        onClick={() => {
                          setYamlEdited(false);
                          setYaml("");
                        }}
                      >
                        <RotateCcw />
                        Rebuild from form
                      </Button>
                    ) : null}
                  </CardHeader>
                  <CardContent className="space-y-2">
                    <Textarea
                      id="spec-yaml"
                      aria-labelledby="spec-yaml-label"
                      aria-invalid={(attempted && localProblems.length > 0) || undefined}
                      value={specYaml}
                      onChange={(e) => {
                        setYamlEdited(true);
                        setYaml(e.target.value);
                      }}
                      rows={22}
                      spellCheck={false}
                      autoCapitalize="off"
                      autoCorrect="off"
                      className={`${MONO} min-h-96 leading-5`}
                    />
                    {problems.length > 0 ? (
                      <ProblemList problems={problems} />
                    ) : (
                      <p className="text-2xs leading-relaxed text-muted">
                        The same document <span className="font-mono">podium run --spec</span>{" "}
                        takes. A field the schema does not have is rejected here, not ignored — a
                        misspelled <span className="font-mono">privilged: true</span> must not
                        submit a task that quietly does something else.
                      </p>
                    )}
                  </CardContent>
                </Card>
              </TabsContent>
            </div>

            <aside className="space-y-3 xl:sticky xl:top-4">
              {mode === "yaml" ? (
                <SpecDigest spec={parsed.spec} />
              ) : (
                <SpecPreview
                  yaml={specYaml}
                  empty={
                    yamlEdited
                      ? specYaml.trim() === ""
                      : Object.keys(built.doc).length === 1 && !built.doc.image
                  }
                />
              )}
              <Button type="submit" className="w-full" disabled={create.isPending || blocked}>
                {create.isPending ? "Submitting…" : "Submit task"}
              </Button>
              <p className="text-2xs leading-relaxed text-muted">
                {create.isPending
                  ? "Waiting for the server to accept the spec."
                  : blocked
                    ? `${problems.length} ${problems.length === 1 ? "problem" : "problems"} to fix before this can be submitted.`
                    : "Queued the moment it is accepted. The scheduler places it on the first eligible node — until one is free, the task sits in queued."}
              </p>
            </aside>
          </div>
        </Tabs>
      </fieldset>
    </form>
  );
}

function ProblemList({ problems }: { problems: string[] }) {
  return (
    <Alert
      role="alert"
      variant="destructive"
      title={`${problems.length} ${problems.length === 1 ? "problem" : "problems"} to fix`}
    >
      <ul data-testid="spec-problems" className="mt-1 space-y-1">
        {problems.map((p, i) => (
          <li key={i} className="font-mono break-words">
            {p}
          </li>
        ))}
      </ul>
    </Alert>
  );
}

/**
 * buildDoc adds what `Fields` does not carry to the document `fieldsToDoc` builds, so the form
 * and the editor still meet in one decoder with one set of messages.
 */
function buildDoc(
  fields: Fields,
  secrets: SecretRow[],
  hardening: { readOnlyRootfs: boolean; capabilities: string[] },
  pids: string,
): { doc: SpecDoc; problems: string[] } {
  const { doc, problems } = fieldsToDoc(fields);

  if (pids.trim() !== "") {
    const n = Number(pids);
    if (!Number.isFinite(n)) {
      problems.push(`resources.pids: ${JSON.stringify(pids.trim())} is not a number`);
    } else {
      doc.resources = { ...doc.resources, pids: n };
    }
  }

  const named = secrets.filter((s) => s.name.trim() !== "" || s.key.trim() !== "");
  if (named.length > 0) {
    doc.secrets = named.map((s) => ({
      name: s.name.trim(),
      target: s.target,
      key: s.key.trim(),
    }));
  }

  if (hardening.readOnlyRootfs || hardening.capabilities.length > 0) {
    doc.hardening = {};
    if (hardening.readOnlyRootfs) doc.hardening.read_only_rootfs = true;
    if (hardening.capabilities.length > 0) {
      doc.hardening.capabilities = [...hardening.capabilities];
    }
  }

  return { doc, problems };
}

function splitEnv(text: string): EnvRow[] {
  return text
    .split("\n")
    .filter((line) => line.trim() !== "")
    .map((line, i) => {
      const eq = line.indexOf("=");
      return eq < 0
        ? { id: i + 1, key: line.trim(), value: "" }
        : { id: i + 1, key: line.slice(0, eq).trim(), value: line.slice(eq + 1) };
    });
}

function joinEnv(rows: EnvRow[]): string {
  return rows
    .filter((r) => r.key.trim() !== "")
    .map((r) => `${r.key.trim()}=${r.value}`)
    .join("\n");
}

/** The path is already the field's label, so the message under a control drops it. */
function tails(problems: string[]): string[] {
  return problems.map((p) => p.replace(/^\S+?[:\s]\s*/, ""));
}

function limitsSummary(fields: Fields, pids: string): string | undefined {
  const parts = [
    fields.timeout.trim(),
    fields.maxAttempts.trim() ? `${fields.maxAttempts.trim()} attempts` : "",
    fields.cpu.trim() ? `${fields.cpu.trim()} CPU` : "",
    fields.memoryMb.trim() ? `${fields.memoryMb.trim()} MB` : "",
    pids.trim() ? `${pids.trim()} PIDs` : "",
    fields.retryOnNodeLoss ? "retry on node loss" : "",
  ].filter((p) => p !== "");
  return parts.length > 0 ? parts.join(" · ") : undefined;
}

function hardeningSummary(hardening: {
  readOnlyRootfs: boolean;
  capabilities: string[];
}): string | undefined {
  const parts = [
    hardening.readOnlyRootfs ? "read-only rootfs" : "",
    hardening.capabilities.length > 0 ? hardening.capabilities.join(", ") : "",
  ].filter((p) => p !== "");
  return parts.length > 0 ? parts.join(" · ") : undefined;
}
