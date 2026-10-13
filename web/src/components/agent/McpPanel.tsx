import { useId, useState, type ReactNode } from "react";
import { ChevronRight, KeyRound, LogIn, Pencil, Plug, Plus, Trash2 } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { McpServer } from "../../gen/podium/agent/v1/agent_pb";
import { IdentityKind } from "../../gen/podium/v1/identity_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import {
  CUSTOM_MCP_DESCRIPTION,
  MCP_PRESETS,
  callbackURL,
  presetFor,
  rememberPending,
  tokenHelp,
  type McpPreset,
} from "../../lib/mcp";
import { absolute, relative } from "../../lib/format";
import { cn } from "../../lib/utils";
import { Empty } from "../Empty";
import { PageFrame } from "../PageHeader";
import { Field } from "../submit/Field";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Badge } from "../ui/badge";
import { Button } from "../ui/button";
import { Collapsible, CollapsibleContent, CollapsibleTrigger } from "../ui/collapsible";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Switch } from "../ui/switch";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../ui/tabs";
import { Textarea } from "../ui/textarea";
import { Tooltip } from "../ui/tooltip";
import { ConductorDown } from "./ConductorDown";
import { McpMark } from "./McpMark";
import { useViewer } from "../../lib/identity";
import { canManageInfra } from "../../lib/rbac";

/**
 * McpPanel is the MCP server registry: the tools this bot can reach that are not built into
 * the harness — Linear, Notion, an internal service somebody wrapped.
 *
 * Registering a server here is NOT granting it. A playbook's own list is what decides which
 * turns get which server, and the screen says so twice: in the header, and on every row that
 * no playbook names. The distinction is the whole security model — a token registered here
 * and named by a playbook a public Slack channel can reach is a token that channel can spend.
 */
/**
 * definitionOf is the half of a row a client owns: an update is a replace, and everything
 * else on the message — the token metadata, the provenance, the playbook list — belongs to
 * the conductor and is ignored there. Sending it back would be a claim this screen has no
 * business making.
 */
// parseEntry reads the Advanced JSON as the harness entry it stands for, or undefined while it
// is not (yet) a JSON object.
function parseEntry(raw: string): Record<string, unknown> | undefined {
  try {
    const v: unknown = JSON.parse(raw);
    return v !== null && typeof v === "object" && !Array.isArray(v) ? (v as Record<string, unknown>) : undefined;
  } catch {
    return undefined;
  }
}

function entryJSON(entry: Record<string, unknown>) {
  return JSON.stringify(entry, null, 2);
}

function serverKey(server: McpServer) {
  return `${server.owner}\n${server.name}`;
}

function definitionOf(server: McpServer, patch: { enabled?: boolean; fallback?: boolean } = {}) {
  return {
    name: server.name,
    url: server.url,
    description: server.description,
    enabled: patch.enabled ?? server.enabled,
    config: server.config,
    fallback: patch.fallback ?? server.fallback,
  };
}

export function McpPanel() {
  const viewer = useViewer();
  const manage = canManageInfra(viewer);
  const personal = viewer?.kind === IdentityKind.USER;
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<McpServer | "new" | "new-bot">();
  // A person opens on their own list. The local token has no personal registry,
  // so it opens on Global and the Yours tab stays read-only.
  const [tab, setTab] = useState<"yours" | "global">(personal ? "yours" : "global");
  const [selectedKey, setSelectedKey] = useState<string | null>(null);

  const list = useQuery({ queryKey: ["agent", "mcp"], queryFn: () => agent.listMcpServers({}) });
  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "mcp"] });

  const setEnabled = useMutation({
    // Enabling is an update of the whole registration, which is the only write there is: the
    // switch sends back what the row already said with one field flipped.
    mutationFn: (v: { server: McpServer; enabled: boolean; fallback?: boolean }) =>
      agent.updateMcpServer({
        server: definitionOf(v.server, { enabled: v.enabled, fallback: v.fallback }),
        forBot: !v.server.owner,
      }),
    onSuccess: async (_res, v) => {
      const what =
        v.fallback === undefined
          ? `${v.server.name} is ${v.enabled ? "enabled" : "disabled"}.`
          : `${v.server.name} ${v.fallback ? "is shared when someone has no server of this name" : "is no longer shared"}.`;
      toast(`${what} It applies to the next turn.`, "ok");
      await reload();
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const remove = useMutation({
    mutationFn: (v: { name: string; forBot: boolean }) =>
      agent.deleteMcpServer({ name: v.name, forBot: v.forBot }),
    onSuccess: async (_res, v) => {
      toast(`${v.name} removed, and its token with it.`, "ok");
      await reload();
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const servers = list.data?.servers ?? [];
  const yours = servers.filter((s) => Boolean(s.owner));
  const global = servers.filter((s) => !s.owner);
  // Admins, owners, and the local token can switch between the two lists. A member
  // never sees Global. Someone who is not a person cannot write a personal server.
  const showTabs = manage;
  const creatingGlobal = showTabs ? tab === "global" : !personal;
  const creating = editing === "new" || editing === "new-bot";
  const editingServer = typeof editing === "object" ? editing : undefined;
  const editingForBot = editing === "new-bot" || (editingServer !== undefined && !editingServer.owner);
  const loaded = !list.isPending && !(list.isError && isAgentUnreachable(list.error));
  const singleList = personal ? yours : global;

  const startCreate = (personalScope: boolean) => setEditing(personalScope ? "new" : "new-bot");

  const renderList = (testId: string, rows: McpServer[], readOnly: boolean, personalScope: boolean) => {
    const open = rows.find((s) => serverKey(s) === selectedKey);
    return (
      <div data-testid={testId}>
        {rows.length === 0 ? (
          <McpEmpty personal={personalScope} canAdd={!readOnly && (personalScope ? personal : manage)} />
        ) : (
          <ul className="m-0 list-none p-0">
            {rows.map((s) => (
              <ServerRow
                key={serverKey(s)}
                server={s}
                busy={
                  (setEnabled.isPending && setEnabled.variables?.server.name === s.name) ||
                  (remove.isPending && remove.variables?.name === s.name)
                }
                readOnly={readOnly}
                onOpen={() => setSelectedKey(serverKey(s))}
                onToggle={(enabled) => setEnabled.mutate({ server: s, enabled })}
                onFallback={(fallback) =>
                  setEnabled.mutate({ server: s, enabled: s.enabled, fallback })
                }
              />
            ))}
          </ul>
        )}
        {open ? (
          <ServerDetail
            server={open}
            busy={
              (setEnabled.isPending && setEnabled.variables?.server.name === open.name) ||
              (remove.isPending && remove.variables?.name === open.name)
            }
            readOnly={readOnly}
            onBack={() => setSelectedKey(null)}
            onToggle={(enabled) => setEnabled.mutate({ server: open, enabled })}
            onFallback={(fallback) =>
              setEnabled.mutate({ server: open, enabled: open.enabled, fallback })
            }
            onEdit={() => {
              // The edit form is its own dialog. Closing this one keeps a single modal open.
              setSelectedKey(null);
              setEditing(open);
            }}
            onDelete={() => remove.mutate({ name: open.name, forBot: !open.owner })}
            onTokenChanged={reload}
          />
        ) : null}
      </div>
    );
  };

  if (list.isError && !isAgentUnreachable(list.error)) {
    return (
      <Empty icon={Plug} title="Could not read the MCP servers" hint={errorMessage(list.error)} />
    );
  }

  const scopePersonal = showTabs ? tab === "yours" : personal;
  // Personal rows belong to a signed-in account. The company list belongs to an admin.
  const canWriteScope = scopePersonal ? personal : manage;

  return (
    <PageFrame title="MCP" bodyClassName="p-6">
      {list.isError && isAgentUnreachable(list.error) ? (
        <ConductorDown
          what="The MCP servers could not be read"
          onRetry={() => void list.refetch()}
          retrying={list.isFetching}
        />
      ) : null}

      {list.isPending ? <McpSkeleton /> : null}

      {loaded ? (
        <section
          className="min-w-0 overflow-hidden rounded-lg border border-border bg-panel"
          aria-label="MCP server management"
        >
          <div className="border-b border-border p-6">
            <h2 className="text-2xl leading-tight font-bold">MCP servers</h2>
            <p className="mt-2 max-w-[66ch] text-soft">
              Register an endpoint to make it available. A playbook must still name the server
              before an assistant can use it.
            </p>
          </div>

          {showTabs ? (
            <Tabs
              value={tab}
              onValueChange={(value) => {
                setTab(value as "yours" | "global");
                setSelectedKey(null);
              }}
            >
              <TabsList className="px-4" aria-label="MCP server ownership">
                <TabsTrigger
                  value="yours"
                  className="gap-2 py-2.5 text-base leading-[1.55]"
                  aria-label={`Yours, ${yours.length} ${yours.length === 1 ? "server" : "servers"}`}
                >
                  Yours
                  <TabCount count={yours.length} />
                </TabsTrigger>
                <TabsTrigger
                  value="global"
                  className="gap-2 py-2.5 text-base leading-[1.55]"
                  aria-label={`Global, ${global.length} ${global.length === 1 ? "server" : "servers"}`}
                >
                  Global
                  <TabCount count={global.length} />
                </TabsTrigger>
              </TabsList>
              <ScopeBar
                personal={scopePersonal}
                onConnect={canWriteScope ? () => startCreate(!creatingGlobal) : undefined}
                connectTestId={creatingGlobal ? "mcp-new" : "mcp-new-yours"}
              />
              <TabsContent value="yours" className="pt-0">
                {renderList("mcp-yours", yours, !personal, true)}
              </TabsContent>
              <TabsContent value="global" className="pt-0">
                {renderList("mcp-global", global, !manage, false)}
              </TabsContent>
            </Tabs>
          ) : (
            <>
              <ScopeBar
                personal={personal}
                onConnect={canWriteScope ? () => startCreate(!creatingGlobal) : undefined}
                connectTestId={creatingGlobal ? "mcp-new" : "mcp-new-yours"}
              />
              {renderList(personal ? "mcp-yours" : "mcp-global", singleList, false, personal)}
            </>
          )}
        </section>
      ) : null}

      <ServerDialog
        key={creating ? editing : (editingServer?.name ?? "closed")}
        server={editingServer}
        forBot={editingForBot}
        open={editing !== undefined}
        taken={new Set((editingForBot ? global : yours).map((s) => s.name))}
        onOpenChange={(open) => {
          if (!open) setEditing(undefined);
        }}
        onDone={async (name, created) => {
          setEditing(undefined);
          toast(`${name} ${created ? "added" : "saved"}. It applies to the next turn.`, "ok");
          await reload();
        }}
      />
    </PageFrame>
  );
}

function TabCount({ count }: { count: number }) {
  return (
    <span className="inline-grid h-5 min-w-[22px] place-items-center rounded-sm border border-line-strong bg-panel px-1.5 text-xs font-bold text-fg tabular-nums">
      {count}
    </span>
  );
}

function ScopeBar({
  personal,
  onConnect,
  connectTestId,
}: {
  personal: boolean;
  onConnect?: () => void;
  connectTestId: string;
}) {
  return (
    <div className="grid grid-cols-1 items-center gap-4 border-b border-border bg-raised px-6 py-4 sm:grid-cols-[minmax(0,1fr)_auto]">
      <p className="m-0 text-soft">
        {personal
          ? "Personal servers are available to your playbooks."
          : "Shared servers are managed for the workspace."}
      </p>
      {onConnect ? (
        <Button
          type="button"
          className="shrink-0 justify-self-start sm:justify-self-end"
          data-testid={connectTestId}
          onClick={onConnect}
        >
          <Plus />
          Connect MCP
        </Button>
      ) : null}
    </div>
  );
}

function McpEmpty({ personal, canAdd }: { personal: boolean; canAdd: boolean }) {
  return (
    <div className="grid min-h-80 content-center justify-items-center gap-3 px-6 py-12 text-center">
      <span
        aria-hidden
        className="grid size-12 place-items-center rounded-md border border-line-strong bg-raised font-mono text-lg font-bold text-soft"
      >
        M
      </span>
      <h3 className="text-lg font-bold">No {personal ? "personal" : "global"} MCP servers</h3>
      <p className="m-0 max-w-[58ch] text-soft">
        {personal
          ? canAdd
            ? "Connect an MCP server to make it available to your playbooks. A playbook must still name it before an assistant can use it."
            : "Personal servers belong to a signed-in account."
          : "Connect an MCP server for the workspace. A playbook must still name it before an assistant can use it."}
      </p>
    </div>
  );
}

function ServerIcon({
  server,
  large,
}: {
  server: { name: string; url: string };
  large?: boolean;
}) {
  const preset = presetFor(server);
  return (
    <span
      aria-hidden
      className={cn(
        "grid shrink-0 place-items-center rounded-sm border border-line-strong bg-white",
        large ? "size-14" : "size-10",
      )}
    >
      <McpMark name={preset?.name ?? "unknown"} className={large ? "size-8 rounded-sm" : "size-6 rounded-sm"} />
    </span>
  );
}

/**
 * ServerRow is one registration on the list. The name opens the detail, where the
 * credential, edit, and remove actions live. The URL stays in full because it is what a
 * turn will talk to, and a server no playbook names is stated rather than left as an absence.
 */
function ServerRow({
  server,
  busy,
  readOnly,
  onOpen,
  onToggle,
  onFallback,
}: {
  server: McpServer;
  busy: boolean;
  readOnly?: boolean;
  onOpen: () => void;
  onToggle: (enabled: boolean) => void;
  onFallback: (fallback: boolean) => void;
}) {
  const uid = useId();
  const playbooks =
    server.playbooks.length > 0
      ? server.playbooks.map((p) => `/${p}`).join(", ")
      : "No playbook names this server yet.";

  return (
    <li data-testid="mcp-row" className="border-b border-border last:border-b-0">
      <article className="grid items-start gap-4 px-6 py-6 md:grid-cols-[minmax(0,1fr)_auto]">
        <div className="min-w-0">
          <div className="mb-2 flex flex-wrap items-center gap-2">
            <ServerIcon server={server} />
            <h3 className="text-lg font-bold">
              <button
                type="button"
                className="bg-transparent p-0 text-left font-bold text-fg underline decoration-transparent underline-offset-4 hover:decoration-current"
                onClick={onOpen}
              >
                {server.name}
              </button>
            </h3>
            <CredentialBadge server={server} />
            <Badge variant={server.enabled ? "ok" : "default"}>
              {server.enabled ? "Enabled" : "Disabled"}
            </Badge>
            {server.tokenSet && server.authKind === "oauth" && !server.refreshable ? (
              <Badge variant="warn">expires, not refreshable</Badge>
            ) : null}
          </div>
          {server.description ? (
            <p className="mb-2 text-fg leading-normal">{server.description}</p>
          ) : null}
          <code className="block max-w-full font-mono text-sm leading-normal break-all text-soft">
            {server.url}
          </code>
          <p className="mt-3 text-soft">
            <strong className="font-bold text-fg">Playbook use · </strong>
            <span>{playbooks}</span>
          </p>
        </div>
        {readOnly ? null : (
          <div className="flex flex-col items-start gap-2 md:items-end">
            <Button type="button" variant="outline" size="sm" onClick={onOpen}>
              View details
            </Button>
            <div className="grid justify-items-start gap-y-0 md:justify-items-end">
              <div className="grid grid-cols-[42px_4.5rem] items-center gap-x-3">
                <Switch
                  id={`${uid}-enabled`}
                  aria-label={`${server.name} is enabled`}
                  checked={server.enabled}
                  disabled={busy}
                  onCheckedChange={onToggle}
                />
                <Label htmlFor={`${uid}-enabled`} className="min-h-11 cursor-pointer text-sm font-bold text-fg">
                  Enabled
                </Label>
              </div>
              {!server.owner ? (
                <Tooltip
                  label={`Used when someone has no personal server named ${server.name}.`}
                  side="left"
                >
                  <div className="grid grid-cols-[42px_4.5rem] items-center gap-x-3">
                    <Switch
                      id={`${uid}-fallback`}
                      data-testid="mcp-fallback"
                      aria-label={`Use ${server.name} when someone has no server of this name`}
                      checked={server.fallback}
                      disabled={busy}
                      onCheckedChange={onFallback}
                    />
                    <Label
                      htmlFor={`${uid}-fallback`}
                      className="min-h-11 cursor-pointer text-sm font-bold text-fg underline decoration-dotted underline-offset-[3px]"
                    >
                      Fallback
                    </Label>
                  </div>
                </Tooltip>
              ) : null}
            </div>
          </div>
        )}
      </article>
    </li>
  );
}

function CredentialBadge({ server }: { server: McpServer }) {
  if (server.tokenSet && server.authKind === "oauth") {
    return (
      <Badge variant="ok">Signed in{server.account ? ` · ${server.account}` : ""}</Badge>
    );
  }
  if (server.tokenSet) {
    return (
      <Badge variant="ok">Token set{server.tokenHint ? ` ···${server.tokenHint}` : ""}</Badge>
    );
  }
  return <Badge>No credential</Badge>;
}

/**
 * ServerDetail is the registration opened from the list, in a modal so the list stays put.
 * Sign-in, the pasted token, edit, and remove live here.
 */
function ServerDetail({
  server,
  busy,
  readOnly,
  onBack,
  onToggle,
  onFallback,
  onEdit,
  onDelete,
  onTokenChanged,
}: {
  server: McpServer;
  busy: boolean;
  readOnly?: boolean;
  onBack: () => void;
  onToggle: (enabled: boolean) => void;
  onFallback: (fallback: boolean) => void;
  onEdit: () => void;
  onDelete: () => void;
  onTokenChanged: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const [tokenOpen, setTokenOpen] = useState(false);
  const [signInError, setSignInError] = useState<string>();
  const uid = useId();
  const personal = Boolean(server.owner);
  const playbooks =
    server.playbooks.length > 0
      ? server.playbooks.map((p) => `/${p}`).join(", ")
      : "No playbook names this server yet.";

  const signIn = useMutation({
    mutationFn: () =>
      agent.startMcpOAuth({
        name: server.name,
        redirectUri: callbackURL(),
        forBot: !server.owner,
      }),
    onSuccess: (res) => {
      setSignInError(undefined);
      rememberPending({
        flowId: res.flowId,
        state: res.state,
        name: server.name,
        issuer: res.issuer,
      });
      window.location.assign(res.authorizeUrl);
    },
    onError: (err) => setSignInError(errorMessage(err)),
  });

  // The token and remove dialogs portal outside this one. A click in either is "outside"
  // this content, and dismissing here would unmount them.
  const hold = (event: { preventDefault: () => void }) => {
    if (confirming || tokenOpen) event.preventDefault();
  };

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onBack(); }}>
    <DialogContent
      className="flex max-h-[calc(100vh-2rem)] max-w-[960px] flex-col gap-0 overflow-hidden p-0"
      onPointerDownOutside={hold}
      onInteractOutside={hold}
      onFocusOutside={hold}
    >
    <div className="grid min-h-0 flex-1 gap-6 overflow-y-auto p-6">
      <div className="flex flex-wrap items-start justify-between gap-4 pr-8">
        <div className="flex min-w-0 items-center gap-4">
          <ServerIcon server={server} large />
          <div className="min-w-0">
            <p className="mb-1 text-xs font-bold tracking-[0.08em] text-muted uppercase">
              {personal ? "Personal MCP server" : "Global MCP server"}
            </p>
            <DialogTitle className="text-2xl leading-tight font-bold tracking-normal break-all">
              {server.name}
            </DialogTitle>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <CredentialBadge server={server} />
          <Badge variant={server.enabled ? "ok" : "default"}>
            {server.enabled ? "Enabled" : "Disabled"}
          </Badge>
        </div>
      </div>

      <DialogDescription className="max-w-[68ch] text-base text-soft">
        {server.description || "Custom MCP server."}
      </DialogDescription>

      <section
        aria-label="Credential"
        className="grid justify-items-start gap-3 rounded-md border border-line-strong bg-raised p-4"
      >
        <div className="flex flex-wrap items-center gap-3">
          <h4 className="m-0 text-base font-bold">Credential</h4>
          <CredentialBadge server={server} />
        </div>
        <p className="m-0 max-w-[68ch] leading-normal text-soft">
          {server.tokenSet
            ? "The credential is stored as a secret. Nothing on this screen can read it back."
            : "No credential is stored yet. Sign in, or paste a token."}
        </p>
        {server.tokenSetBy ? (
          <p className="m-0 text-sm text-muted">
            {server.authKind === "oauth" ? "signed in by" : "token by"} {server.tokenSetBy}
            {server.tokenSetAt ? (
              <>
                {" · "}
                <span title={absolute(server.tokenSetAt)}>{relative(server.tokenSetAt)}</span>
              </>
            ) : null}
          </p>
        ) : null}
        {server.tokenSet && server.authKind === "oauth" && !server.refreshable ? (
          <p className="m-0 font-bold text-warn">
            This sign-in expires and cannot be refreshed. Sign in again before it lapses.
          </p>
        ) : null}
        {signInError ? (
          <Alert variant="destructive" role="alert">
            {signInError}
          </Alert>
        ) : null}
        {readOnly ? null : (
          <div className="flex flex-wrap gap-2">
            <Button
              type="button"
              variant="outline"
              size="sm"
              data-testid="mcp-signin"
              disabled={signIn.isPending}
              onClick={() => signIn.mutate()}
            >
              <LogIn />
              {signIn.isPending
                ? "Discovering…"
                : server.authKind === "oauth"
                  ? "Sign in again"
                  : "Sign in"}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              data-testid="mcp-token"
              onClick={() => setTokenOpen(true)}
            >
              <KeyRound />
              {server.tokenSet && server.authKind !== "oauth" ? "Replace token" : "Paste a token"}
            </Button>
            <Button
              type="button"
              variant="outline"
              size="sm"
              data-testid="mcp-edit"
              aria-label={`Edit ${server.name}`}
              disabled={busy}
              onClick={onEdit}
            >
              <Pencil />
              Edit
            </Button>
          </div>
        )}
      </section>

      <dl className="m-0 grid grid-cols-1 overflow-hidden rounded-md border border-border sm:grid-cols-2">
        <DetailField label="MCP endpoint" code>
          {server.url}
        </DetailField>
        <DetailField label="Ownership">
          {personal ? "Yours · personal to your account" : "Global · shared with the workspace"}
        </DetailField>
        <DetailField label="Credential">
          {server.tokenSet
            ? server.authKind === "oauth"
              ? `Signed in${server.account ? ` as ${server.account}` : ""}`
              : `Token set${server.tokenHint ? ` ···${server.tokenHint}` : ""}`
            : "No credential set"}
        </DetailField>
        <DetailField label="Playbook use">{playbooks}</DetailField>
      </dl>

      {readOnly ? null : (
        <section className="overflow-hidden rounded-md border border-border">
          <h4 className="m-0 border-b border-border p-4 text-base font-bold">Availability</h4>
          <div className="flex items-center justify-between gap-4 p-4">
            <div className="min-w-0">
              <strong className="block">Enabled</strong>
              <p className="mt-1 mb-0 text-sm text-soft">
                Make this server available to playbooks that name it.
              </p>
            </div>
            <Switch
              id={`${uid}-enabled`}
              aria-label={`${server.name} is enabled`}
              checked={server.enabled}
              disabled={busy}
              onCheckedChange={onToggle}
              className="shrink-0"
            />
          </div>
          {personal ? null : (
            <div className="flex items-center justify-between gap-4 border-t border-border p-4">
              <div className="min-w-0">
                <strong className="block">Global fallback</strong>
                <p className="mt-1 mb-0 text-sm text-soft">
                  Use this server when someone has no personal server with this name.
                </p>
              </div>
              <Switch
                id={`${uid}-fallback`}
                aria-label={`Use ${server.name} when someone has no server of this name`}
                checked={server.fallback}
                disabled={busy}
                onCheckedChange={onFallback}
                className="shrink-0"
              />
            </div>
          )}
        </section>
      )}

      {readOnly ? null : (
        <section className="flex flex-wrap items-center justify-between gap-4 rounded-md border border-border p-4">
          <div className="min-w-0">
            <h4 className="m-0 text-base font-bold">Remove server</h4>
            <p className="mt-1 mb-0 text-sm text-soft">
              Remove this registration from {personal ? "your account" : "the workspace"}. This
              action needs confirmation.
            </p>
          </div>
          <Button
            type="button"
            variant="destructive"
            data-testid="mcp-delete"
            aria-label={`Remove ${server.name}`}
            disabled={busy}
            onClick={() => setConfirming(true)}
          >
            <Trash2 />
            Remove server
          </Button>
        </section>
      )}

      <TokenDialog
        key={`${server.name}:${server.tokenHint}`}
        server={server}
        open={tokenOpen}
        onOpenChange={setTokenOpen}
        onDone={() => {
          setTokenOpen(false);
          onTokenChanged();
        }}
      />

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle className="text-2xl leading-tight font-bold tracking-normal">
              Remove MCP server?
            </DialogTitle>
            <DialogDescription className="text-base text-soft">
              Remove {server.name} from {personal ? "Yours" : "Global"}? The stored token goes
              with it. There is no undo.
            </DialogDescription>
          </DialogHeader>
          {server.playbooks.length > 0 ? (
            <Alert variant="warn" role="note">
              {server.playbooks.map((p) => `/${p}`).join(", ")}{" "}
              {server.playbooks.length === 1 ? "names" : "name"} this server. Every turn of{" "}
              {server.playbooks.length === 1 ? "that playbook" : "those playbooks"} will fail
              until the name is taken out of it.
            </Alert>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" size="sm" onClick={() => setConfirming(false)}>
              Keep server
            </Button>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              data-testid="mcp-delete-confirm"
              aria-label={`Confirm removing ${server.name}`}
              disabled={busy}
              onClick={() => {
                setConfirming(false);
                onDelete();
              }}
            >
              {busy ? "Removing…" : "Remove server"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
    </DialogContent>
    </Dialog>
  );
}

function DetailField({
  label,
  code,
  children,
}: {
  label: string;
  code?: boolean;
  children: ReactNode;
}) {
  return (
    <div className="min-w-0 border-b border-border p-4 sm:odd:border-r">
      <dt className="text-sm font-bold text-muted">{label}</dt>
      <dd className={cn("mt-2 mb-0 break-all text-fg", code && "font-mono text-sm leading-normal")}>
        {children}
      </dd>
    </div>
  );
}

/**
 * ServerDialog adds a registration or edits one. The name is immutable after creation: it is
 * what playbooks name and what the token's secret is called, so renaming would silently
 * detach both.
 *
 * Adding starts on a product picker — known MCP endpoints with their icons, plus Custom —
 * because the hard part of a well-known server is remembering its URL. A wrong URL produces
 * a turn that fails on a tool call rather than a form that says no.
 */
function ServerDialog({
  server,
  forBot = false,
  open,
  taken,
  onOpenChange,
  onDone,
}: {
  server?: McpServer;
  forBot?: boolean;
  open: boolean;
  taken: Set<string>;
  onOpenChange: (open: boolean) => void;
  onDone: (name: string, created: boolean) => void | Promise<void>;
}) {
  const uid = useId();
  const creating = server === undefined;
  const [picked, setPicked] = useState<McpPreset | "custom" | undefined>(creating ? undefined : "custom");
  const [name, setName] = useState(server?.name ?? "");
  const [url, setUrl] = useState(server?.url ?? "");
  const [enabled, setEnabled] = useState(server?.enabled ?? true);
  const [fallback, setFallback] = useState(server?.fallback ?? false);
  const [token, setToken] = useState("");
  const [config, setConfig] = useState(server?.config ?? "");
  const [advanced, setAdvanced] = useState(config !== "");
  const [error, setError] = useState<string>();

  // The URL field and the JSON's url are one value, so editing either updates the other.
  function changeUrl(next: string) {
    setUrl(next);
    const entry = parseEntry(config);
    if (entry) setConfig(entryJSON({ ...entry, url: next.trim() }));
  }

  function changeConfig(next: string) {
    setConfig(next);
    const entry = parseEntry(next);
    if (typeof entry?.url === "string") setUrl(entry.url);
  }

  function toggleAdvanced(open: boolean) {
    setAdvanced(open);
    if (open && config.trim() === "") setConfig(entryJSON({ type: "remote", url: url.trim() }));
  }

  const preset = picked === undefined || picked === "custom" ? undefined : picked;
  const help = tokenHelp(preset);
  const picking = creating && picked === undefined;
  const description = creating ? (preset?.description ?? "") : (server.description ?? "");
  const helper = picking
    ? "Pick a known server to fill in its endpoint, or Custom to enter your own."
    : description || CUSTOM_MCP_DESCRIPTION;

  function apply(next: McpPreset | "custom") {
    setPicked(next);
    setError(undefined);
    if (next === "custom") {
      setName("");
      setUrl("");
      return;
    }
    setName(next.name);
    setUrl(next.url);
  }

  const save = useMutation({
    mutationFn: async () => {
      const body = {
        name: name.trim(),
        url: url.trim(),
        description: description.trim(),
        enabled,
        config: config.trim(),
        fallback: forBot ? fallback : false,
      };
      if (creating) {
        await agent.createMcpServer({ server: body, token: token.trim(), forBot });
        return true;
      }
      await agent.updateMcpServer({ server: body, forBot });
      return false;
    },
    onSuccess: async (created) => {
      await onDone(name.trim(), created);
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const problems: string[] = [];
  if (creating && name.trim() !== "" && taken.has(name.trim())) {
    problems.push(`A server named ${name.trim()} is already registered.`);
  }
  const configProblems =
    config.trim() !== "" && parseEntry(config) === undefined ? ["Config must be a JSON object."] : [];
  const ready =
    name.trim() !== "" && url.trim() !== "" && problems.length === 0 && configProblems.length === 0;

  const title = !creating
    ? `Edit ${server.name}`
    : picking
      ? `New ${forBot ? "global" : "personal"} MCP server`
      : preset
        ? `Add ${preset.label}`
        : "Add a custom server";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-[720px]">
        <DialogHeader>
          <DialogTitle className="text-2xl leading-tight font-bold tracking-normal">{title}</DialogTitle>
          <DialogDescription className="text-base text-soft">{helper}</DialogDescription>
        </DialogHeader>

        {picking ? (
          <>
            <ProductPicker onPick={apply} />
            <DialogFooter>
              <Button type="button" variant="outline" size="sm" onClick={() => onOpenChange(false)}>
                Cancel
              </Button>
            </DialogFooter>
          </>
        ) : (
          <form
            className="space-y-4"
            onSubmit={(e) => {
              e.preventDefault();
              setError(undefined);
              save.mutate();
            }}
          >
            <Field
              id={`${uid}-name`}
              label="Name"
              required
              problems={problems}
              hint={
                creating
                  ? "Lower case, letters, digits and hyphens. Playbooks name this, and the model sees its tools as mcp__<name>__*."
                  : "The name cannot change: playbooks name it, and so does its stored token."
              }
            >
              {(control) => (
                <Input
                  {...control}
                  value={name}
                  onChange={(e) => setName(e.target.value)}
                  disabled={!creating}
                  spellCheck={false}
                  placeholder={preset?.name ?? "linear"}
                  className="font-mono"
                />
              )}
            </Field>

            <Field id={`${uid}-url`} label="URL" required>
              {(control) => (
                <Input
                  {...control}
                  value={url}
                  onChange={(e) => changeUrl(e.target.value)}
                  spellCheck={false}
                  placeholder={preset?.url ?? "https://mcp.example.com/mcp"}
                  className="font-mono text-xs"
                />
              )}
            </Field>

            {creating ? (
              <Field id={`${uid}-token`} label="Token" hint={help.hint}>
                {(control) => (
                  <Input
                    {...control}
                    type="password"
                    value={token}
                    onChange={(e) => setToken(e.target.value)}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder={help.placeholder}
                    className="font-mono text-xs"
                  />
                )}
              </Field>
            ) : null}

            <Collapsible open={advanced} onOpenChange={toggleAdvanced} className="rounded-md border border-border p-3">
              <CollapsibleTrigger asChild>
                <button type="button" className="flex items-center gap-1 font-bold">
                  <ChevronRight
                    aria-hidden
                    className={cn("size-3.5 transition-transform duration-150", advanced && "rotate-90")}
                  />
                  Advanced options
                </button>
              </CollapsibleTrigger>
              <CollapsibleContent className="pt-2">
                <Field
                  id={`${uid}-config`}
                  label="Config"
                  problems={configProblems}
                  hint="The harness's whole entry for this server: add headers, timeout (ms) or any other option. Its url follows the URL field, and Enabled stays the switch below. Stored in clear, so not for secrets — the token sets Authorization."
                >
                  {(control) => (
                    <Textarea
                      {...control}
                      value={config}
                      onChange={(e) => changeConfig(e.target.value)}
                      spellCheck={false}
                      rows={8}
                      className="font-mono text-xs"
                    />
                  )}
                </Field>
              </CollapsibleContent>
            </Collapsible>

            <label
              htmlFor={`${uid}-enabled`}
              className="flex items-start gap-3 rounded-md border border-border p-3"
            >
              <Switch
                id={`${uid}-enabled`}
                checked={enabled}
                onCheckedChange={setEnabled}
                aria-label="Enabled"
                className="mt-0.5"
              />
              <span>
                <strong className="block">Enabled</strong>
                <small className="mt-0.5 block text-sm leading-normal text-soft">
                  Make this registered server available to playbooks that name it.
                </small>
              </span>
            </label>
            {forBot ? (
              <label
                htmlFor={`${uid}-fallback`}
                className="flex items-start gap-3 rounded-md border border-border p-3"
              >
                <Switch
                  id={`${uid}-fallback`}
                  checked={fallback}
                  onCheckedChange={setFallback}
                  aria-label="Use when someone has no server of this name"
                  className="mt-0.5"
                />
                <span>
                  <strong className="block">Use when someone has none</strong>
                  <small className="mt-0.5 block text-sm leading-normal text-soft">
                    Global fallback applies when a person has no personal server with this name.
                  </small>
                </span>
              </label>
            ) : null}

            {error ? (
              <Alert variant="destructive" role="alert">
                {error}
              </Alert>
            ) : null}

            <DialogFooter>
              <Button
                type="button"
                variant="outline"
                size="sm"
                onClick={() => {
                  if (creating) {
                    setPicked(undefined);
                    setError(undefined);
                    return;
                  }
                  onOpenChange(false);
                }}
                disabled={save.isPending}
              >
                {creating ? "Back" : "Cancel"}
              </Button>
              <Button type="submit" size="sm" data-testid="mcp-save" disabled={!ready || save.isPending}>
                {save.isPending ? "Saving…" : creating ? "Add server" : "Save"}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}

function ProductPicker({ onPick }: { onPick: (next: McpPreset | "custom") => void }) {
  return (
    <div className="grid grid-cols-1 gap-3 sm:grid-cols-2">
      {MCP_PRESETS.map((p) => (
        <button
          key={p.name}
          type="button"
          data-testid="mcp-preset"
          onClick={() => onPick(p)}
          className="flex min-h-[136px] flex-col items-start gap-2 rounded-md border border-line-strong bg-panel p-4 text-left hover:border-accent hover:bg-raised"
        >
          <ServerIcon server={p} />
          <strong className="text-base">{p.label}</strong>
          <span className="text-sm leading-snug text-soft">{p.description}</span>
        </button>
      ))}
      <button
        type="button"
        data-testid="mcp-preset-custom"
        onClick={() => onPick("custom")}
        className="flex min-h-[136px] flex-col items-start gap-2 rounded-md border border-line-strong bg-panel p-4 text-left hover:border-accent hover:bg-raised"
      >
        <span
          aria-hidden
          className="grid size-10 place-items-center rounded-sm border border-line-strong bg-white text-[#222326]"
        >
          <svg viewBox="0 0 24 24" className="size-[22px] fill-none stroke-current stroke-[1.8]">
            <path d="M9 3v6m6-6v6M7 9h10v4a5 5 0 0 1-10 0V9Zm5 9v3" strokeLinecap="round" strokeLinejoin="round" />
          </svg>
        </span>
        <strong className="text-base">Custom</strong>
        <span className="text-sm leading-snug text-soft">Enter an MCP server URL</span>
      </button>
    </div>
  );
}

/**
 * TokenDialog is the only way a credential is written, and it is deliberately separate from
 * the registration form: replacing a token is not the same decision as fixing a typo in a
 * URL, and an edit form that carried a password field would ask for one every time.
 */
function TokenDialog({
  server,
  open,
  onOpenChange,
  onDone,
}: {
  server: McpServer;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  onDone: () => void;
}) {
  const uid = useId();
  const toast = useToast();
  const [token, setToken] = useState("");
  const [error, setError] = useState<string>();
  const help = tokenHelp(presetFor(server));

  // Every path out of this dialog empties the field, so a token typed and abandoned is not
  // sitting in a form the next person to open it inherits.
  function close(next: boolean) {
    if (!next) {
      setToken("");
      setError(undefined);
    }
    onOpenChange(next);
  }

  function done() {
    setToken("");
    setError(undefined);
    onDone();
  }

  const save = useMutation({
    mutationFn: () =>
      agent.setMcpServerToken({ name: server.name, token: token.trim(), forBot: !server.owner }),
    onSuccess: () => {
      toast(`${server.name}'s token is stored. It applies to the next turn.`, "ok");
      done();
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const clear = useMutation({
    mutationFn: () => agent.clearMcpServerToken({ name: server.name, forBot: !server.owner }),
    onSuccess: () => {
      toast(`${server.name}'s token is removed. It stays registered.`, "ok");
      done();
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const busy = save.isPending || clear.isPending;

  return (
    <Dialog open={open} onOpenChange={close}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle className="text-2xl leading-tight font-bold tracking-normal">
            {server.tokenSet ? "Replace" : "Add"} {server.name}&apos;s token
          </DialogTitle>
          <DialogDescription className="text-base text-soft">
            It is stored as the Podium secret <code className="font-mono">{server.tokenSecret}</code>{" "}
            and reaches a turn as <code className="font-mono">{server.tokenEnv}</code>. Nothing
            reads it back, not this screen, not the conductor. A sign-in ends up in exactly
            the same place; pasting a token replaces one.
          </DialogDescription>
        </DialogHeader>

        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            setError(undefined);
            save.mutate();
          }}
        >
          <Field
            id={`${uid}-token`}
            label="Token"
            required
            hint={help.placeholder ? help.hint : "Sent as Authorization: Bearer <token>."}
          >
            {(control) => (
              <Input
                {...control}
                type="password"
                value={token}
                onChange={(e) => setToken(e.target.value)}
                autoComplete="off"
                spellCheck={false}
                placeholder={help.placeholder}
                className="font-mono text-xs"
              />
            )}
          </Field>

          {error ? (
            <Alert variant="destructive" role="alert">
              {error}
            </Alert>
          ) : null}

          <DialogFooter>
            {server.tokenSet ? (
              <Button
                type="button"
                variant="outline"
                size="sm"
                data-testid="mcp-token-clear"
                disabled={busy}
                onClick={() => {
                  setError(undefined);
                  clear.mutate();
                }}
              >
                {clear.isPending ? "Removing…" : "Remove token"}
              </Button>
            ) : null}
            <Button
              type="button"
              variant="outline"
              size="sm"
              disabled={busy}
              onClick={() => close(false)}
            >
              Cancel
            </Button>
            <Button
              type="submit"
              size="sm"
              data-testid="mcp-token-save"
              disabled={token.trim() === "" || busy}
            >
              {save.isPending ? "Storing…" : "Store token"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function McpSkeleton() {
  return (
    <ul className="space-y-2.5">
      {[0, 1].map((i) => (
        <li key={i} className="rounded-xl border border-border bg-card px-4 py-3.5">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="mt-2 h-3 w-64" />
        </li>
      ))}
    </ul>
  );
}
