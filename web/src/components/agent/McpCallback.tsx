import { useEffect, useRef, useState } from "react";
import { CheckCircle2, Plug } from "lucide-react";
import { Link, useNavigate, useSearchParams } from "react-router";
import { useMutation } from "@tanstack/react-query";
import { agent, errorMessage } from "../../lib/client";
import { takePending, type PendingMcpOAuth } from "../../lib/mcp";
import { Empty } from "../Empty";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";

/**
 * McpCallback is where an authorization server sends the operator's browser back to.
 *
 * It is a route in this app and not an endpoint on podium-server, which is what keeps the
 * whole flow inside the authenticated API: this page reads the code off its own URL and
 * hands it to the conductor over the ordinary Connect transport. There is no
 * unauthenticated HTTP route anywhere in the sign-in.
 *
 * The code is single-use, and the exchange has to happen exactly once — a second attempt is
 * refused by the authorization server, which would read to an operator as "the sign-in
 * failed" when it had in fact worked. So the effect is guarded by a ref rather than by a
 * dependency list: React's development double-invoke would otherwise spend the code twice.
 */
export function McpCallback() {
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const sent = useRef(false);
  const [pending] = useState<PendingMcpOAuth | undefined>(takePending);

  const code = params.get("code") ?? "";
  const state = params.get("state") ?? "";
  // The authorization server's own way of saying no — a denied consent screen arrives here
  // rather than at the conductor, so it is this page that has to explain it.
  const denied = params.get("error") ?? "";
  const deniedDetail = params.get("error_description") ?? "";

  const complete = useMutation({
    mutationFn: () =>
      agent.completeMcpOAuth({ flowId: pending?.flowId ?? "", code, state }),
  });

  useEffect(() => {
    if (sent.current || denied !== "" || code === "" || !pending?.flowId) return;
    sent.current = true;
    complete.mutate();
  }, [code, denied, pending, complete]);

  const back = (
    <Button type="button" size="sm" onClick={() => void navigate("/agent/mcp", { replace: true })}>
      Back to MCP servers
    </Button>
  );

  if (denied !== "") {
    return (
      <Empty
        icon={Plug}
        title="The sign-in was not granted"
        hint={deniedDetail || denied}
        action={back}
      />
    );
  }

  if (code === "" || !pending?.flowId) {
    // Either a stale tab, or a callback that arrived without the sign-in this browser
    // started. Neither is recoverable from here, and neither is worth a scary word: the
    // sign-in is one button away.
    return (
      <Empty
        icon={Plug}
        title="There is no sign-in in progress"
        hint={
          code === ""
            ? "This page is where an authorization server sends you back to. Nothing was in the address."
            : "This browser has no record of starting that sign-in. Start it again from the MCP screen."
        }
        action={back}
      />
    );
  }

  if (complete.isError) {
    return (
      <div className="mx-auto max-w-2xl space-y-4">
        <Alert variant="destructive" role="alert" title="The sign-in could not be completed">
          {errorMessage(complete.error)}
        </Alert>
        <p className="text-xs leading-relaxed text-muted">
          An authorization code can only be used once, so this cannot be retried from here —
          start the sign-in again.{" "}
          <Link to="/agent/mcp" className="text-accent hover:underline">
            Back to MCP servers
          </Link>
          .
        </p>
      </div>
    );
  }

  if (complete.isSuccess) {
    const server = complete.data.server;
    return (
      <Empty
        icon={CheckCircle2}
        title={`Signed in to ${server?.name ?? pending.name}`}
        hint={
          server?.account
            ? `As ${server.account}. It applies to the next turn of every playbook that names it.`
            : "It applies to the next turn of every playbook that names it."
        }
        action={back}
      />
    );
  }

  return (
    <Empty
      icon={Plug}
      title={`Finishing the sign-in to ${pending.name}…`}
      hint={pending.issuer ? `Exchanging the code with ${pending.issuer}.` : "Exchanging the code."}
    />
  );
}
