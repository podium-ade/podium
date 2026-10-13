import { useEffect, useMemo, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Check, Copy, Plus, TriangleAlert } from "lucide-react";
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

/** Design table head: 12px, weight 750, muted, uppercase, 0.08em tracking, 12×16 padding. */
const headCell =
  "px-4 py-3 text-xs font-[750] leading-[normal] tracking-[0.08em] text-muted uppercase";

const YOURS_COPY =
  "Secrets only you can see and change. Set secrets here for tasks that should run with your own credentials.";
const GLOBAL_COPY =
  "Admin-defined secrets that are accessible by the system. Set secrets here to configure what tasks need to run.";

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
  const showList = !query.isPending && !(rows.length === 0 && query.error);

  const openNew = () => setSetting({ scope: createScope });

  return (
    <PageFrame title="Secrets" bodyClassName="space-y-5 p-6">
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

      <section
        className="min-w-0 overflow-hidden rounded-lg border border-border bg-panel"
        aria-labelledby="secrets-heading"
      >
        <div className="border-b border-border p-6">
          <h2 id="secrets-heading" className="text-2xl leading-tight font-bold">
            Secrets
          </h2>
          <p className="mt-2 max-w-[66ch] text-soft">
            Values are write-only. A value cannot be viewed after it is saved. There is no read
            API, and it leaves the server only inside an assignment.
          </p>
        </div>

        {query.isPending ? (
          <TableSkeleton cols={6} />
        ) : showYours ? (
          <Tabs value={tab} onValueChange={(value) => setTab(value as "global" | "yours")}>
            <TabsList className="px-4" aria-label="Secret scope">
              <TabsTrigger
                value="yours"
                className="gap-2"
                aria-label={`Yours, ${yours.length} ${yours.length === 1 ? "secret" : "secrets"}`}
              >
                Yours
                <TabCount count={yours.length} />
              </TabsTrigger>
              <TabsTrigger
                value="global"
                className="gap-2"
                aria-label={`Global, ${globalRows.length} ${globalRows.length === 1 ? "secret" : "secrets"}`}
              >
                Global
                <TabCount count={globalRows.length} />
              </TabsTrigger>
            </TabsList>
            <ScopeBar
              personal={creatingPersonal}
              canCreate={canCreate}
              disabled={noMasterKey}
              onNew={openNew}
            />
            <TabsContent value="yours" data-testid="secrets-yours" className="pt-0">
              <SecretList
                list={yours}
                emptyTitle="No secrets of yours yet"
                caption="Your secrets"
                showList={showList}
                personal={personal}
                manage={manage}
                mixedKeys={mixedKeys}
                commonKey={commonKey}
                onRotate={(secret) =>
                  setSetting({ lockedName: secret.name, scope: SecretScope.PERSONAL })
                }
                onDelete={setDeleting}
              />
            </TabsContent>
            <TabsContent value="global" data-testid="secrets-global" className="pt-0">
              <SecretList
                list={globalRows}
                emptyTitle="No global secrets"
                caption="Global secrets"
                showList={showList}
                personal={personal}
                manage={manage}
                mixedKeys={mixedKeys}
                commonKey={commonKey}
                onRotate={(secret) =>
                  setSetting({ lockedName: secret.name, scope: SecretScope.GLOBAL })
                }
                onDelete={setDeleting}
              />
            </TabsContent>
          </Tabs>
        ) : (
          <>
            <ScopeBar
              personal={false}
              canCreate={canCreate}
              disabled={noMasterKey}
              onNew={openNew}
            />
            <SecretList
              list={globalRows}
              emptyTitle="No secrets yet"
              caption="Global secrets"
              showList={showList}
              personal={personal}
              manage={manage}
              mixedKeys={mixedKeys}
              commonKey={commonKey}
              onRotate={(secret) =>
                setSetting({ lockedName: secret.name, scope: SecretScope.GLOBAL })
              }
              onDelete={setDeleting}
            />
          </>
        )}
      </section>

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

function ScopeBar({
  personal,
  canCreate,
  disabled,
  onNew,
}: {
  personal: boolean;
  canCreate: boolean;
  disabled: boolean;
  onNew: () => void;
}) {
  return (
    <div className="grid grid-cols-1 items-center gap-4 border-b border-border bg-raised px-6 py-4 sm:grid-cols-[minmax(0,1fr)_auto]">
      <p className="m-0 text-soft">{personal ? YOURS_COPY : GLOBAL_COPY}</p>
      {canCreate ? (
        <Button
          type="button"
          className="shrink-0 justify-self-start sm:justify-self-end"
          onClick={onNew}
          disabled={disabled}
        >
          <Plus />
          New secret
        </Button>
      ) : null}
    </div>
  );
}

function TabCount({ count }: { count: number }) {
  return (
    <span
      aria-hidden
      className="inline-grid h-5 min-w-[22px] place-items-center rounded-sm border border-line-strong bg-panel px-1.5 text-xs font-bold text-fg tabular-nums"
    >
      {count}
    </span>
  );
}

function SecretList({
  list,
  emptyTitle,
  caption,
  showList,
  personal,
  manage,
  mixedKeys,
  commonKey,
  onRotate,
  onDelete,
}: {
  list: Secret[];
  emptyTitle: string;
  caption: string;
  showList: boolean;
  personal: boolean;
  manage: boolean;
  mixedKeys: boolean;
  commonKey: string;
  onRotate: (secret: Secret) => void;
  onDelete: (secret: Secret) => void;
}) {
  return (
    <div aria-live="polite">
      {showList && list.length === 0 ? <SecretsEmpty title={emptyTitle} /> : null}
      {showList && list.length > 0 ? (
        <Table
          containerClassName="rounded-none border-0 bg-transparent max-[600px]:overflow-visible"
          className="min-w-[640px] max-[600px]:block max-[600px]:min-w-0"
        >
          <caption className="sr-only">{caption}</caption>
          <TableHeader className="max-[600px]:hidden">
            <TableRow className="hover:bg-transparent">
              <TableHead className={headCell}>Name</TableHead>
              <TableHead className={headCell}>Version</TableHead>
              <TableHead className={headCell}>Last set</TableHead>
              <TableHead className={headCell}>Set by</TableHead>
              <TableHead className={headCell}>Key</TableHead>
              <TableHead className={headCell}>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody className="max-[600px]:block [&_tr:last-child_td]:pb-2">
            {list.map((s) => {
              const cut = s.name.lastIndexOf(".");
              const stale = ageDays(s) >= STALE_DAYS;
              const writable = s.scope === SecretScope.PERSONAL ? personal : manage;
              return (
                <TableRow
                  key={`${s.scope ?? "global"}-${s.name}`}
                  className="max-[600px]:grid max-[600px]:grid-cols-2 max-[600px]:border-t max-[600px]:border-border max-[600px]:px-4 max-[600px]:py-3"
                >
                  <TableCell className="px-4 py-4 max-[600px]:col-span-2 max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
                    <MobileLabel>Name</MobileLabel>
                    <div className="flex min-w-0 items-center gap-1">
                      <span className="min-w-0 truncate font-mono text-xs" title={s.name}>
                        {cut > 0 ? <span className="text-muted">{s.name.slice(0, cut + 1)}</span> : null}
                        <span className="text-fg">{cut > 0 ? s.name.slice(cut + 1) : s.name}</span>
                      </span>
                      <CopyName name={s.name} />
                    </div>
                  </TableCell>
                  <TableCell className="px-4 py-4 text-sm whitespace-nowrap text-muted max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
                    <MobileLabel>Version</MobileLabel>
                    <span>v</span>
                    <span className="tabular text-fg">{s.version}</span>
                  </TableCell>
                  <TableCell
                    className="px-4 py-4 text-sm whitespace-nowrap max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2"
                    title={absolute(s.updatedAt)}
                  >
                    <MobileLabel>Last set</MobileLabel>
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
                  <TableCell className="px-4 py-4 text-sm text-muted max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
                    <MobileLabel>Set by</MobileLabel>
                    <span className="block truncate">{s.createdBy || "-"}</span>
                  </TableCell>
                  <TableCell className="px-4 py-4 text-sm max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
                    <MobileLabel>Key</MobileLabel>
                    {mixedKeys && s.keyId !== commonKey ? (
                      <Badge tone="warn" dot={false} className="font-mono">
                        {s.keyId || "-"}
                      </Badge>
                    ) : (
                      <Chip className="font-mono">{s.keyId || "-"}</Chip>
                    )}
                  </TableCell>
                  <TableCell className="px-4 py-4 max-[600px]:col-span-2 max-[600px]:block max-[600px]:border-0 max-[600px]:px-2 max-[600px]:py-2">
                    <MobileLabel>Actions</MobileLabel>
                    {writable ? (
                      <div className="flex items-center justify-end gap-2 max-[600px]:justify-start">
                        <Button
                          variant="outline"
                          aria-label={`Rotate ${s.name}`}
                          onClick={() => onRotate(s)}
                        >
                          Rotate
                        </Button>
                        <Button
                          variant="ghost"
                          aria-label={`Delete ${s.name}`}
                          onClick={() => onDelete(s)}
                          className="text-err hover:bg-err-bg hover:text-err"
                        >
                          Delete
                        </Button>
                      </div>
                    ) : null}
                  </TableCell>
                </TableRow>
              );
            })}
          </TableBody>
        </Table>
      ) : null}
    </div>
  );
}

function SecretsEmpty({ title }: { title: string }) {
  return (
    <div className="grid min-h-80 content-center justify-items-center gap-3 px-6 py-12 text-center">
      <span
        aria-hidden
        className="grid size-12 place-items-center rounded-md border border-line-strong bg-raised font-mono text-lg font-bold text-soft"
      >
        S
      </span>
      <h3 className="text-lg font-bold">{title}</h3>
      <p className="m-0 max-w-[58ch] text-soft">
        Use New secret to add one. A task that names a missing secret is refused at admission.
      </p>
    </div>
  );
}

function MobileLabel({ children }: { children: string }) {
  return (
    <span className="mb-1 hidden text-xs font-bold tracking-[0.08em] text-muted uppercase max-[600px]:block">
      {children}
    </span>
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
