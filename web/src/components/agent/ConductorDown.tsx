import { RotateCw } from "lucide-react";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";

/**
 * ConductorDown is what every agent screen shows when podium-agent does not answer.
 *
 * It is deliberately not an error page. The conductor is restarted whenever a playbook or a
 * profile changes, and for those few seconds every read from it fails; an operator who is
 * shown a red failure learns to distrust the screen rather than the process. So the headline
 * names what could not be read, the body says the wait is expected, and there is a retry to
 * press instead of a reload.
 */
export function ConductorDown({
  what,
  onRetry,
  retrying,
}: {
  /** The sentence that names what failed, e.g. "The profile could not be read". */
  what: string;
  onRetry?: () => void;
  retrying?: boolean;
}) {
  return (
    <Alert variant="warn" title={what} className="max-w-3xl">
      <p>
        podium-agent is not reachable. That is the normal state for a few seconds after the
        conductor restarts, and it clears on its own. If it does not, check its{" "}
        <code className="font-mono">/readyz</code> on{" "}
        <code className="font-mono">PODIUM_AGENT_LISTEN</code> and the podium-agent logs.
      </p>
      {onRetry ? (
        <Button
          variant="outline"
          size="xs"
          className="mt-2.5"
          onClick={onRetry}
          disabled={retrying}
        >
          <RotateCw className={retrying ? "animate-spin" : undefined} />
          {retrying ? "Retrying…" : "Retry"}
        </Button>
      ) : null}
    </Alert>
  );
}
