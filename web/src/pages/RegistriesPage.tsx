import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Plus } from "lucide-react";

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
  const showTable = !query.isPending && query.error === null;

  return (
    <PageFrame title="Registries" bodyClassName="space-y-5 p-6">
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

      <section
        className="min-w-0 overflow-hidden rounded-lg border border-border bg-panel"
        aria-labelledby="registries-heading"
      >
        <div className="border-b border-border p-6">
          <h2 id="registries-heading" className="text-2xl leading-tight font-bold">
            Registries
          </h2>
          <p className="mt-2 max-w-[66ch] text-soft">
            A login is matched by host. An image reference such as{" "}
            <code className="font-mono text-xs">us-docker.pkg.dev/…</code> or{" "}
            <code className="font-mono text-xs">ghcr.io/…</code> uses the login stored for that
            host, on whichever node the task lands. A host with no login is pulled anonymously.
            Passwords are write-only.
          </p>
        </div>

        <div className="grid grid-cols-1 items-center gap-4 border-b border-border bg-raised px-6 py-4 sm:grid-cols-[minmax(0,1fr)_auto]">
          <p className="m-0 text-soft">
            One login per host. A password is written once and cannot be read back.
          </p>
          {manage ? (
            <Button
              type="button"
              className="shrink-0 justify-self-start sm:justify-self-end"
              onClick={() => setSetting({})}
              disabled={noMasterKey}
            >
              <Plus />
              New registry
            </Button>
          ) : null}
        </div>

        <div aria-live="polite">
          {query.isPending ? <TableSkeleton cols={5} /> : null}
          {showTable ? (
            <Table
              containerClassName="rounded-none border-0 bg-transparent max-[600px]:overflow-visible"
              className="min-w-[640px] max-[600px]:block max-[600px]:min-w-0"
            >
              <caption className="sr-only">Registries</caption>
              <TableHeader className="max-[600px]:hidden">
                <TableRow className="hover:bg-transparent">
                  <TableHead className="px-4 py-3">Host</TableHead>
                  <TableHead className="px-4 py-3">Username</TableHead>
                  <TableHead className="px-4 py-3">Last set</TableHead>
                  <TableHead className="px-4 py-3">Set by</TableHead>
                  <TableHead className="px-4 py-3">
                    <span className="sr-only">Actions</span>
                  </TableHead>
                </TableRow>
              </TableHeader>
              <TableBody className="max-[600px]:block [&_tr:last-child_td]:pb-2">
                {sorted.length === 0 ? (
                  <TableRow className="hover:bg-transparent max-[600px]:block">
                    <TableCell colSpan={5} className="border-0 px-6 py-12 text-center">
                      <h3 className="text-lg font-bold">No registries yet</h3>
                      <p className="mx-auto mt-2 max-w-[48ch] text-muted">
                        Every pull is anonymous until a registry is added here. A private image
                        fails on the node with pull access denied.
                      </p>
                    </TableCell>
                  </TableRow>
                ) : (
                  sorted.map((r) => (
                    <TableRow
                      key={r.host}
                      className="max-[600px]:grid max-[600px]:grid-cols-2 max-[600px]:border-t max-[600px]:border-border max-[600px]:px-4 max-[600px]:py-3"
                    >
                      <TableCell className="px-4 py-4 max-[600px]:col-span-2 max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
                        <MobileLabel>Host</MobileLabel>
                        <span className="font-mono text-xs text-fg" title={r.host}>
                          {r.host}
                        </span>
                      </TableCell>
                      <TableCell className="px-4 py-4 font-mono text-xs text-muted max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
                        <MobileLabel>Username</MobileLabel>
                        {r.username}
                      </TableCell>
                      <TableCell
                        className="px-4 py-4 text-sm whitespace-nowrap text-muted max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2"
                        title={absolute(r.updatedAt)}
                      >
                        <MobileLabel>Last set</MobileLabel>
                        {relative(r.updatedAt)}
                      </TableCell>
                      <TableCell className="px-4 py-4 text-sm text-muted max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
                        <MobileLabel>Set by</MobileLabel>
                        {r.createdBy || "-"}
                      </TableCell>
                      <TableCell className="px-4 py-4 max-[600px]:col-span-2 max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
                        <MobileLabel>Actions</MobileLabel>
                        {manage ? (
                          <div className="flex items-center justify-end gap-2 max-[600px]:justify-start">
                            <Button
                              variant="outline"
                              aria-label={`Replace login for ${r.host}`}
                              onClick={() => setSetting({ lockedHost: r.host })}
                            >
                              Replace
                            </Button>
                            <Button
                              variant="ghost"
                              aria-label={`Remove ${r.host}`}
                              onClick={() => setDeleting(r)}
                              className="text-err hover:bg-err-bg hover:text-err"
                            >
                              Remove
                            </Button>
                          </div>
                        ) : null}
                      </TableCell>
                    </TableRow>
                  ))
                )}
              </TableBody>
            </Table>
          ) : null}
        </div>
      </section>

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

function MobileLabel({ children }: { children: string }) {
  return (
    <span className="mb-1 hidden text-xs font-bold tracking-[0.08em] text-muted uppercase max-[600px]:block">
      {children}
    </span>
  );
}
