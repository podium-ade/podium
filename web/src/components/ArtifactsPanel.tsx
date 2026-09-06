import { useEffect, useState } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { Download, FileArchive, FileText, Link2, Loader } from "lucide-react";
import { Badge } from "./Badge";
import { Skeleton } from "./Skeleton";
import { useToast } from "./Toast";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "./ui/card";
import { Tooltip } from "./ui/tooltip";
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
      <Card>
        <CardHeader>
          <div>
            <CardTitle>Artifacts</CardTitle>
          </div>
        </CardHeader>
        <div className="px-5 pb-5">
          <Alert title="No object store configured">
            This server has no <code className="font-mono">PODIUM_S3_ENDPOINT</code>, so nothing a
            task produces is kept.
          </Alert>
        </div>
      </Card>
    );
  }

  const rows = query.data?.artifacts ?? [];
  const files = rows.filter((a) => a.kind !== "log");
  const logs = rows.filter((a) => a.kind === "log");

  return (
    <Card className="overflow-hidden">
      <CardHeader>
        <div>
          <CardTitle className="flex items-center gap-2">
            Artifacts
            {rows.length > 0 ? (
              <span className="tabular text-2xs font-normal text-faint">{rows.length}</span>
            ) : null}
          </CardTitle>
          <CardDescription>
            What the task left behind, downloaded through the control plane.
          </CardDescription>
        </div>
      </CardHeader>

      {query.isPending ? (
        <div aria-busy="true" aria-label="Loading artifacts">
          {[0, 1].map((i) => (
            <div key={i} className="flex items-center gap-3 border-t border-hairline px-5 py-3">
              <Skeleton className="size-7 shrink-0" />
              <Skeleton className="h-3.5 w-56" />
              <Skeleton className="ml-auto h-3 w-24" />
            </div>
          ))}
        </div>
      ) : rows.length === 0 ? (
        <div className="border-t border-hairline px-5 py-9 text-center">
          <p className="text-xs text-muted">Nothing stored.</p>
          <p className="mx-auto mt-1 max-w-sm text-2xs leading-relaxed text-faint">
            A task keeps whatever it writes to{" "}
            <code className="font-mono text-muted">/workspace/.podium/artifacts/</code>, plus its
            own output once the roll-up sweep has archived it.
          </p>
        </div>
      ) : (
        <div>
          {files.map((a) => (
            <ArtifactRow key={a.id} artifact={a} />
          ))}
          {logs.length > 0 ? (
            <p className="border-t border-hairline bg-panel/60 px-5 py-2 text-2xs text-faint">
              Archived logs — the same output as above, compressed into the object store once the
              task finished.
            </p>
          ) : null}
          {logs.map((a) => (
            <ArtifactRow key={a.id} artifact={a} />
          ))}
        </div>
      )}
    </Card>
  );
}

function ArtifactRow({ artifact }: { artifact: Artifact }) {
  const toast = useToast();
  const [copied, setCopied] = useState(false);
  const kind = artifact.kind || "file";
  const Icon = kind === "log" ? FileArchive : FileText;

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
      data-kind={kind}
      className="flex items-center gap-3 border-t border-hairline px-5 py-2.5 transition-colors hover:bg-raised/30"
    >
      <span className="grid size-7 shrink-0 place-items-center rounded-md border border-border bg-raised/50 text-faint">
        <Icon className="size-3.5" />
      </span>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="truncate font-mono text-xs text-fg" title={artifact.name}>
            {artifact.name}
          </span>
          <Badge tone={kind === "log" ? "idle" : "run"} dot={false}>
            {kind}
          </Badge>
        </div>
        <div className="mt-0.5 flex flex-wrap items-center gap-x-2 text-2xs text-faint">
          <span className="tabular">{humanBytes(artifact.sizeBytes)}</span>
          <span aria-hidden>·</span>
          <span className="truncate">{artifact.contentType || "application/octet-stream"}</span>
          <span aria-hidden>·</span>
          <span title={absolute(artifact.createdAt)}>{relative(artifact.createdAt)}</span>
        </div>
      </div>
      <div className="flex shrink-0 items-center gap-1">
        <Tooltip label="Download through the control plane">
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label={get.isPending ? "Downloading…" : "Download"}
            disabled={get.isPending}
            onClick={() => get.mutate()}
          >
            {get.isPending ? <Loader className="animate-spin" /> : <Download />}
          </Button>
        </Tooltip>
        <Tooltip
          label={
            copied
              ? "Copied"
              : "Copy a presigned link straight to the object store, valid for 15 minutes"
          }
        >
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Copy link"
            disabled={copyLink.isPending}
            onClick={() => copyLink.mutate()}
          >
            <Link2 className={copied ? "text-ok" : undefined} />
          </Button>
        </Tooltip>
      </div>
    </div>
  );
}
