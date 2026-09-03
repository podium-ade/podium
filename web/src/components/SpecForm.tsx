import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { useNavigate } from "react-router";
import { useToast } from "./Toast";
import { Code, connectCode, errorMessage, tasks } from "../lib/client";
import {
  docToYaml,
  fieldsToDoc,
  parseSpecValue,
  parseSpecYaml,
  splitServerProblems,
  EMPTY_FIELDS,
  type Fields,
  type SpecInit,
} from "../lib/spec";

type Mode = "form" | "yaml";

const input =
  "w-full rounded border border-border bg-bg px-2 py-1 font-mono text-xs outline-none focus:border-accent";

export function SpecForm({
  initialFields = EMPTY_FIELDS,
  initialYaml = "",
  initialMode = "form",
  rerunOf,
}: {
  initialFields?: Fields;
  initialYaml?: string;
  initialMode?: Mode;
  rerunOf?: string;
}) {
  const navigate = useNavigate();
  const toast = useToast();
  const [mode, setMode] = useState<Mode>(initialMode);
  const [fields, setFields] = useState<Fields>(initialFields);
  const [yaml, setYaml] = useState(initialYaml);
  const [problems, setProblems] = useState<string[]>([]);

  const set = <K extends keyof Fields>(key: K, value: Fields[K]) =>
    setFields((prev) => ({ ...prev, [key]: value }));

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
        setProblems(splitServerProblems(errorMessage(err)));
        return;
      }
      toast(`CreateTask: ${errorMessage(err)}`);
    },
  });

  const submit = () => {
    setProblems([]);
    if (mode === "yaml") {
      const parsed = parseSpecYaml(yaml);
      if (!parsed.spec) {
        setProblems(parsed.problems);
        return;
      }
      create.mutate(parsed.spec);
      return;
    }
    const { doc, problems: local } = fieldsToDoc(fields);
    const parsed = parseSpecValue(doc);
    const all = [...local, ...parsed.problems];
    if (all.length > 0 || !parsed.spec) {
      setProblems(all.length > 0 ? all : ["the spec is empty"]);
      return;
    }
    create.mutate(parsed.spec);
  };

  const fillFromForm = () => {
    const { doc } = fieldsToDoc(fields);
    setYaml(docToYaml(doc));
  };

  return (
    <form
      className="space-y-4"
      onSubmit={(e) => {
        e.preventDefault();
        submit();
      }}
    >
      <div className="flex flex-wrap items-center gap-2 text-xs">
        <div className="flex overflow-hidden rounded border border-border">
          {(["form", "yaml"] as Mode[]).map((m) => (
            <button
              key={m}
              type="button"
              aria-pressed={mode === m}
              onClick={() => {
                if (m === "yaml" && yaml.trim() === "") fillFromForm();
                setMode(m);
              }}
              className={`px-3 py-1 ${mode === m ? "bg-accent text-bg" : "text-muted hover:text-fg"}`}
            >
              {m === "form" ? "Form" : "YAML spec"}
            </button>
          ))}
        </div>
        {rerunOf ? (
          <span className="text-muted">
            pre-filled from <span className="font-mono">{rerunOf}</span>
          </span>
        ) : null}
      </div>

      {problems.length > 0 ? (
        <ul
          data-testid="spec-problems"
          className="space-y-1 rounded border border-err/50 bg-err/10 px-3 py-2 text-xs text-err"
        >
          {problems.map((p, i) => (
            <li key={i} className="font-mono break-words">
              {p}
            </li>
          ))}
        </ul>
      ) : null}

      {mode === "form" ? (
        <div className="space-y-4 rounded border border-border bg-panel p-3">
          <div className="grid gap-3 sm:grid-cols-2">
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted">Image</span>
              <input
                aria-label="Image"
                value={fields.image}
                onChange={(e) => set("image", e.target.value)}
                placeholder="alpine:3"
                className={input}
              />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted">Working directory</span>
              <input
                aria-label="Working directory"
                value={fields.workingDir}
                onChange={(e) => set("workingDir", e.target.value)}
                placeholder="/workspace"
                className={input}
              />
            </label>
          </div>

          <label className="flex flex-col gap-1 text-xs">
            <span className="text-muted">Command — one argument per line</span>
            <textarea
              aria-label="Command"
              value={fields.command}
              onChange={(e) => set("command", e.target.value)}
              rows={4}
              spellCheck={false}
              placeholder={"sh\n-c\necho hello"}
              className={input}
            />
            <span className="text-muted">
              Empty runs the image&apos;s own entrypoint. One line per argument, so a shell
              command is three lines: <code className="font-mono">sh</code>,{" "}
              <code className="font-mono">-c</code>, then the script.
            </span>
          </label>

          <div className="grid gap-3 sm:grid-cols-2">
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted">Environment — KEY=VALUE per line</span>
              <textarea
                aria-label="Environment"
                value={fields.env}
                onChange={(e) => set("env", e.target.value)}
                rows={3}
                spellCheck={false}
                placeholder="CI=true"
                className={input}
              />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted">Node labels — comma separated</span>
              <input
                aria-label="Labels"
                value={fields.labels}
                onChange={(e) => set("labels", e.target.value)}
                placeholder="linux/arm64, browser"
                className={input}
              />
            </label>
          </div>

          <div className="grid gap-3 sm:grid-cols-4">
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted">Timeout</span>
              <input
                aria-label="Timeout"
                value={fields.timeout}
                onChange={(e) => set("timeout", e.target.value)}
                placeholder="1h"
                className={input}
              />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted">Max attempts</span>
              <input
                aria-label="Max attempts"
                value={fields.maxAttempts}
                onChange={(e) => set("maxAttempts", e.target.value)}
                placeholder="1"
                className={input}
              />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted">CPU cores</span>
              <input
                aria-label="CPU cores"
                value={fields.cpu}
                onChange={(e) => set("cpu", e.target.value)}
                placeholder="0 = unlimited"
                className={input}
              />
            </label>
            <label className="flex flex-col gap-1 text-xs">
              <span className="text-muted">Memory (MB)</span>
              <input
                aria-label="Memory MB"
                value={fields.memoryMb}
                onChange={(e) => set("memoryMb", e.target.value)}
                placeholder="0 = unlimited"
                className={input}
              />
            </label>
          </div>

          <label className="flex items-center gap-2 text-xs">
            <input
              type="checkbox"
              checked={fields.retryOnNodeLoss}
              onChange={(e) => set("retryOnNodeLoss", e.target.checked)}
            />
            <span>
              Re-run on another node if this one goes offline mid-task
              <span className="text-muted">
                {" "}
                — leave off unless the task is safe to run twice.
              </span>
            </span>
          </label>

          <p className="text-xs text-muted">
            Sidecars, secrets and hardening are not in this form. Switch to{" "}
            <b>YAML spec</b> for those; it takes the same document as{" "}
            <code className="font-mono">podium run --spec file.yaml</code>.
          </p>
        </div>
      ) : (
        <div className="space-y-2 rounded border border-border bg-panel p-3">
          <div className="flex items-center gap-3 text-xs">
            <span className="text-muted">
              The same document <code className="font-mono">podium run --spec</code> takes.
              Unknown fields are rejected here, not ignored.
            </span>
            <button
              type="button"
              onClick={fillFromForm}
              className="ml-auto rounded border border-border px-2 py-0.5 hover:border-accent"
            >
              Fill from form
            </button>
          </div>
          <textarea
            aria-label="Task spec YAML"
            value={yaml}
            onChange={(e) => setYaml(e.target.value)}
            rows={22}
            spellCheck={false}
            autoCapitalize="off"
            autoCorrect="off"
            className={`${input} leading-5`}
          />
        </div>
      )}

      <div className="flex items-center gap-3">
        <button
          type="submit"
          disabled={create.isPending}
          className="rounded bg-accent px-3 py-1.5 text-sm font-medium text-bg disabled:opacity-50"
        >
          {create.isPending ? "Submitting…" : "Submit task"}
        </button>
        <span className="text-xs text-muted">
          The task is queued immediately; the scheduler places it on the next eligible node.
        </span>
      </div>
    </form>
  );
}
