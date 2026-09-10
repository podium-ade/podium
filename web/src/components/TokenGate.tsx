import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { errorMessage, identity, isUnauthenticated } from "../lib/client";
import { getToken, onRejected, setToken } from "../lib/auth";
import { ViewerContext, viewerFrom } from "../lib/identity";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

/**
 * TokenGate decides whether this deployment needs a credential from the browser at all.
 *
 * It probes WhoAmI once. Under the tailnet transport the call succeeds with no header: Tailscale
 * named the caller at the transport layer, so there is nothing to ask for and the gate never
 * shows — the answer goes straight into the header. Under the local transport the same probe comes
 * back Unauthenticated, and only then does the UI ask for PODIUM_LOCAL_TOKEN, keep it in
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
    return (
      <div className="grid h-full place-items-center bg-background">
        <div className="flex items-center gap-2 text-sm text-muted">
          <Loader2 className="size-4 animate-spin" />
          Connecting…
        </div>
      </div>
    );
  }

  const rejected = isUnauthenticated(probe.error) ? getToken() !== "" : false;
  const failure =
    probe.error && !isUnauthenticated(probe.error) ? errorMessage(probe.error) : undefined;

  return (
    <div className="relative grid h-full place-items-center overflow-y-auto bg-background p-6">
      {/* A single soft wash keeps the sign-in from reading as an error page. */}
      <div
        aria-hidden
        className="pointer-events-none absolute inset-x-0 top-0 h-72 bg-[radial-gradient(60%_100%_at_50%_0%,var(--color-accent),transparent)] opacity-[0.07]"
      />
      <div className="relative w-full max-w-sm">
        <div className="mb-7 flex flex-col items-center gap-3 text-center">
          <span
            aria-hidden
            className="grid size-10 place-items-center rounded-xl bg-accent/12 text-accent ring-1 ring-accent/25"
          >
            <svg
              viewBox="0 0 16 16"
              className="size-5"
              fill="none"
              stroke="currentColor"
              strokeWidth="1.75"
              strokeLinecap="round"
            >
              <path d="M3 12.5V9m5 3.5V4m5 8.5V6.5" />
            </svg>
          </span>
          <div className="space-y-1">
            <h1 className="font-mono text-base font-semibold tracking-tight">
              podium<span className="text-accent">.</span>
            </h1>
            <p className="text-xs leading-relaxed text-muted">
              This server is on the <code className="font-mono text-fg">dev</code> transport, which
              authenticates every API call with a shared bearer token.
            </p>
          </div>
        </div>

        <form
          className="rounded-xl border border-border bg-card p-5 shadow-sm"
          onSubmit={(e) => {
            e.preventDefault();
            setToken(value);
            setForced(false);
            setAttempt((n) => n + 1);
          }}
        >
          <Label htmlFor="dev-token">Dev token</Label>
          <Input
            id="dev-token"
            type="password"
            autoFocus
            autoComplete="off"
            placeholder="PODIUM_LOCAL_TOKEN"
            aria-invalid={rejected || undefined}
            value={value}
            onChange={(e) => setValue(e.target.value)}
            className="mt-1.5 font-mono"
          />

          {rejected ? (
            <Alert variant="destructive" className="mt-3">
              Token rejected — re-enter it.
            </Alert>
          ) : null}
          {failure ? (
            <Alert variant="destructive" className="mt-3">
              {failure}
            </Alert>
          ) : null}

          <Button type="submit" className="mt-4 w-full">
            Connect
          </Button>

          <p className="mt-4 text-2xs leading-relaxed text-faint">
            Kept in this browser&apos;s local storage and sent as an{" "}
            <code className="font-mono">Authorization</code> header. A server on a tailnet never
            shows this: Tailscale identifies you and there is no token.
          </p>
        </form>
      </div>
    </div>
  );
}
