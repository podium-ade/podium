import { useMutation, useQueryClient } from "@tanstack/react-query";
import { ExternalLink, Globe, Power } from "lucide-react";
import { useToast } from "../Toast";
import { Badge } from "../Badge";
import { Button } from "../ui/button";
import { Card, CardDescription, CardHeader, CardTitle } from "../ui/card";
import type { Task } from "../../gen/podium/v1/task_pb";
import { errorMessage, tasks } from "../../lib/client";
import { absolute, toDate } from "../../lib/format";

/**
 * PreviewCard is where a task that exposes ports can be reached once its command has exited,
 * and the button that ends it early. The ttl counts from the exit, so a preview published by a
 * command that is still running has no expiry yet.
 */
export function PreviewCard({ task }: { task: Task }) {
  const qc = useQueryClient();
  const toast = useToast();
  const release = useMutation({
    mutationFn: () => tasks.releasePreview({ taskId: task.id }),
    onSuccess: () => {
      toast("Preview released. The node is tearing it down.", "ok");
      void qc.invalidateQueries({ queryKey: ["task", task.id] });
    },
    onError: (err) => toast(`ReleasePreview: ${errorMessage(err)}`),
  });

  const p = task.preview;
  if (!p) return null;
  const released = p.releasedAt !== undefined;
  const expires = toDate(p.expiresAt);
  const names = Object.keys(p.urls).sort();

  return (
    <Card data-testid="task-preview">
      <CardHeader className="flex-row items-start justify-between gap-3 pb-3">
        <div>
          <CardTitle className="flex items-center gap-2">
            <Globe className="size-4 text-faint" />
            Preview
            {released ? (
              <Badge tone="idle">released</Badge>
            ) : expires ? (
              <Badge tone="ok">up</Badge>
            ) : (
              <Badge tone="run">published</Badge>
            )}
          </CardTitle>
          <CardDescription>
            {released
              ? `Released ${absolute(p.releasedAt)}${p.releaseReason ? ` (${p.releaseReason})` : ""}.`
              : expires
                ? `Over ${p.via} on ${p.address}, until ${expires.toLocaleString()}.`
                : `Over ${p.via} on ${p.address}. It stays up once the command exits.`}
          </CardDescription>
        </div>
        {!released && expires ? (
          <Button
            variant="outline"
            size="sm"
            disabled={release.isPending}
            onClick={() => release.mutate()}
          >
            <Power />
            {release.isPending ? "Releasing…" : "Release"}
          </Button>
        ) : null}
      </CardHeader>
      {released ? null : (
        <ul className="space-y-1.5 border-t border-hairline px-5 py-3">
          {names.map((name) => (
            <li key={name} className="grid grid-cols-[8rem_minmax(0,1fr)] gap-x-3 text-xs">
              <span className="text-faint">{name}</span>
              <a
                href={p.urls[name]}
                target="_blank"
                rel="noreferrer"
                className="inline-flex min-w-0 items-center gap-1 font-mono text-accent hover:underline"
              >
                <span className="truncate">{p.urls[name]}</span>
                <ExternalLink className="size-3 shrink-0" />
              </a>
            </li>
          ))}
        </ul>
      )}
    </Card>
  );
}
