import { useState } from "react";
import { Link } from "react-router";
import { useQuery } from "@tanstack/react-query";
import { X } from "lucide-react";
import { IdentityKind } from "../../gen/podium/v1/identity_pb";
import { agent } from "../../lib/client";
import { useViewer } from "../../lib/identity";
import { addMcpHref, hasPersonalLinear } from "../../lib/mcp";
import { cn } from "../../lib/utils";

/** LINEAR_MCP_HINT_KEY remembers, in this browser, that the viewer put the hint away. */
export const LINEAR_MCP_HINT_KEY = "podium.linearMcpHint.dismissed";

function dismissed(): boolean {
  try {
    return localStorage.getItem(LINEAR_MCP_HINT_KEY) === "1";
  } catch {
    return false;
  }
}

/**
 * LinearMcpHint is one quiet line offering a person their own Linear MCP server, where Linear is
 * in front of them: the Issues view, while Linear is among its trackers, and a ticket's
 * conversation. With it the assistant reads and writes Linear in a chat as them, with their permissions; without it, it cannot touch Linear
 * beyond what the app itself posts. It is shown only to a signed-in person who has no personal
 * Linear server switched on, until they connect one or put the line away.
 */
export function LinearMcpHint({ className }: { className?: string }) {
  const viewer = useViewer();
  const person = viewer?.kind === IdentityKind.USER;
  const [hidden, setHidden] = useState(dismissed);
  const servers = useQuery({
    queryKey: ["agent", "mcp"],
    queryFn: () => agent.listMcpServers({}),
    enabled: person && !hidden,
    staleTime: 60_000,
  });

  if (!person || hidden || !servers.isSuccess || hasPersonalLinear(servers.data.servers)) return null;

  return (
    <p
      data-testid="linear-mcp-hint"
      className={cn("flex items-center gap-2 text-2xs text-faint", className)}
    >
      <span className="min-w-0">
        <Link to={addMcpHref("linear")} className="text-accent hover:underline">
          Connect your Linear MCP server
        </Link>{" "}
        so the assistant can read and update Linear as you from a chat.
      </span>
      <button
        type="button"
        aria-label="Dismiss"
        data-testid="linear-mcp-hint-dismiss"
        onClick={() => {
          try {
            localStorage.setItem(LINEAR_MCP_HINT_KEY, "1");
          } catch {
            // Private windows refuse storage: the line goes away for this visit only.
          }
          setHidden(true);
        }}
        className="shrink-0 rounded-sm p-0.5 text-faint hover:bg-raised hover:text-muted"
      >
        <X aria-hidden className="size-3" />
      </button>
    </p>
  );
}
