import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { admin, errorMessage, isUnauthenticated } from "../lib/client";
import { getToken, onRejected, setToken } from "../lib/auth";

/**
 * TokenGate is the browser half of the dev transport's static-token auth.
 *
 * The server requires `Authorization: Bearer <PODIUM_DEV_TOKEN>` on every Connect endpoint and a
 * freshly loaded page has no token, so the UI probes once with ListNodes: if the call succeeds
 * (which is what happens behind `pnpm dev`, where Vite injects the header) nothing is asked for.
 * Otherwise it asks, stores the answer in localStorage, and re-probes. The token is never baked
 * into the served HTML, and the server's auth is not relaxed for the UI. Step 11's tailnet
 * transport derives identity from WhoIs, and this component goes away with it.
 */
export function TokenGate({ children }: { children: ReactNode }) {
  const [attempt, setAttempt] = useState(0);
  const [forced, setForced] = useState(false);
  const [value, setValue] = useState(getToken());

  const probe = useQuery({
    queryKey: ["auth-probe", attempt],
    queryFn: () => admin.listNodes({}),
    retry: false,
    staleTime: Infinity,
    gcTime: Infinity,
  });

  // Any later 401 — an expired session, or a token changed out from under us — re-gates.
  useEffect(() => onRejected(() => setForced(true)), []);

  if (!forced && probe.isSuccess) return <>{children}</>;

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
          The dev transport authenticates every API call with a shared bearer token. Paste{" "}
          <code className="font-mono text-fg">PODIUM_DEV_TOKEN</code> to continue. It is kept in
          this browser&apos;s local storage and sent as an{" "}
          <code className="font-mono">Authorization</code> header.
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
