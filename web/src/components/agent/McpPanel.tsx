import { useId, useState } from "react";
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
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageFrame } from "../PageHeader";
import { Field } from "../submit/Field";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
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
  const [tab, setTab] = useState<"yours" | "global">("yours");

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
  // Both lists only when a signed-in person may also edit the company list. A member
  // never sees Global. The dev token is not a person, so the page is Global alone.
  const showTabs = Boolean(personal && manage);
  const creatingGlobal = showTabs ? tab === "global" : !personal;
  const creating = editing === "new" || editing === "new-bot";
  const editingServer = typeof editing === "object" ? editing : undefined;
  const editingForBot = editing === "new-bot" || (editingServer !== undefined && !editingServer.owner);
  const loaded = !list.isPending && !(list.isError && isAgentUnreachable(list.error));
  const singleList = personal ? yours : global;

  const renderList = (testId: string, rows: McpServer[], readOnly: boolean) => (
    <div data-testid={testId} className="space-y-2.5">
      {rows.length === 0 ? (
        <p className="text-xs text-muted">None yet.</p>
      ) : (
        <ul className="space-y-2.5">
          {rows.map((s) => (
            <ServerRow
              key={`${s.owner}:${s.name}`}
              server={s}
              busy={
                (setEnabled.isPending && setEnabled.variables?.server.name === s.name) ||
                (remove.isPending && remove.variables?.name === s.name)
              }
              readOnly={readOnly}
              onToggle={(enabled) => setEnabled.mutate({ server: s, enabled })}
              onFallback={(fallback) =>
                setEnabled.mutate({ server: s, enabled: s.enabled, fallback })
              }
              onEdit={() => setEditing(s)}
              onDelete={() => remove.mutate({ name: s.name, forBot: !s.owner })}
              onTokenChanged={reload}
            />
          ))}
        </ul>
      )}
    </div>
  );

  if (list.isError && !isAgentUnreachable(list.error)) {
    return (
      <Empty icon={Plug} title="Could not read the MCP servers" hint={errorMessage(list.error)} />
    );
  }

  return (
    <PageFrame
        title="MCP servers"
        actions={
          personal || manage ? (
            <Button
              type="button"
              size="sm"
              data-testid={creatingGlobal ? "mcp-new" : "mcp-new-yours"}
              onClick={() => setEditing(creatingGlobal ? "new-bot" : "new")}
            >
              <Plus />
              New server
            </Button>
          ) : null
        }
      >

      {list.isError && isAgentUnreachable(list.error) ? (
        <ConductorDown
          what="The MCP servers could not be read"
          onRetry={() => void list.refetch()}
          retrying={list.isFetching}
        />
      ) : null}

      {list.isPending ? <McpSkeleton /> : null}

      {loaded && !showTabs && singleList.length === 0 ? (
        <Empty
          icon={Plug}
          title="No MCP servers"
          hint="Add one and it becomes available to the playbooks that name it."
        />
      ) : null}

      {loaded && showTabs ? (
        <Tabs value={tab} onValueChange={(value) => setTab(value as "yours" | "global")}>
          <TabsList>
            <TabsTrigger value="yours">Yours</TabsTrigger>
            <TabsTrigger value="global">Global</TabsTrigger>
          </TabsList>
          <TabsContent value="yours">{renderList("mcp-yours", yours, false)}</TabsContent>
          <TabsContent value="global">{renderList("mcp-global", global, false)}</TabsContent>
        </Tabs>
      ) : null}

      {loaded && !showTabs && singleList.length > 0
        ? renderList(personal ? "mcp-yours" : "mcp-global", singleList, false)
        : null}

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

/**
 * ServerRow is one registration. The URL is shown in full because it is the whole of what
 * identifies the thing a turn will be talking to, and "no playbook names it" is stated
 * rather than left as an absence — a registered server nothing uses is the normal state
 * right after adding one, and the row is where somebody finds out they are not done.
 */
function ServerRow({
  server,
  busy,
  readOnly,
  onToggle,
  onFallback,
  onEdit,
  onDelete,
  onTokenChanged,
}: {
  server: McpServer;
  busy: boolean;
  readOnly?: boolean;
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

  /**
   * Starting a sign-in is one call and then a whole-page navigation away from this origin.
   *
   * The browser sends its OWN callback URL, because it is the only party that knows the
   * address this control plane is reached at — the conductor never has to be told, and the
   * client is registered against whatever it is. What comes back is a flow id and a state,
   * which name a sign-in rather than bearing one: the verifier that makes them usable stays
   * on the conductor.
   */
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

  return (
    <li
      data-testid="mcp-row"
      className="rounded-xl border border-border bg-card px-4 py-3.5 shadow-xs"
    >
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <McpMark name={presetFor(server)?.name ?? "unknown"} className="size-6 rounded-md" />
            <span className="font-mono text-sm font-medium text-fg">{server.name}</span>
            {server.tokenSet && server.authKind === "oauth" ? (
              <Badge tone="ok" dot={false}>
                <LogIn className="size-3" />
                signed in{server.account ? ` · ${server.account}` : ""}
              </Badge>
            ) : server.tokenSet ? (
              <Badge tone="ok" dot={false}>
                <KeyRound className="size-3" />
                token ···{server.tokenHint}
              </Badge>
            ) : (
              <Badge tone="idle">no credential</Badge>
            )}
            {server.tokenSet && server.authKind === "oauth" && !server.refreshable ? (
              // Without a refresh token the sign-in dies at expiry and a human has to do it
              // again. Worth saying before a turn is the thing that finds out.
              <Badge tone="warn">expires, not refreshable</Badge>
            ) : null}
            {!server.enabled ? <Badge tone="lost">disabled</Badge> : null}
          </div>
          <p className="truncate font-mono text-xs text-muted" title={server.url}>
            {server.url}
          </p>
          <p
            className="h-4 max-w-2xl truncate text-xs leading-4 text-muted"
            title={server.description || undefined}
          >
            {server.description || "\u00a0"}
          </p>
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {readOnly ? null : (
          <>
          <div className="flex items-center gap-2">
            <Switch
              id={`${uid}-enabled`}
              aria-label={`${server.name} is enabled`}
              checked={server.enabled}
              disabled={busy}
              onCheckedChange={onToggle}
            />
            <Label htmlFor={`${uid}-enabled`} className="cursor-pointer">
              Enabled
            </Label>
          </div>
          {!server.owner ? (
            <div className="flex items-center gap-2">
              <Switch
                id={`${uid}-fallback`}
                data-testid="mcp-fallback"
                aria-label={`Use ${server.name} when someone has no server of this name`}
                checked={server.fallback}
                disabled={busy}
                onCheckedChange={onFallback}
              />
              <Label htmlFor={`${uid}-fallback`} className="cursor-pointer">
                Use when someone has none
              </Label>
            </div>
          ) : null}
          <Tooltip label={`Edit ${server.name}`}>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              data-testid="mcp-edit"
              aria-label={`Edit ${server.name}`}
              disabled={busy}
              onClick={onEdit}
            >
              <Pencil />
            </Button>
          </Tooltip>
          <Tooltip label={`Remove ${server.name}`}>
            <Button
              type="button"
              variant="ghost"
              size="icon-sm"
              data-testid="mcp-delete"
              aria-label={`Remove ${server.name}`}
              disabled={busy}
              onClick={() => setConfirming(true)}
              className="hover:bg-err/12 hover:text-err"
            >
              <Trash2 />
            </Button>
          </Tooltip>
          </>
          )}
        </div>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-hairline pt-2.5">
        {server.playbooks.length > 0 ? (
          <Chip>{server.playbooks.map((p) => `/${p}`).join(" ")}</Chip>
        ) : (
          <Chip>no playbook names it</Chip>
        )}
        {readOnly ? null : (
        <Button
          type="button"
          variant="outline"
          size="sm"
          data-testid="mcp-signin"
          disabled={signIn.isPending}
          onClick={() => signIn.mutate()}
        >
          <LogIn />
          {signIn.isPending ? "Discovering…" : server.authKind === "oauth" ? "Sign in again" : "Sign in"}
        </Button>
        )}
        {readOnly ? null : (
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
        )}
        {server.tokenSetBy ? (
          <span className="ml-auto shrink-0 text-2xs text-faint">
            {server.authKind === "oauth" ? "signed in by" : "token by"} {server.tokenSetBy}
            {server.tokenSetAt ? (
              <>
                {" · "}
                <span title={absolute(server.tokenSetAt)}>{relative(server.tokenSetAt)}</span>
              </>
            ) : null}
          </span>
        ) : null}
      </div>

      {signInError ? (
        <Alert variant="destructive" role="alert" className="mt-3">
          {signInError}
        </Alert>
      ) : null}

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
            <DialogTitle>Remove {server.name}?</DialogTitle>
            <DialogDescription>
              The stored token goes with it. There is no undo.
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
              Keep
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
    </li>
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
      ? "Add an MCP server"
      : preset
        ? `Add ${preset.label}`
        : "Add a custom server";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="sm:max-w-lg">
        <DialogHeader>
          <DialogTitle>{title}</DialogTitle>
          <DialogDescription>{helper}</DialogDescription>
        </DialogHeader>

        {picking ? (
          <ProductPicker onPick={apply} />
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

            <Collapsible open={advanced} onOpenChange={toggleAdvanced}>
              <CollapsibleTrigger asChild>
                <button
                  type="button"
                  className="flex items-center gap-1 text-xs font-medium text-muted hover:text-fg"
                >
                  <ChevronRight
                    aria-hidden
                    className={cn("size-3.5 transition-transform duration-150", advanced && "rotate-90")}
                  />
                  Advanced (JSON)
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

            <div className="flex items-center gap-2">
              <Switch
                id={`${uid}-enabled`}
                checked={enabled}
                onCheckedChange={setEnabled}
                aria-label="Enabled"
              />
              <Label htmlFor={`${uid}-enabled`} className="cursor-pointer">
                Enabled
              </Label>
            </div>
            {forBot ? (
              <div className="flex items-center gap-2">
                <Switch
                  id={`${uid}-fallback`}
                  checked={fallback}
                  onCheckedChange={setFallback}
                  aria-label="Use when someone has no server of this name"
                />
                <Label htmlFor={`${uid}-fallback`} className="cursor-pointer">
                  Use when someone has none
                </Label>
              </div>
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
    <div className="grid grid-cols-3 gap-1 sm:grid-cols-4">
      {MCP_PRESETS.map((p) => (
        <ProductTile
          key={p.name}
          name={p.name}
          label={p.label}
          testId="mcp-preset"
          onClick={() => onPick(p)}
        />
      ))}
      <ProductTile name="custom" label="Custom" testId="mcp-preset-custom" onClick={() => onPick("custom")} />
    </div>
  );
}

function ProductTile({
  name,
  label,
  testId,
  onClick,
}: {
  name: string;
  label: string;
  testId: string;
  onClick: () => void;
}) {
  return (
    <button
      type="button"
      data-testid={testId}
      onClick={onClick}
      className={cn(
        "flex flex-col items-center gap-2 rounded-lg px-2 py-2.5 text-center",
        "outline-none transition-colors hover:bg-raised",
        "focus-visible:ring-2 focus-visible:ring-ring/50",
      )}
    >
      <McpMark name={name} className="size-10 rounded-xl" />
      <span className="text-xs font-medium text-fg">{label}</span>
    </button>
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
          <DialogTitle>{server.tokenSet ? "Replace" : "Add"} {server.name}&apos;s token</DialogTitle>
          <DialogDescription>
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
