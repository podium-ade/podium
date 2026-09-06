import { useQuery } from "@tanstack/react-query";
import { useSearchParams } from "react-router";
import { PageHeader } from "../components/PageHeader";
import { Skeleton } from "../components/Skeleton";
import { SpecForm, type Advanced } from "../components/SpecForm";
import { Alert } from "../components/ui/alert";
import type { TaskSpec } from "../gen/podium/v1/common_pb";
import { errorMessage, tasks } from "../lib/client";
import { EMPTY_FIELDS, formatGoDuration, specToYaml, type Fields } from "../lib/spec";

/**
 * SubmitPage is the form, and `?rerun=<task_id>` is the same form pre-filled from a task that
 * has already run.
 *
 * Re-run is a *new task*, not a restart: a terminal task has no outgoing edges in the state
 * graph, and nothing on the wire pretends otherwise. What is carried over is the spec.
 */
export function SubmitPage() {
  const [params] = useSearchParams();
  const rerun = params.get("rerun") ?? "";

  const source = useQuery({
    queryKey: ["task", rerun],
    queryFn: () => tasks.getTask({ taskId: rerun }),
    enabled: rerun !== "",
  });

  if (rerun !== "" && source.isPending) {
    return (
      <div className="space-y-5">
        <div className="space-y-2">
          <Skeleton className="h-7 w-48" />
          <Skeleton className="h-4 w-96" />
        </div>
        <Skeleton className="h-9 w-40" />
        <div className="grid gap-5 xl:grid-cols-[minmax(0,1fr)_21rem] xl:items-start">
          <Skeleton className="h-96 w-full" />
          <Skeleton className="h-64 w-full" />
        </div>
      </div>
    );
  }

  const spec = source.data?.task?.spec;

  return (
    <div className="space-y-5">
      <PageHeader
        title={rerun ? "Re-run task" : "New task"}
        description={
          rerun
            ? "A new task from this spec — a terminal task has no outgoing edges, so this is not a restart."
            : "Queue work. The scheduler places it on the next eligible node."
        }
        back={{ to: "/", label: "Tasks" }}
      />

      {rerun !== "" && source.error ? (
        <Alert variant="warn" title={`Could not read ${rerun}`}>
          {errorMessage(source.error)}. The form below is empty — the spec could not be copied
          from it.
        </Alert>
      ) : null}

      <SpecForm
        key={rerun}
        rerunOf={spec ? rerun : undefined}
        initialFields={spec ? fieldsFromSpec(spec) : EMPTY_FIELDS}
        initialAdvanced={spec ? advancedFromSpec(spec) : undefined}
        initialYaml={spec ? specToYaml(spec) : ""}
        // Sidecars are the one thing the form still cannot show truthfully, so a spec that has
        // them opens in YAML rather than quietly dropping half of itself. Secrets and hardening
        // are pre-filled into the form itself.
        initialMode={spec && Object.keys(spec.sidecars).length > 0 ? "yaml" : "form"}
      />
    </div>
  );
}

function fieldsFromSpec(spec: TaskSpec): Fields {
  return {
    image: spec.image,
    command: spec.command.join("\n"),
    workingDir: spec.workingDir,
    env: Object.keys(spec.env)
      .sort()
      .map((k) => `${k}=${spec.env[k]}`)
      .join("\n"),
    labels: spec.labels.join(", "),
    timeout: spec.timeout
      ? formatGoDuration(Number(spec.timeout.seconds), spec.timeout.nanos)
      : "",
    maxAttempts: spec.maxAttempts ? String(spec.maxAttempts) : "",
    cpu: spec.resources?.cpu ? String(spec.resources.cpu) : "",
    memoryMb: spec.resources?.memoryMb ? String(spec.resources.memoryMb) : "",
    retryOnNodeLoss: spec.retryOnNodeLoss,
  };
}

function advancedFromSpec(spec: TaskSpec): Advanced {
  return {
    secrets: spec.secrets.map((s) => ({
      name: s.name,
      target: s.target || "env",
      key: s.key,
    })),
    hardening: {
      readOnlyRootfs: spec.hardening?.readOnlyRootfs ?? false,
      capabilities: [...(spec.hardening?.capabilities ?? [])],
    },
    pids: spec.resources?.pids ? String(spec.resources.pids) : "",
  };
}
