import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Container, Plus, RotateCw, Trash2 } from "lucide-react";

import { Empty } from "../components/Empty";
import { PageFrame } from "../components/PageHeader";
import { DeleteRegistryDialog } from "../components/registries/DeleteRegistryDialog";
import { RegistryDialog } from "../components/registries/RegistryDialog";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
import { Tooltip } from "../components/ui/tooltip";
import type { Registry } from "../gen/podium/v1/registry_pb";
import { Code, connectCode, errorMessage, registries } from "../lib/client";
import { absolute, relative } from "../lib/format";
import { useViewer } from "../lib/identity";
import { canManageInfra } from "../lib/rbac";

/**
 * The Registries screen: one login per registry host, so tasks and playbooks can name an
 * image on a private registry the same way they name a public one. Like Secrets it shows
 * metadata only; there is no read endpoint for a password and none is wanted.
 */
export function RegistriesPage() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const toast = useToast();
  const [setting, setSetting] = useState<{ lockedHost?: string }>();
  const [deleting, setDeleting] = useState<Registry>();

  const query = useQuery({
    queryKey: ["registries"],
    queryFn: () => registries.listRegistries({}),
    placeholderData: (prev) => prev,
  });

  useEffect(() => {
    if (query.error) toast(`ListRegistries: ${errorMessage(query.error)}`);
  }, [query.error, toast]);

  const rows = useMemo(() => query.data?.registries ?? [], [query.data]);
  const sorted = useMemo(() => [...rows].sort((a, b) => a.host.localeCompare(b.host)), [rows]);

  const noMasterKey = connectCode(query.error) === Code.FailedPrecondition;
  const listFailed = query.error !== null && !noMasterKey;

  return (
    <PageFrame
        title="Registries"
        actions={
          manage ? (
            <Button size="sm" onClick={() => setSetting({})} disabled={noMasterKey}>
              <Plus />
              New registry
            </Button>
          ) : null
        }
      >

      <Alert variant="info" title="A login is matched by host">
        An image reference names its registry (for example{" "}
        <span className="font-mono">us-docker.pkg.dev/…</span>,{" "}
        <span className="font-mono">ghcr.io/…</span>), and a node pulling it sends the login stored
        for that host, on whatever node the task lands. Registries not listed here are pulled
        anonymously. Passwords are write-only: they leave the server only inside an assignment.
      </Alert>

      {noMasterKey ? (
        <Alert variant="warn" title="This server has no master key configured">
          There is nothing to encrypt a password with, so it cannot store registry logins. Set{" "}
          <span className="font-mono">PODIUM_MASTER_KEY_FILE</span> and restart podium-server.
        </Alert>
      ) : null}

      {listFailed ? (
        <Alert variant="destructive" title="Could not list registries">
          <p>{errorMessage(query.error)}</p>
          <Button
            variant="outline"
            size="xs"
            className="mt-2"
            onClick={() => void query.refetch()}
            disabled={query.isFetching}
          >
            {query.isFetching ? "Retrying…" : "Retry"}
          </Button>
        </Alert>
      ) : null}

      {query.isPending ? (
        <TableSkeleton cols={5} />
      ) : rows.length === 0 ? (
        query.error ? null : (
          <Empty
            icon={Container}
            title="No registries yet"
            hint="Every pull is anonymous until a registry is added here. A private image fails on the node with pull access denied."
            action={
              manage ? (
                <Button size="sm" onClick={() => setSetting({})} disabled={noMasterKey}>
                  <Plus />
                  New registry
                </Button>
              ) : undefined
            }
          />
        )
      ) : (
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Host</TableHead>
              <TableHead>Username</TableHead>
              <TableHead>Last set</TableHead>
              <TableHead>Set by</TableHead>
              <TableHead />
            </TableRow>
          </TableHeader>
          <TableBody>
            {sorted.map((r) => (
              <TableRow key={r.host}>
                <TableCell>
                  <span className="font-mono text-xs text-fg" title={r.host}>
                    {r.host}
                  </span>
                </TableCell>
                <TableCell className="font-mono text-xs text-muted">{r.username}</TableCell>
                <TableCell className="text-xs whitespace-nowrap text-muted" title={absolute(r.updatedAt)}>
                  {relative(r.updatedAt)}
                </TableCell>
                <TableCell className="text-xs text-muted">{r.createdBy || "-"}</TableCell>
                <TableCell>
                  {manage ? (
                  <div className="flex items-center justify-end gap-1">
                    <Button
                      variant="outline"
                      size="xs"
                      aria-label={`Replace login for ${r.host}`}
                      onClick={() => setSetting({ lockedHost: r.host })}
                    >
                      <RotateCw />
                      Replace
                    </Button>
                    <Tooltip label={`Remove ${r.host}`}>
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-label={`Remove ${r.host}`}
                        onClick={() => setDeleting(r)}
                        className="hover:bg-err/10 hover:text-err"
                      >
                        <Trash2 />
                      </Button>
                    </Tooltip>
                  </div>
                  ) : null}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      )}

      {setting ? (
        <RegistryDialog
          open
          onOpenChange={(open) => !open && setSetting(undefined)}
          existing={rows}
          lockedHost={setting.lockedHost}
        />
      ) : null}
      {deleting ? (
        <DeleteRegistryDialog
          registry={deleting}
          open
          onOpenChange={(open) => !open && setDeleting(undefined)}
        />
      ) : null}
    </PageFrame>
  );
}
