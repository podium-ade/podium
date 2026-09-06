import { useQuery } from "@tanstack/react-query";
import { Link, useSearchParams } from "react-router";
import { PageHeader } from "../components/PageHeader";
import { Skeleton } from "../components/Skeleton";
import { SpecForm } from "../components/SpecForm";
import type { TaskSpec } from "../gen/podium/v1/common_pb";
import { errorMessage, tasks } from "../lib/client";
import {
  EMPTY_FIELDS,
  formatGoDuration,
  hasAdvancedFields,
  specToYaml,
  type Fields,
} from "../lib/spec";

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
      <div className="space-y-3">
        <Skeleton className="h-6 w-64" />
        <Skeleton className="h-96 w-full" />
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
        actions={
          <Link to="/" className="text-xs text-muted hover:text-fg">
            ← Tasks
          </Link>
        }
      />

      {rerun !== "" && source.error ? (
        <p className="text-xs text-err">
          Could not read {rerun}: {errorMessage(source.error)}. The form below is empty.
        </p>
      ) : null}

      <SpecForm
        key={rerun}
        rerunOf={spec ? rerun : undefined}
        initialFields={spec ? fieldsFromSpec(spec) : EMPTY_FIELDS}
        initialYaml={spec ? specToYaml(spec) : ""}
        // A spec with sidecars, secrets or hardening in it cannot be shown truthfully by the
        // simple form, so it opens in YAML rather than quietly dropping half of itself.
        initialMode={spec && hasAdvancedFields(spec) ? "yaml" : "form"}
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
