import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Badge } from "./Badge";
import { useToast } from "./Toast";
import type { Artifact } from "../gen/podium/v1/artifact_pb";
import { download } from "../lib/artifacts";
import { artifacts as artifactClient, Code, connectCode, errorMessage } from "../lib/client";
import { absolute, humanBytes, relative } from "../lib/format";

/**
 * ArtifactsPanel lists what a task left behind.
 *
 * `kind` is either "file" — something the task wrote to /workspace/.podium/artifacts — or
 * "log", the task's own output after it aged out of Postgres and into the object store. They
 * are shown apart because a rolled-up log in the same list as a screenshot reads as a second
 * screenshot, and because a log row's size is the *compressed* size, which is not comparable
 * with a file's.
 */
export function ArtifactsPanel({ taskId, refetch }: { taskId: string; refetch: boolean }) {
  const toast = useToast();
  const query = useQuery({
    queryKey: ["artifacts", taskId],
    queryFn: () => artifactClient.listArtifacts({ taskId }),
    // A running task gains artifacts as it goes; a finished one gains its rolled-up log a
    // minute or so after it ends, so keep looking for a while either way.
    refetchInterval: refetch ? 10_000 : false,
    placeholderData: (prev) => prev,
  });

  useEffect(() => {
    if (query.error && connectCode(query.error) !== Code.FailedPrecondition) {
      toast(`ListArtifacts: ${errorMessage(query.error)}`);
    }
  }, [query.error, toast]);

  // A server with no object store answers FailedPrecondition for every task. Saying it once,
  // as a fact about the deployment, is better than a row-shaped error.
  if (connectCode(query.error) === Code.FailedPrecondition) {
    return (
      <section className="rounded-xl border border-border bg-card px-3 py-2 text-xs text-muted shadow-xs">
        <span className="font-medium text-fg">Artifacts</span> — this server has no object store
        configured (<code className="font-mono">PODIUM_S3_ENDPOINT</code>), so nothing a task
        produces is kept.
      </section>
    );
  }

  const rows = query.data?.artifacts ?? [];
  const files = rows.filter((a) => a.kind !== "log");
  const logs = rows.filter((a) => a.kind === "log");

  return (
    <section className="rounded-xl border border-border bg-card shadow-xs">
      <h2 className="border-b border-border px-3 py-2 text-xs font-medium">
        Artifacts{rows.length > 0 ? ` (${rows.length})` : ""}
      </h2>
      {query.isPending ? (
        <p className="px-3 py-2 text-xs text-muted">loading…</p>
      ) : rows.length === 0 ? (
        <p className="px-3 py-2 text-xs text-muted">
          Nothing stored. A task keeps what it writes to{" "}
          <code className="font-mono">/workspace/.podium/artifacts/</code>.
        </p>
      ) : (
        <div className="divide-y divide-border">
          {files.map((a) => (
            <ArtifactRow key={a.id} artifact={a} />
          ))}
          {logs.length > 0 ? (
            <p className="px-3 py-1.5 text-xs text-muted">
              Archived logs — the same output as above, compressed into the object store once the
              task finished.
            </p>
          ) : null}
          {logs.map((a) => (
            <ArtifactRow key={a.id} artifact={a} />
          ))}
        </div>
      )}
    </section>
  );
}

function ArtifactRow({ artifact }: { artifact: Artifact }) {
  const toast = useToast();
  const [copied, setCopied] = useState(false);

  const get = useMutation({
    mutationFn: () => download(artifact),
    onError: (err) => toast(`download ${artifact.name}: ${errorMessage(err)}`),
  });

  // A presigned URL is for handing to something that is not this browser; it expires in 15
  // minutes and only works from a network that can reach the object store, which is why it is
  // a copy button rather than the download button.
  const copyLink = useMutation({
    mutationFn: () => artifactClient.getArtifactURL({ artifactId: artifact.id }),
    onSuccess: (res) => {
      void navigator.clipboard?.writeText(res.url);
      setCopied(true);
      toast(`Presigned link copied. It expires ${absolute(res.expiresAt)}.`, "ok");
    },
    onError: (err) => toast(`GetArtifactURL: ${errorMessage(err)}`),
  });

  return (
    <div
      data-testid="artifact-row"
      data-kind={artifact.kind || "file"}
      className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-1.5 text-xs"
    >
      <Badge tone={artifact.kind === "log" ? "idle" : "run"}>{artifact.kind || "file"}</Badge>
      <span className="font-mono break-all">{artifact.name}</span>
      <span className="text-muted">{humanBytes(artifact.sizeBytes)}</span>
      <span className="text-muted">{artifact.contentType || "application/octet-stream"}</span>
      <span className="text-muted" title={absolute(artifact.createdAt)}>
        {relative(artifact.createdAt)}
      </span>
      <span className="ml-auto flex gap-2">
        <button
          type="button"
          disabled={get.isPending}
          onClick={() => get.mutate()}
          className="rounded border border-border px-2 py-0.5 hover:border-accent disabled:opacity-40"
        >
          {get.isPending ? "Downloading…" : "Download"}
        </button>
        <button
          type="button"
          disabled={copyLink.isPending}
          onClick={() => copyLink.mutate()}
          title="A presigned link straight to the object store, valid for 15 minutes"
          className="rounded border border-border px-2 py-0.5 hover:border-accent disabled:opacity-40"
        >
          {copied ? "Copied" : "Copy link"}
        </button>
      </span>
    </div>
  );
}
