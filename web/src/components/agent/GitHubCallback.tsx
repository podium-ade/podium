import { useEffect, useRef, useState } from "react";
import { CheckCircle2, GitPullRequest } from "lucide-react";
import { useNavigate, useSearchParams } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { agent, errorMessage } from "../../lib/client";
import { takeGitHubPending, type PendingGitHubOAuth } from "../../lib/github";
import { Empty } from "../Empty";

/**
 * GitHubCallback is where GitHub sends the browser back after a person connects their
 * account. Like McpCallback, it hands the code to the conductor over the authenticated API,
 * and a ref keeps React's double effect from spending the single-use code twice.
 */
export function GitHubCallback() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const sent = useRef(false);
  const [pending] = useState<PendingGitHubOAuth | undefined>(takeGitHubPending);

  const code = params.get("code") ?? "";
  const state = params.get("state") ?? "";
  const denied = params.get("error_description") ?? params.get("error") ?? "";

  const complete = useMutation({
    mutationFn: () => agent.completeGitHubOAuth({ flowId: pending?.flowId ?? "", code, state }),
  });

  useEffect(() => {
    if (sent.current || denied !== "" || code === "" || !pending?.flowId) return;
    sent.current = true;
    complete.mutate();
  }, [code, denied, pending, complete]);

  const back = (
    <button
      type="button"
      className="text-sm text-accent hover:underline"
      onClick={() => void navigate("/agent/settings/account", { replace: true })}
    >
      Back to Account
    </button>
  );

  if (denied !== "") {
    return <Empty icon={GitPullRequest} title="GitHub was not connected" hint={denied} action={back} />;
  }
  if (code === "" || !pending?.flowId) {
    return (
      <Empty
        icon={GitPullRequest}
        title="There is no GitHub connection in progress"
        hint="Start it again from Settings → Account."
        action={back}
      />
    );
  }
  if (complete.isError) {
    return (
      <Empty
        icon={GitPullRequest}
        title="GitHub could not be connected"
        hint={`${errorMessage(complete.error)} Start it again from Settings → Account.`}
        action={back}
      />
    );
  }
  if (complete.isSuccess) {
    return (
      <Empty
        icon={CheckCircle2}
        title={`Connected as @${complete.data.account?.githubLogin ?? ""}`}
        hint="Playbooks that use your GitHub account now open pull requests as you."
        action={back}
      />
    );
  }
  return <Empty icon={GitPullRequest} title="Connecting GitHub…" hint="Exchanging the code with GitHub." />;
}
