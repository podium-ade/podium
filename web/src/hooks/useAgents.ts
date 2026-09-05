import { useQuery } from "@tanstack/react-query";
import type { AgentBackend } from "../gen/podium/agent/v1/agent_pb";
import { agent } from "../lib/client";

/**
 * useAgents is the picker's catalogue: which backends this conductor runs, which models each
 * offers, and whether a credential for it is stored.
 *
 * It comes from the server rather than from a constant in this bundle because the server is
 * also what validates against it. A list that lived here would drift from the one a save is
 * checked against, and the first anybody would hear of it is a refusal with no explanation.
 *
 * A conductor that is down leaves the list empty. Every screen that uses it still renders:
 * the picker falls back to whatever the skill or profile already holds, so an operator can
 * still read what is set even when nothing can be chosen.
 */
export function useAgents(): { agents: AgentBackend[]; loading: boolean } {
  const q = useQuery({
    queryKey: ["agent", "agents"],
    queryFn: () => agent.listAgents({}),
    // The catalogue changes when a credential is set, which is a different screen. A minute
    // is short enough to notice that and long enough not to refetch on every render.
    staleTime: 60_000,
  });
  return { agents: q.data?.agents ?? [], loading: q.isPending };
}
