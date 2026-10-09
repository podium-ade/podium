import { useState } from "react";
import { GitPullRequest } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { agent, errorMessage } from "../../lib/client";
import { githubCallbackURL, rememberGitHubPending } from "../../lib/github";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../ui/card";

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

  return (
    <Card data-testid="github-account-card">
      <CardHeader>
        <CardTitle>GitHub account</CardTitle>
        <CardDescription>
          Connect your GitHub account so the assistant opens pull requests as you. You do not
          need a personal access token.
        </CardDescription>
      </CardHeader>
      <CardContent className="space-y-4">
        {query.isError ? <Alert variant="destructive">{errorMessage(query.error)}</Alert> : null}
        {account && !account.available && !account.connected ? (
          <p className="text-sm text-muted">
            GitHub is not enabled for your organization yet. Contact an admin.
          </p>
        ) : null}
        {account?.connected ? (
          <div className="flex items-center gap-3">
            <GitPullRequest className="size-5 text-muted" aria-hidden />
            <div className="min-w-0">
              <div className="truncate text-sm font-medium text-fg">@{account.githubLogin}</div>
              {account.name ? <div className="truncate text-xs text-muted">{account.name}</div> : null}
            </div>
          </div>
        ) : null}
        {error ? <Alert variant="destructive">{error}</Alert> : null}
        {account?.connected ? (
          <Button type="button" variant="outline" className="w-full" onClick={() => disconnect.mutate()} disabled={busy}>
            Disconnect GitHub
          </Button>
        ) : account?.available ? (
          <Button type="button" className="w-full" onClick={() => connect.mutate()} disabled={busy}>
            <GitPullRequest />
            Connect GitHub
          </Button>
        ) : null}
      </CardContent>
    </Card>
  );
}
