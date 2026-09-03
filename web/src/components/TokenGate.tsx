import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { errorMessage, identity, isUnauthenticated } from "../lib/client";
import { getToken, onRejected, setToken } from "../lib/auth";
import { ViewerContext, viewerFrom } from "../lib/identity";

/**
 * TokenGate decides whether this deployment needs a credential from the browser at all.
 *
 * It probes WhoAmI once. Under the tailnet transport the call succeeds with no header: Tailscale
 * named the caller at the transport layer, so there is nothing to ask for and the gate never
 * shows — the answer goes straight into the header. Under the dev transport the same probe comes
 * back Unauthenticated, and only then does the UI ask for PODIUM_DEV_TOKEN, keep it in
 * localStorage and probe again.
 *
 * The probe is deliberately the same authenticated endpoint as everything else. Relaxing the
 * server's auth for a "who am I" endpoint would publish it to anything that can reach the port,
 * and templating the token into the served HTML would hand it to every reader of the page.
 */
export function TokenGate({ children }: { children: ReactNode }) {
  const [attempt, setAttempt] = useState(0);
  const [forced, setForced] = useState(false);
  const [value, setValue] = useState(getToken());

  const probe = useQuery({
    queryKey: ["whoami", attempt],
    queryFn: () => identity.whoAmI({}),
    retry: false,
    staleTime: Infinity,
    gcTime: Infinity,
  });

  // Any later 401 — an expired session, or a token changed out from under us — re-gates.
  useEffect(() => onRejected(() => setForced(true)), []);

  if (!forced && probe.isSuccess) {
    return <ViewerContext value={viewerFrom(probe.data)}>{children}</ViewerContext>;
  }

  if (!forced && probe.isPending) {
    return <div className="grid h-full place-items-center text-sm text-muted">connecting…</div>;
  }

  const rejected = isUnauthenticated(probe.error) ? getToken() !== "" : false;
  const failure =
    probe.error && !isUnauthenticated(probe.error) ? errorMessage(probe.error) : undefined;

  return (
    <div className="grid h-full place-items-center p-6">
      <form
        className="w-full max-w-md rounded border border-border bg-panel p-6"
        onSubmit={(e) => {
          e.preventDefault();
          setToken(value);
          setForced(false);
          setAttempt((n) => n + 1);
        }}
      >
        <h1 className="text-lg font-semibold">podium</h1>
        <p className="mt-2 text-sm text-muted">
          This server is on the <code className="font-mono text-fg">dev</code> transport, which
          authenticates every API call with a shared bearer token. Paste{" "}
          <code className="font-mono text-fg">PODIUM_DEV_TOKEN</code> to continue. It is kept in
          this browser&apos;s local storage and sent as an{" "}
          <code className="font-mono">Authorization</code> header.
        </p>
        <p className="mt-2 text-xs text-muted">
          A server on a tailnet never shows this: Tailscale identifies you and there is no token.
        </p>
        {rejected ? <p className="mt-3 text-sm text-err">Token rejected — re-enter it.</p> : null}
        {failure ? <p className="mt-3 text-sm text-err">{failure}</p> : null}
        <label className="mt-4 block text-xs text-muted" htmlFor="dev-token">
          Dev token
        </label>
        <input
          id="dev-token"
          type="password"
          autoFocus
          autoComplete="off"
          value={value}
          onChange={(e) => setValue(e.target.value)}
          className="mt-1 w-full rounded border border-border bg-bg px-2 py-1.5 font-mono text-sm outline-none focus:border-accent"
        />
        <button
          type="submit"
          className="mt-4 w-full rounded bg-accent px-3 py-1.5 text-sm font-medium text-bg hover:opacity-90"
        >
          Connect
        </button>
      </form>
    </div>
  );
}
