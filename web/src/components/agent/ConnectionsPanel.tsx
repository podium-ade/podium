import { useState } from "react";
import type { ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { GitHubConnection, SlackConnection } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { Badge } from "../Badge";
import { Empty } from "../Empty";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import { Textarea } from "../ui/textarea";
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

  if (query.isError && !isAgentUnreachable(query.error)) {
    return <Empty title="Could not read connections" hint={errorMessage(query.error)} />;
  }

  return (
    <div className="max-w-2xl space-y-4">
      <p className="text-sm leading-relaxed text-muted">
        GitHub and Slack for this conductor. The GitHub App is saved here. Slack can still
        come from the environment until you save a pair. Either way, the conductor uses a
        saved value the next time it starts.
      </p>
      {isAgentUnreachable(query.error) ? (
        <ConductorDown
          what="Connections could not be read"
          onRetry={() => void query.refetch()}
          retrying={query.isFetching}
        />
      ) : null}
      {query.isPending ? <p className="text-sm text-muted">Reading connections…</p> : null}
      {query.data ? (
        <div className="space-y-4">
          <GitHubCard
            key={`${query.data.github?.source ?? ""}|${query.data.github?.appId ?? ""}|${query.data.github?.webhookListen ?? ""}|${query.data.github?.setAt?.seconds ?? ""}`}
            connection={query.data.github}
          />
          <SlackCard connection={query.data.slack} />
          <LinearCard configured={query.data.linear?.configured === true} />
        </div>
      ) : null}
    </div>
  );
}

function savedSource(source: string | undefined): string {
  return source === "saved" ? "saved" : "";
}

function status(source: string, configured: boolean, restart: boolean): { label: string; tone: "ok" | "warn" | "idle" } {
  if (!configured) return { label: "Not set", tone: "idle" };
  if (restart) return { label: "Restart to apply", tone: "warn" };
  if (source === "environment") return { label: "From environment", tone: "ok" };
  return { label: "Saved", tone: "ok" };
}

function GitHubCard({ connection }: { connection?: GitHubConnection }) {
  const toast = useToast();
  const qc = useQueryClient();
  const gh = connection;
  const [appId, setAppId] = useState(gh?.appId ?? "");
  const [privateKey, setPrivateKey] = useState("");
  const [webhookSecret, setWebhookSecret] = useState("");
  const [listen, setListen] = useState(gh?.webhookListen ?? "");
  const [error, setError] = useState("");

  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "connections"] });
  const save = useMutation({
    mutationFn: () =>
      agent.setGitHubConnection({
        appId: appId.trim(),
        privateKey,
        webhookSecret,
        webhookListen: listen.trim(),
      }),
    onSuccess: async (res) => {
      setPrivateKey("");
      setWebhookSecret("");
      setError("");
      toast(res.github?.restartRequired ? "GitHub saved. Restart the conductor to use it." : "GitHub saved.", "ok");
      await reload();
    },
    onError: (err) => setError(errorMessage(err)),
  });
  const clear = useMutation({
    mutationFn: () => agent.clearGitHubConnection({}),
    onSuccess: async () => {
      setError("");
      toast("Removed the saved GitHub App. It is off the next time the conductor starts.", "ok");
      await reload();
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const badge = status(savedSource(gh?.source), gh?.configured === true, gh?.restartRequired === true);
  const saved = gh?.source === "saved";

  return (
    <section aria-labelledby="connection-github" className="space-y-4 rounded-xl border border-border bg-card p-4 shadow-xs" data-testid="connection-github">
      <header className="flex flex-wrap items-center gap-2">
        <h2 id="connection-github" className="text-sm font-semibold tracking-tight text-fg">
          GitHub
        </h2>
        <Badge tone={badge.tone}>{badge.label}</Badge>
      </header>
      <p className="text-sm leading-relaxed text-muted">
        The App turns use to clone and push. Add the webhook secret and listen address when
        GitHub should send pull request reviews here. Tasks still reach this conductor at{" "}
        <code className="font-mono text-fg">PODIUM_AGENT_TASK_URL</code>.
      </p>
      {gh?.restartRequired ? (
        <Alert variant="warn">Restart the conductor. It is still running with the previous GitHub App.</Alert>
      ) : null}
      <div className="grid gap-3">
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
        <Field
          label="Private key"
          htmlFor="github-private-key"
          hint={
            gh?.privateKeySet
              ? "Leave blank to keep the saved key."
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
        </Field>
        <Field
          label="Webhook secret"
          htmlFor="github-webhook-secret"
          hint={
            gh?.webhookSecretSet
              ? `Saved, ending in ${gh.webhookSecretHint || "••••"}. Leave blank to keep it while a listen address is set.`
              : "Optional. Set it together with the listen address to accept reviews."
          }
        >
          <Input
            id="github-webhook-secret"
            type="password"
            value={webhookSecret}
            onChange={(e) => setWebhookSecret(e.target.value)}
            autoComplete="off"
          />
        </Field>
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
      </div>
      {error ? <Alert variant="destructive">{error}</Alert> : null}
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" onClick={() => save.mutate()} disabled={save.isPending || clear.isPending}>
          Save GitHub
        </Button>
        {saved ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => clear.mutate()}
            disabled={save.isPending || clear.isPending}
          >
            Remove saved GitHub
          </Button>
        ) : null}
      </div>
    </section>
  );
}

function SlackCard({ connection }: { connection?: SlackConnection }) {
  const toast = useToast();
  const qc = useQueryClient();
  const slack = connection;
  const [appToken, setAppToken] = useState("");
  const [botToken, setBotToken] = useState("");
  const [error, setError] = useState("");

  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "connections"] });
  const save = useMutation({
    mutationFn: () => agent.setSlackConnection({ appToken, botToken }),
    onSuccess: async (res) => {
      setAppToken("");
      setBotToken("");
      setError("");
      toast(res.slack?.restartRequired ? "Slack saved. Restart the conductor to use it." : "Slack saved.", "ok");
      await reload();
    },
    onError: (err) => setError(errorMessage(err)),
  });
  const clear = useMutation({
    mutationFn: () => agent.clearSlackConnection({}),
    onSuccess: async () => {
      setError("");
      toast("Removed the saved Slack app. The environment applies on the next start.", "ok");
      await reload();
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const badge = status(slack?.source ?? "", slack?.configured === true, slack?.restartRequired === true);
  const saved = slack?.source === "saved";
  const keep = saved ? "Leave blank to keep the saved token." : "Set in the environment. Paste a token to save it here.";

  return (
    <section aria-labelledby="connection-slack" className="space-y-4 rounded-xl border border-border bg-card p-4 shadow-xs" data-testid="connection-slack">
      <header className="flex flex-wrap items-center gap-2">
        <h2 id="connection-slack" className="text-sm font-semibold tracking-tight text-fg">
          Slack
        </h2>
        <Badge tone={badge.tone}>{badge.label}</Badge>
      </header>
      <p className="text-sm leading-relaxed text-muted">
        Socket Mode needs the app-level token and the bot token. Both are saved together.
      </p>
      {slack?.restartRequired ? (
        <Alert variant="warn">Restart the conductor. It is still running with the previous Slack tokens.</Alert>
      ) : null}
      <div className="grid gap-3">
        <Field
          label="App-level token"
          htmlFor="slack-app-token"
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
        </Field>
        <Field
          label="Bot token"
          htmlFor="slack-bot-token"
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
        </Field>
      </div>
      {error ? <Alert variant="destructive">{error}</Alert> : null}
      <div className="flex flex-wrap gap-2">
        <Button type="button" size="sm" onClick={() => save.mutate()} disabled={save.isPending || clear.isPending}>
          Save Slack
        </Button>
        {saved ? (
          <Button
            type="button"
            size="sm"
            variant="outline"
            onClick={() => clear.mutate()}
            disabled={save.isPending || clear.isPending}
          >
            Remove saved Slack
          </Button>
        ) : null}
      </div>
    </section>
  );
}

function LinearCard({ configured }: { configured: boolean }) {
  return (
    <section
      aria-labelledby="connection-linear"
      className="space-y-2 rounded-xl border border-dashed border-border bg-card/40 p-4"
      data-testid="connection-linear"
    >
      <header className="flex flex-wrap items-center gap-2">
        <h2 id="connection-linear" className="text-sm font-semibold tracking-tight text-fg">
          Linear
        </h2>
        <Badge tone="idle">Coming soon</Badge>
      </header>
      <p className="text-sm leading-relaxed text-muted">
        Issue assignments will be configured here.
        {configured ? " This conductor is still using the Linear key from its environment." : ""}
      </p>
    </section>
  );
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
    <div className="space-y-1.5">
      <Label htmlFor={htmlFor}>{label}</Label>
      {children}
      {hint ? <p className="text-xs leading-relaxed text-muted">{hint}</p> : null}
    </div>
  );
}
