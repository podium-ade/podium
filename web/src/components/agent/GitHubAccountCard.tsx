import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { agent, errorMessage } from "../../lib/client";
import { githubCallbackURL, rememberGitHubPending } from "../../lib/github";
import { Badge } from "../Badge";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";

/**
 * GitHubAccountCard connects the signed-in person's own GitHub account, so a playbook that
 * names github.token opens pull requests as them.
 */
export function GitHubAccountCard() {
  const qc = useQueryClient();
  const [error, setError] = useState("");
  const query = useQuery({
    queryKey: ["agent", "github-account"],
    queryFn: () => agent.getGitHubAccount({}),
  });

  const connect = useMutation({
    mutationFn: () => agent.startGitHubOAuth({ redirectUri: githubCallbackURL() }),
    onSuccess: (res) => {
      rememberGitHubPending({ flowId: res.flowId, state: res.state });
      window.location.assign(res.authorizeUrl);
    },
    onError: (err) => setError(errorMessage(err)),
  });
  const disconnect = useMutation({
    mutationFn: () => agent.disconnectGitHubAccount({}),
    onSuccess: async () => {
      setError("");
      await qc.invalidateQueries({ queryKey: ["agent", "github-account"] });
    },
    onError: (err) => setError(errorMessage(err)),
  });

  const account = query.data?.account;
  const busy = connect.isPending || disconnect.isPending;
  const connected = account?.connected === true;
  const badge = account?.needsReconnect
    ? { tone: "warn" as const, label: "Reconnect" }
    : connected
      ? { tone: "ok" as const, label: "Connected" }
      : { tone: "idle" as const, label: "Not connected" };

  return (
    <article
      data-testid="github-account-card"
      className="grid gap-3 rounded-md border border-border bg-panel p-4"
    >
      <header className="flex flex-wrap items-center gap-2">
        <h2 className="text-base font-bold">GitHub account</h2>
        <Badge tone={badge.tone} dot={false}>
          {badge.label}
        </Badge>
      </header>
      <p className="max-w-[68ch] text-sm text-soft">
        Connect your GitHub account so the assistant opens pull requests as you.
      </p>
      {query.isError ? <Alert variant="destructive">{errorMessage(query.error)}</Alert> : null}
      {account?.needsReconnect ? (
        <Alert variant="warn" title="Your GitHub connection has expired">
          Reconnect it to keep opening pull requests as you.
        </Alert>
      ) : null}
      {connected ? (
        <div className="grid min-w-0">
          <strong className="truncate text-sm">@{account?.githubLogin}</strong>
          {account?.name ? <span className="truncate text-xs text-muted">{account.name}</span> : null}
        </div>
      ) : null}
      {account && !account.available && !account.connected ? (
        <p className="text-sm text-soft">
          GitHub is not enabled for your organization yet. Contact an admin.
        </p>
      ) : null}
      {error ? <Alert variant="destructive">{error}</Alert> : null}
      <div className="flex flex-wrap items-center gap-2">
        {account?.needsReconnect && account.available ? (
          <Button type="button" onClick={() => connect.mutate()} disabled={busy}>
            Reconnect GitHub
          </Button>
        ) : null}
        {connected ? (
          <Button type="button" variant="outline" onClick={() => disconnect.mutate()} disabled={busy}>
            Disconnect GitHub
          </Button>
        ) : account?.available ? (
          <Button type="button" onClick={() => connect.mutate()} disabled={busy}>
            Connect GitHub
          </Button>
        ) : null}
      </div>
    </article>
  );
}
