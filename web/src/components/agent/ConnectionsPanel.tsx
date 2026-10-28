import { useState } from "react";
import type { ReactNode } from "react";
import { ChevronDown } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type {
  GitHubConnection,
  GitHubWebhookFilters,
  SlackConnection,
} from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { cn } from "../../lib/utils";
import { Badge } from "../Badge";
import { Empty } from "../Empty";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { Checkbox } from "../ui/checkbox";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../ui/table";
import { Textarea } from "../ui/textarea";
import { BrandTile } from "./BrandIcon";
import { ConductorDown } from "./ConductorDown";

/**
 * ConnectionsPanel is where this conductor's GitHub App and Slack app are saved.
 * Linear is shown so the section has a place for it, and it cannot be saved yet.
 * Slack can still come from the environment until a pair is saved. The GitHub App
 * cannot: those environment variables are ignored. A saved value is what the
 * conductor uses the next time it starts.
 */
export function ConnectionsPanel() {
  const query = useQuery({
    queryKey: ["agent", "connections"],
    queryFn: () => agent.getConnections({}),
  });
  const [editing, setEditing] = useState<"github" | "slack" | null>(null);

  if (query.isError && !isAgentUnreachable(query.error)) {
    return <Empty title="Could not read connections" hint={errorMessage(query.error)} />;
  }

  const data = query.data;

  return (
    <div>
      {isAgentUnreachable(query.error) ? (
        <div className="p-4">
          <ConductorDown
            what="Connections could not be read"
            onRetry={() => void query.refetch()}
            retrying={query.isFetching}
          />
        </div>
      ) : null}
      {query.isPending ? <p className="p-4 text-sm text-muted">Reading connections…</p> : null}
      {data ? (
        <Table containerClassName="rounded-none border-0 bg-transparent">
          <caption className="sr-only">Connections</caption>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className={cn(settingsHead, "w-[40%]")}>Connection</TableHead>
              <TableHead className={cn(settingsHead, "w-[20%]")}>Status</TableHead>
              <TableHead className={settingsHead}>
                <span className="sr-only">Actions</span>
              </TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            <ConnectionRow
              name="GitHub"
              mark="github"
              badge={status(savedSource(data.github?.source), data.github?.configured === true, data.github?.restartRequired === true)}
              detail={data.github?.configured ? `App ${data.github.appId}` : "No App saved"}
              onEdit={() => setEditing("github")}
            />
            <ConnectionRow
              name="Slack"
              mark="slack"
              badge={status(data.slack?.source ?? "", data.slack?.configured === true, data.slack?.restartRequired === true)}
              detail={
                data.slack?.source === "saved"
                  ? "Both tokens are saved here"
                  : data.slack?.source === "environment"
                    ? "Using the environment"
                    : "No tokens saved"
              }
              onEdit={() => setEditing("slack")}
            />
            <ConnectionRow
              name="Linear"
              mark="linear"
              badge={{ label: "Coming soon", tone: "idle" }}
              detail={`Issue assignments will be configured here.${
                data.linear?.configured === true
                  ? " This conductor is still using the Linear key from its environment."
                  : ""
              }`}
            />
          </TableBody>
        </Table>
      ) : null}
      {data && editing === "github" ? (
        <GitHubDialog connection={data.github} onClose={() => setEditing(null)} />
      ) : null}
      {data && editing === "slack" ? (
        <SlackDialog connection={data.slack} onClose={() => setEditing(null)} />
      ) : null}
    </div>
  );
}

function savedSource(source: string | undefined): string {
  return source === "saved" ? "saved" : "";
}

function status(
  source: string,
  configured: boolean,
  restart: boolean,
): { label: string; tone: "ok" | "warn" | "idle" } {
  if (!configured) return { label: "Not set", tone: "idle" };
  if (restart) return { label: "Restart to apply", tone: "warn" };
  if (source === "environment") return { label: "From environment", tone: "ok" };
  return { label: "Saved", tone: "ok" };
}

function ConnectionRow({
  name,
  mark,
  badge,
  detail,
  onEdit,
}: {
  name: string;
  mark: string;
  badge: { label: string; tone: "ok" | "warn" | "idle" };
  detail: string;
  onEdit?: () => void;
}) {
  return (
    <TableRow data-testid={`connection-${name.toLowerCase()}`}>
      <TableCell className="px-4 py-4">
        <div className="flex min-w-0 items-center gap-3">
          <BrandTile name={mark} />
          <div className="min-w-0">
            <strong className="block truncate text-sm">{name}</strong>
            <span className="block text-xs text-muted">{detail}</span>
          </div>
        </div>
      </TableCell>
      <TableCell className="px-4 py-4">
        <Badge tone={badge.tone}>{badge.label}</Badge>
      </TableCell>
      <TableCell className="px-4 py-4 text-right whitespace-nowrap">
        {onEdit ? (
          <Button type="button" variant="outline" size="sm" aria-label={`Edit ${name}`} onClick={onEdit}>
            Edit
          </Button>
        ) : null}
      </TableCell>
    </TableRow>
  );
}

/** The design's table header: small capitals, muted, on the frame's 16px inset. */
const settingsHead =
  "px-4 py-3 text-xs font-[750] leading-[normal] tracking-[0.08em] text-muted uppercase";

function GitHubDialog({
  connection,
  onClose,
}: {
  connection?: GitHubConnection;
  onClose: () => void;
}) {
  const toast = useToast();
  const qc = useQueryClient();
  const gh = connection;
  const [appId, setAppId] = useState(gh?.appId ?? "");
  const [privateKey, setPrivateKey] = useState("");
  const [webhookSecret, setWebhookSecret] = useState("");
  const [listen, setListen] = useState(gh?.webhookListen ?? "");
  const [clientId, setClientId] = useState(gh?.clientId ?? "");
  const [clientSecret, setClientSecret] = useState("");
  const [error, setError] = useState("");
  const [replaceKey, setReplaceKey] = useState(false);
  const [replaceWebhook, setReplaceWebhook] = useState(false);
  const [replaceClient, setReplaceClient] = useState(false);
  const [groups, setGroups] = useState({
    app: !(gh?.appId && gh?.privateKeySet),
    webhook: !gh?.webhookSecretSet,
    auth: !(gh?.clientId && gh?.clientSecretSet),
    triggers: false,
    advanced: false,
  });

  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "connections"] });
  const save = useMutation({
    mutationFn: () =>
      agent.setGitHubConnection({
        appId: appId.trim(),
        privateKey,
        webhookSecret,
        webhookListen: listen.trim(),
        clientId: clientId.trim(),
        clientSecret,
      }),
    onSuccess: async (res) => {
      toast(res.github?.restartRequired ? "GitHub saved, but the conductor could not switch to it. Restart the conductor to use it." : "GitHub saved and in use.", res.github?.restartRequired ? "err" : "ok");
      onClose();
      await reload();
    },
    onError: (err) => setError(errorMessage(err)),
  });
  const clear = useMutation({
    mutationFn: () => agent.clearGitHubConnection({}),
    onSuccess: async (res) => {
      toast(res.github?.restartRequired ? "Removed the saved GitHub App. Restart the conductor to turn it off." : "Removed the saved GitHub App. It is off.", res.github?.restartRequired ? "err" : "ok");
      onClose();
      await reload();
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const saved = gh?.source === "saved";
  const busy = save.isPending || clear.isPending;

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="max-h-[calc(100vh-2rem)] max-w-[640px] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-3 text-2xl leading-tight font-bold tracking-normal">
            <BrandTile name="github" size="dialog" />
            GitHub
          </DialogTitle>
          <DialogDescription className="text-base text-soft">
            The App turns use to clone and push. Add the webhook secret and listen address when
            GitHub should send pull request reviews here.
          </DialogDescription>
        </DialogHeader>
        {gh?.restartRequired ? (
          <Alert variant="warn">Restart the conductor. It is still running with the previous GitHub App.</Alert>
        ) : null}
        <div className="grid gap-3">
          <Group
            title="GitHub App"
            summary={gh?.appId && gh?.privateKeySet ? "App ID and private key for clone and push." : "Not set"}
            open={groups.app}
            onToggle={() => setGroups((g) => ({ ...g, app: !g.app }))}
          >
            <Field label="App ID" htmlFor="github-app-id" hint="The number on the GitHub App's settings page.">
              <Input
                id="github-app-id"
                value={appId}
                onChange={(e) => setAppId(e.target.value)}
                inputMode="numeric"
                autoComplete="off"
                placeholder="123456"
              />
            </Field>
            <SecretSlot
              label="Private key"
              set={gh?.privateKeySet === true}
              replacing={replaceKey}
              status="Saved"
              onReplace={() => setReplaceKey(true)}
              onKeep={() => {
                setPrivateKey("");
                setReplaceKey(false);
              }}
              keepLabel="Keep saved key"
              hint={
                gh?.privateKeySet
                  ? "Paste a new key, or keep the saved one."
                  : "The PEM from GitHub's generate-a-private-key button."
              }
            >
              <Textarea
                id="github-private-key"
                value={privateKey}
                onChange={(e) => setPrivateKey(e.target.value)}
                className="min-h-28 font-mono text-xs"
                autoComplete="off"
                spellCheck={false}
                placeholder="-----BEGIN RSA PRIVATE KEY-----"
              />
            </SecretSlot>
          </Group>
          <Group
            title="Webhooks"
            summary={gh?.webhookSecretSet ? "Secret that signs posts from GitHub." : "Not set"}
            open={groups.webhook}
            onToggle={() => setGroups((g) => ({ ...g, webhook: !g.webhook }))}
          >
            <SecretSlot
              label="Webhook secret"
              set={gh?.webhookSecretSet === true}
              replacing={replaceWebhook}
              status={`Saved, ending in ${gh?.webhookSecretHint || "••••"}`}
              onReplace={() => setReplaceWebhook(true)}
              onKeep={() => {
                setWebhookSecret("");
                setReplaceWebhook(false);
              }}
              keepLabel="Keep saved secret"
              hint={
                gh?.webhookSecretSet
                  ? "Paste a new secret, or keep the saved one."
                  : "Optional. Set it with a listen address under Advanced."
              }
            >
              <Input
                id="github-webhook-secret"
                type="password"
                value={webhookSecret}
                onChange={(e) => setWebhookSecret(e.target.value)}
                autoComplete="off"
              />
            </SecretSlot>
          </Group>
          <Group
            title="Authentication"
            summary={gh?.clientId && gh?.clientSecretSet ? "Client ID and secret for connecting GitHub." : "Not set"}
            open={groups.auth}
            onToggle={() => setGroups((g) => ({ ...g, auth: !g.auth }))}
          >
            <Field
              label="Client ID"
              htmlFor="github-client-id"
              hint="Optional. Lets people connect their own GitHub accounts under Settings → Account, so a playbook can open pull requests as them. Clear it to turn that off."
            >
              <Input
                id="github-client-id"
                value={clientId}
                onChange={(e) => setClientId(e.target.value)}
                autoComplete="off"
                placeholder="Iv23li…"
                className="font-mono"
              />
            </Field>
            <SecretSlot
              label="Client secret"
              set={gh?.clientSecretSet === true}
              replacing={replaceClient}
              status="Saved"
              onReplace={() => setReplaceClient(true)}
              onKeep={() => {
                setClientSecret("");
                setReplaceClient(false);
              }}
              keepLabel="Keep saved secret"
              hint={
                gh?.clientSecretSet
                  ? "Paste a new secret, or keep the saved one."
                  : "Generated on the GitHub App's settings page. Required with a client ID."
              }
            >
              <Input
                id="github-client-secret"
                type="password"
                value={clientSecret}
                onChange={(e) => setClientSecret(e.target.value)}
                autoComplete="off"
              />
            </SecretSlot>
          </Group>
          <Group
            title="Triggers"
            summary={triggersSummary(gh?.filters)}
            open={groups.triggers}
            onToggle={() => setGroups((g) => ({ ...g, triggers: !g.triggers }))}
          >
            <TriggersForm filters={gh?.filters} />
          </Group>
          <Group
            title="Advanced"
            summary="Listen address this machine uses for webhook posts."
            open={groups.advanced}
            onToggle={() => setGroups((g) => ({ ...g, advanced: !g.advanced }))}
          >
            <Field
              label="Webhook listen"
              htmlFor="github-webhook-listen"
              hint="host:port this process binds for GitHub. Clear it to turn reviews off."
            >
              <Input
                id="github-webhook-listen"
                value={listen}
                onChange={(e) => setListen(e.target.value)}
                autoComplete="off"
                placeholder="0.0.0.0:8091"
                className="font-mono"
              />
            </Field>
          </Group>
        </div>
        {error ? <Alert variant="destructive">{error}</Alert> : null}
        <div className="flex flex-wrap items-center gap-2">
          {saved ? (
            <Button
              type="button"
              variant="ghost"
              className="mr-auto text-err hover:bg-err-bg"
              onClick={() => clear.mutate()}
              disabled={busy}
            >
              Remove saved GitHub
            </Button>
          ) : null}
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="button" onClick={() => save.mutate()} disabled={busy}>
            Save GitHub
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

const replyModes = [
  {
    value: "bot_threads",
    label: "Threads the App started",
    detail: "A reply under one of the App's review comments needs no mention. A reply in a thread a person started does.",
  },
  {
    value: "any_thread",
    label: "Any thread",
    detail: "A reply in any review thread needs no mention, once the pull request has been reviewed.",
  },
  {
    value: "always",
    label: "Never",
    detail: "Every reply needs a mention or a trigger phrase.",
  },
] as const;

function triggersSummary(f?: GitHubWebhookFilters): string {
  const who = f?.allowedActors.length ? `${f.allowedActors.length} allowed` : "Anyone";
  const phrases = f?.triggerPhrases.length
    ? `@mention or ${f.triggerPhrases.length} phrase${f.triggerPhrases.length === 1 ? "" : "s"}`
    : "@mention";
  return `${who}, ${phrases}.`;
}

/** One entry per line or comma. */
function splitList(text: string): string[] {
  return text
    .split(/[\n,]/)
    .map((v) => v.trim())
    .filter(Boolean);
}

/**
 * TriggersForm saves on its own: the filters apply to the next webhook without a
 * restart, unlike the App above.
 */
function TriggersForm({ filters }: { filters?: GitHubWebhookFilters }) {
  const toast = useToast();
  const qc = useQueryClient();
  const [allowed, setAllowed] = useState((filters?.allowedActors ?? []).join("\n"));
  const [ignored, setIgnored] = useState((filters?.ignoredActors ?? []).join("\n"));
  const [ignoreBots, setIgnoreBots] = useState(filters?.ignoreBots ?? false);
  const [phrases, setPhrases] = useState((filters?.triggerPhrases ?? []).join("\n"));
  const [replyMode, setReplyMode] = useState(filters?.replyMode || "bot_threads");
  const [autoReview, setAutoReview] = useState(filters?.autoReview ?? true);
  const [error, setError] = useState("");

  const save = useMutation({
    mutationFn: () =>
      agent.setGitHubWebhookFilters({
        filters: {
          allowedActors: splitList(allowed),
          ignoredActors: splitList(ignored),
          ignoreBots,
          triggerPhrases: phrases.split("\n").map((v) => v.trim()).filter(Boolean),
          replyMode,
          autoReview,
        },
      }),
    onSuccess: async () => {
      setError("");
      toast("Triggers saved. They apply to the next webhook.", "ok");
      await qc.invalidateQueries({ queryKey: ["agent", "connections"] });
    },
    onError: (err) => setError(errorMessage(err)),
  });

  return (
    <>
      <Field
        label="Allowed people"
        htmlFor="github-allowed-actors"
        hint="GitHub logins, one per line. Only they can start a review. Leave it empty to allow anyone."
      >
        <Textarea
          id="github-allowed-actors"
          value={allowed}
          onChange={(e) => setAllowed(e.target.value)}
          className="min-h-16 font-mono text-sm"
          autoComplete="off"
          spellCheck={false}
          placeholder="octocat"
        />
      </Field>
      <Field
        label="Ignored people"
        htmlFor="github-ignored-actors"
        hint="These logins never start a review, even when they mention the App."
      >
        <Textarea
          id="github-ignored-actors"
          value={ignored}
          onChange={(e) => setIgnored(e.target.value)}
          className="min-h-16 font-mono text-sm"
          autoComplete="off"
          spellCheck={false}
          placeholder="dependabot[bot]"
        />
      </Field>
      <CheckRow
        id="github-ignore-bots"
        checked={ignoreBots}
        onChange={setIgnoreBots}
        label="Ignore bot accounts"
        detail="Drop everything sent by a bot, such as Dependabot or Renovate."
      />
      <Field
        label="Trigger phrases"
        htmlFor="github-trigger-phrases"
        hint="One per line. A top-level comment that contains one starts a review, as an @mention of the App does. Matched as whole words, ignoring case."
      >
        <Textarea
          id="github-trigger-phrases"
          value={phrases}
          onChange={(e) => setPhrases(e.target.value)}
          className="min-h-16 font-mono text-sm"
          autoComplete="off"
          spellCheck={false}
          placeholder="/review"
        />
      </Field>
      <fieldset className="grid gap-1.5">
        <legend className="mb-1.5 text-sm font-medium">Replies without a mention</legend>
        {replyModes.map((m) => (
          <label key={m.value} className="flex cursor-pointer items-start gap-3 py-1.5">
            <input
              type="radio"
              name="github-reply-mode"
              value={m.value}
              checked={replyMode === m.value}
              onChange={() => setReplyMode(m.value)}
              className="mt-1 accent-accent"
            />
            <span className="min-w-0">
              <span className="block text-sm text-fg">{m.label}</span>
              <span className="block text-xs leading-relaxed text-muted">{m.detail}</span>
            </span>
          </label>
        ))}
      </fieldset>
      <CheckRow
        id="github-auto-review"
        checked={autoReview}
        onChange={setAutoReview}
        label="Review new pull requests"
        detail="Start a review when a pull request opens ready, or a draft is marked ready."
      />
      {error ? <Alert variant="destructive">{error}</Alert> : null}
      <Button
        type="button"
        variant="outline"
        className="justify-self-start"
        onClick={() => save.mutate()}
        disabled={save.isPending}
      >
        Save triggers
      </Button>
    </>
  );
}

function CheckRow({
  id,
  checked,
  onChange,
  label,
  detail,
}: {
  id: string;
  checked: boolean;
  onChange: (on: boolean) => void;
  label: string;
  detail: string;
}) {
  return (
    <label htmlFor={id} className="flex cursor-pointer items-start gap-3 py-1.5">
      <Checkbox id={id} checked={checked} onCheckedChange={(v) => onChange(v === true)} className="mt-0.5" />
      <span className="min-w-0">
        <span className="block text-sm text-fg">{label}</span>
        <span className="block text-xs leading-relaxed text-muted">{detail}</span>
      </span>
    </label>
  );
}

function SlackDialog({
  connection,
  onClose,
}: {
  connection?: SlackConnection;
  onClose: () => void;
}) {
  const toast = useToast();
  const qc = useQueryClient();
  const slack = connection;
  const saved = slack?.source === "saved";
  const [appToken, setAppToken] = useState("");
  const [botToken, setBotToken] = useState("");
  const [error, setError] = useState("");
  const [replaceApp, setReplaceApp] = useState(!saved);
  const [replaceBot, setReplaceBot] = useState(!saved);
  const [open, setOpen] = useState(true);

  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "connections"] });
  const save = useMutation({
    mutationFn: () => agent.setSlackConnection({ appToken, botToken }),
    onSuccess: async (res) => {
      toast(res.slack?.restartRequired ? "Slack saved, but the conductor could not switch to it. Restart the conductor to use it." : "Slack saved and in use.", res.slack?.restartRequired ? "err" : "ok");
      onClose();
      await reload();
    },
    onError: (err) => setError(errorMessage(err)),
  });
  const clear = useMutation({
    mutationFn: () => agent.clearSlackConnection({}),
    onSuccess: async (res) => {
      toast(res.slack?.restartRequired ? "Removed the saved Slack app. Restart the conductor to apply it." : "Removed the saved Slack app.", res.slack?.restartRequired ? "err" : "ok");
      onClose();
      await reload();
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const busy = save.isPending || clear.isPending;
  const keep = saved
    ? "Paste a new token, or keep the saved one."
    : "Set in the environment. Paste a token to save it here.";

  return (
    <Dialog open onOpenChange={(next) => { if (!next) onClose(); }}>
      <DialogContent className="max-h-[calc(100vh-2rem)] max-w-[640px] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-3 text-2xl leading-tight font-bold tracking-normal">
            <BrandTile name="slack" size="dialog" />
            Slack
          </DialogTitle>
          <DialogDescription className="text-base text-soft">
            {saved
              ? "Replace one token, or both. A token you leave as saved stays."
              : "Paste both tokens to save them here. Until then, Slack can still come from the environment."}
          </DialogDescription>
        </DialogHeader>
        {slack?.restartRequired ? (
          <Alert variant="warn">Restart the conductor. It is still running with the previous Slack tokens.</Alert>
        ) : null}
        <Group
          title="Tokens"
          summary={saved ? "App-level token and bot token." : "Not set"}
          open={open}
          onToggle={() => setOpen((v) => !v)}
        >
          <SecretSlot
            label="App-level token"
            set={saved}
            replacing={replaceApp}
            status={slack?.appTokenHint ? `Saved. Ending in ${slack.appTokenHint}` : "Saved"}
            onReplace={() => setReplaceApp(true)}
            onKeep={() => {
              setAppToken("");
              setReplaceApp(false);
            }}
            keepLabel="Keep saved token"
            hint={slack?.appTokenHint ? `${keep} Ending in ${slack.appTokenHint}.` : "Starts with xapp-. Scope connections:write."}
          >
            <Input
              id="slack-app-token"
              type="password"
              value={appToken}
              onChange={(e) => setAppToken(e.target.value)}
              autoComplete="off"
              placeholder="xapp-"
            />
          </SecretSlot>
          <SecretSlot
            label="Bot token"
            set={saved}
            replacing={replaceBot}
            status={slack?.botTokenHint ? `Saved. Ending in ${slack.botTokenHint}` : "Saved"}
            onReplace={() => setReplaceBot(true)}
            onKeep={() => {
              setBotToken("");
              setReplaceBot(false);
            }}
            keepLabel="Keep saved token"
            hint={slack?.botTokenHint ? `${keep} Ending in ${slack.botTokenHint}.` : "Starts with xoxb-."}
          >
            <Input
              id="slack-bot-token"
              type="password"
              value={botToken}
              onChange={(e) => setBotToken(e.target.value)}
              autoComplete="off"
              placeholder="xoxb-"
            />
          </SecretSlot>
        </Group>
        {error ? <Alert variant="destructive">{error}</Alert> : null}
        <div className="flex flex-wrap items-center gap-2">
          {saved ? (
            <Button
              type="button"
              variant="ghost"
              className="mr-auto text-err hover:bg-err-bg"
              onClick={() => clear.mutate()}
              disabled={busy}
            >
              Remove saved Slack
            </Button>
          ) : null}
          <Button type="button" variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button type="button" onClick={() => save.mutate()} disabled={busy}>
            Save Slack
          </Button>
        </div>
      </DialogContent>
    </Dialog>
  );
}

function Group({
  title,
  summary,
  open,
  onToggle,
  children,
}: {
  title: string;
  summary: string;
  open: boolean;
  onToggle: () => void;
  children: ReactNode;
}) {
  return (
    <section className="overflow-hidden rounded-md border border-border bg-panel">
      <button
        type="button"
        className="flex min-h-11 w-full items-center justify-between gap-3 px-4 py-3 text-left"
        aria-expanded={open}
        onClick={onToggle}
      >
        <span className="grid min-w-0 gap-0.5">
          <strong className="text-sm">{title}</strong>
          {open ? null : <span className="text-sm font-normal text-muted">{summary}</span>}
        </span>
        <ChevronDown className={cn("size-4 shrink-0 text-muted", open ? "" : "-rotate-90")} />
      </button>
      {open ? <div className="grid gap-3 px-4 pb-4">{children}</div> : null}
    </section>
  );
}

function SecretSlot({
  label,
  set,
  replacing,
  status,
  onReplace,
  onKeep,
  keepLabel,
  hint,
  children,
}: {
  label: string;
  set: boolean;
  replacing: boolean;
  status: string;
  onReplace: () => void;
  onKeep: () => void;
  keepLabel: string;
  hint?: string;
  children: ReactNode;
}) {
  if (set && !replacing) {
    return (
      <div className="flex items-center gap-2">
        <div className="flex min-h-11 min-w-0 flex-1 items-center justify-between gap-3 rounded-sm border border-border bg-raised px-3">
          <strong className="text-sm">{label}</strong>
          <span className="min-w-0 text-sm text-muted">{status}</span>
        </div>
        <Button type="button" variant="outline" size="sm" aria-label={`Replace ${label.toLowerCase()}`} onClick={onReplace}>
          Replace
        </Button>
      </div>
    );
  }
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={fieldId(children)}>{label}</Label>
      {children}
      {hint ? <p className="text-xs leading-relaxed text-muted">{hint}</p> : null}
      {set ? (
        <Button type="button" variant="ghost" size="sm" className="justify-self-start" onClick={onKeep}>
          {keepLabel}
        </Button>
      ) : null}
    </div>
  );
}

function fieldId(children: ReactNode): string | undefined {
  if (!children || typeof children !== "object" || !("props" in children)) return undefined;
  const id = (children as { props?: { id?: string } }).props?.id;
  return id;
}

function Field({
  label,
  htmlFor,
  hint,
  children,
}: {
  label: string;
  htmlFor: string;
  hint?: string;
  children: ReactNode;
}) {
  return (
    <div className="grid gap-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint ? <p className="text-xs leading-relaxed text-muted">{hint}</p> : null}
    </div>
  );
}
