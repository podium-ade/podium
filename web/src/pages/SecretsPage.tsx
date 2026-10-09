import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Plus, RotateCw, Trash2, TriangleAlert } from "lucide-react";
import { Badge, Chip } from "../components/Badge";
import { PageFrame } from "../components/PageHeader";
import { DeleteSecretDialog } from "../components/secrets/DeleteSecretDialog";
import { SecretDialog } from "../components/secrets/SecretDialog";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Button } from "../components/ui/button";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
import { Tooltip } from "../components/ui/tooltip";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import { SecretScope, type Secret } from "../gen/podium/v1/secret_pb";
import { Code, connectCode, errorMessage, secrets } from "../lib/client";
import { absolute, relative, toDate } from "../lib/format";
import { useViewer } from "../lib/identity";
import { canManageInfra } from "../lib/rbac";

/** Past this, a secret is old enough that nobody remembers who has a copy of it. */
const STALE_DAYS = 90;

/**
 * The Secrets screen shows metadata and nothing else.
 *
 * No person can read a value back, and there must never be a way to: a value goes in once and
 * comes back out inside an `Assign`, on its way to the node about to run the task that asked
 * for it, or to the conductor alone for its own credentials. `ListSecrets` carries no value and no ciphertext, so the screen
 * cannot leak one even by accident — and there is deliberately no "reveal" control to add one
 * to later. The reveal toggle in the set dialog unmasks that field's own draft; there is
 * nothing stored for it to reach.
 */
export function SecretsPage() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const personal = viewer?.kind === IdentityKind.USER;
  const toast = useToast();
  const [setting, setSetting] = useState<{ lockedName?: string; scope: SecretScope }>();
  const [deleting, setDeleting] = useState<Secret>();
  const [tab, setTab] = useState<"global" | "yours">("global");

  const query = useQuery({
    queryKey: ["secrets"],
    queryFn: () => secrets.listSecrets({}),
    placeholderData: (prev) => prev,
  });

  useEffect(() => {
    if (query.error) toast(`ListSecrets: ${errorMessage(query.error)}`);
  }, [query.error, toast]);

  const rows = useMemo(() => query.data?.secrets ?? [], [query.data]);

  // Sorting by name is what groups the list: dotted names sort into their namespaces on their
  // own, and dimming everything up to the last dot makes those runs visible without spending a
  // header row on each one — most namespaces here hold a single secret.
  const sorted = useMemo(() => [...rows].sort((a, b) => a.name.localeCompare(b.name)), [rows]);
  const globalRows = sorted.filter((s) => s.scope !== SecretScope.PERSONAL);
  const yours = sorted.filter((s) => s.scope === SecretScope.PERSONAL);
  const showYours = personal || yours.length > 0;
  const creatingPersonal = showYours && tab === "yours";
  const canCreate = creatingPersonal ? personal : manage;
  const createScope = creatingPersonal ? SecretScope.PERSONAL : SecretScope.GLOBAL;

  // A key_id that differs from the rest means a master-key rotation stopped half way: those
  // rows are still readable with the old key and nothing else is.
  const mixedKeys = useMemo(() => new Set(rows.map((s) => s.keyId)).size > 1, [rows]);
  const commonKey = useMemo(() => {
    const counts = new Map<string, number>();
    for (const s of rows) counts.set(s.keyId, (counts.get(s.keyId) ?? 0) + 1);
    return [...counts.entries()].sort((a, b) => b[1] - a[1])[0]?.[0] ?? "";
  }, [rows]);

  const noMasterKey = connectCode(query.error) === Code.FailedPrecondition;
  const listFailed = query.error !== null && !noMasterKey;

  const tableFor = (
    list: Secret[],
    empty: { title: string; info: string; scope: SecretScope; canCreate: boolean },
  ) => (
    <Table className="table-fixed">
      <colgroup>
        <col className="w-[32%]" />
        <col className="w-[8%]" />
        <col className="w-[16%]" />
        <col className="w-[16%]" />
        <col className="w-[12%]" />
        <col className="w-[16%]" />
      </colgroup>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>Name</TableHead>
          <TableHead>Version</TableHead>
          <TableHead>Last set</TableHead>
          <TableHead>Set by</TableHead>
          <TableHead>Key</TableHead>
          <TableHead />
        </TableRow>
      </TableHeader>
      <TableBody>
        {list.length === 0 ? (
          <TableRow className="hover:bg-transparent">
            <TableCell colSpan={6} className="px-6 py-16">
              <div className="mx-auto flex max-w-md flex-col items-center gap-3 text-center">
                <p className="text-sm font-medium text-fg">{empty.title}</p>
                {empty.info ? <p className="text-xs leading-relaxed text-muted">{empty.info}</p> : null}
                {empty.canCreate ? (
                  <Button
                    size="sm"
                    onClick={() => setSetting({ scope: empty.scope })}
                    disabled={noMasterKey}
                  >
                    <Plus />
                    New secret
                  </Button>
                ) : null}
              </div>
            </TableCell>
          </TableRow>
        ) : null}
        {list.map((s) => {
          const cut = s.name.lastIndexOf(".");
          const stale = ageDays(s) >= STALE_DAYS;
          const writable =
            s.scope === SecretScope.PERSONAL ? personal : manage;
          return (
            <TableRow key={s.name}>
              <TableCell className="overflow-hidden">
                <div className="flex min-w-0 items-center gap-1">
                  <span className="min-w-0 truncate font-mono text-xs" title={s.name}>
                    {cut > 0 ? (
                      <span className="text-faint">{s.name.slice(0, cut + 1)}</span>
                    ) : null}
                    <span className="text-fg">{cut > 0 ? s.name.slice(cut + 1) : s.name}</span>
                  </span>
                  <CopyName name={s.name} />
                </div>
              </TableCell>
              <TableCell className="text-xs whitespace-nowrap">
                <span className="text-faint">v</span>
                <span className="tabular text-fg">{s.version}</span>
              </TableCell>
              <TableCell className="text-xs whitespace-nowrap" title={absolute(s.updatedAt)}>
                {stale ? (
                  <Tooltip label="Not rotated in over 90 days. Anyone who has held a copy since then still holds a working one.">
                    <span className="inline-flex items-center gap-1 text-warn">
                      <TriangleAlert className="size-3.5" />
                      {relative(s.updatedAt)}
                    </span>
                  </Tooltip>
                ) : (
                  <span className="text-muted">{relative(s.updatedAt)}</span>
                )}
              </TableCell>
              <TableCell className="overflow-hidden text-xs text-muted">
                <span className="block truncate">{s.createdBy || "-"}</span>
              </TableCell>
              <TableCell className="overflow-hidden text-xs">
                {mixedKeys && s.keyId !== commonKey ? (
                  <Badge tone="warn" dot={false} className="font-mono">
                    {s.keyId || "-"}
                  </Badge>
                ) : (
                  <Chip className="font-mono">{s.keyId || "-"}</Chip>
                )}
              </TableCell>
              <TableCell className="overflow-hidden">
                {writable ? (
                  <div className="flex items-center justify-end gap-1">
                    <Button
                      variant="outline"
                      size="xs"
                      aria-label={`Rotate ${s.name}`}
                      onClick={() =>
                        setSetting({
                          lockedName: s.name,
                          scope:
                            s.scope === SecretScope.PERSONAL
                              ? SecretScope.PERSONAL
                              : SecretScope.GLOBAL,
                        })
                      }
                    >
                      <RotateCw />
                      Rotate
                    </Button>
                    <Tooltip label={`Delete ${s.name}`}>
                      <Button
                        variant="ghost"
                        size="icon-xs"
                        aria-label={`Delete ${s.name}`}
                        onClick={() => setDeleting(s)}
                        className="hover:bg-err/10 hover:text-err"
                      >
                        <Trash2 />
                      </Button>
                    </Tooltip>
                  </div>
                ) : null}
              </TableCell>
            </TableRow>
          );
        })}
      </TableBody>
    </Table>
  );

  return (
    <PageFrame
        title="Secrets"
        actions={
          canCreate ? (
            // Without a master key SetSecret refuses too, so the action is offered but inert
            // rather than failing after the operator has typed a value into it.
            <Button size="sm" onClick={() => setSetting({ scope: createScope })} disabled={noMasterKey}>
              <Plus />
              New secret
            </Button>
          ) : null
        }
      >

      <Alert variant="info" title="Values are write-only">
        A value cannot be viewed after it is saved: there is no read API, and it leaves the
        server only inside an assignment, on its way to the node about to run a task that named
        it. Setting a name that already exists rotates it: new version, old value gone.
      </Alert>

      {noMasterKey ? (
        <Alert variant="warn" title="This server has no master key configured">
          There is nothing to encrypt a value with, so it cannot store secrets at all. Set{" "}
          <span className="font-mono">PODIUM_MASTER_KEY_FILE</span> and restart podium-server.
        </Alert>
      ) : null}

      {listFailed ? (
        <Alert variant="destructive" title="Could not list secrets">
          <p>{errorMessage(query.error)}</p>
          <p className="mt-1 opacity-80">
            The metadata list is a plain read. If it is failing, check that podium-server is up
            and that your token is still good.
          </p>
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

      {mixedKeys ? (
        <Alert variant="warn" title="A master-key rotation stopped part way">
          These secrets are not all encrypted under the same master key, so the old key is still
          required to read some of them. Finish it with{" "}
          <span className="font-mono">podium-server rotate-master-key</span>.
        </Alert>
      ) : null}

      {/* An empty list and a list that could not be read are different answers, and only one of
          them is "there is nothing here" — the alert above is the whole answer to the other. */}
      {query.isPending ? (
        <TableSkeleton cols={6} />
      ) : rows.length === 0 && query.error ? null : showYours ? (
        <Tabs value={tab} onValueChange={(value) => setTab(value as "global" | "yours")}>
          <TabsList>
            <TabsTrigger value="global">Global</TabsTrigger>
            <TabsTrigger value="yours">Yours</TabsTrigger>
          </TabsList>
          <TabsContent value="global" data-testid="secrets-global" className="space-y-2">
            <p className="max-w-2xl text-xs leading-relaxed text-muted">
              Admin-defined secrets that are accessible by the system. Set secrets here to
              configure what tasks need to run.
            </p>
            {tableFor(globalRows, {
              title: "No global secrets",
              info: "",
              scope: SecretScope.GLOBAL,
              canCreate: manage,
            })}
          </TabsContent>
          <TabsContent value="yours" data-testid="secrets-yours" className="space-y-2">
            <p className="max-w-2xl text-xs leading-relaxed text-muted">
              Secrets only you can see and change. Set secrets here for tasks that should run
              with your own credentials.
            </p>
            {tableFor(yours, {
              title: "No secrets of yours yet",
              info: "",
              scope: SecretScope.PERSONAL,
              canCreate: personal,
            })}
          </TabsContent>
        </Tabs>
      ) : (
        <div className="space-y-2">
          <p className="max-w-2xl text-xs leading-relaxed text-muted">
            Admin-defined secrets that are accessible by the system. Set secrets here to
            configure what tasks need to run.
          </p>
          {tableFor(globalRows, {
            title: "No secrets yet",
            info: "",
            scope: SecretScope.GLOBAL,
            canCreate: manage,
          })}
        </div>
      )}

      {/* Both dialogs mount only while they are open, so a typed value has nowhere to sit once
          the operator has walked away from one. */}
      {setting ? (
        <SecretDialog
          open
          onOpenChange={(open) => !open && setSetting(undefined)}
          existing={rows}
          lockedName={setting.lockedName}
          scope={setting.scope}
        />
      ) : null}
      {deleting ? (
        <DeleteSecretDialog
          secret={deleting}
          open
          onOpenChange={(open) => !open && setDeleting(undefined)}
        />
      ) : null}
    </PageFrame>
  );
}

function ageDays(s: Secret, now = Date.now()): number {
  const d = toDate(s.updatedAt);
  return d ? (now - d.getTime()) / 86_400_000 : 0;
}

/** The name is the contract a task spec has to match exactly, so it is worth copying, not retyping. */
function CopyName({ name }: { name: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Tooltip label={copied ? "Copied" : "Copy name"}>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label={`Copy ${name}`}
        className="opacity-0 transition-opacity group-hover/row:opacity-100 focus-visible:opacity-100"
        onClick={() => {
          void navigator.clipboard?.writeText(name);
          setCopied(true);
          setTimeout(() => setCopied(false), 1200);
        }}
      >
        {copied ? <Check className="text-ok" /> : <Copy />}
      </Button>
    </Tooltip>
  );
}
