import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { Loader2 } from "lucide-react";
import { errorMessage, identity, isUnauthenticated } from "../lib/client";
import { authErrorMessage, fetchAuthStatus, getToken, onRejected, setToken } from "../lib/auth";
import { ViewerContext, viewerFrom, type Viewer } from "../lib/identity";
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
 * localStorage and probe again. When Google Workspace sign-in is configured, /auth/status says
 * so and the gate offers that instead of (or as well as) the token.
 *
 * The probe is deliberately the same authenticated endpoint as everything else. Relaxing the
 * server's auth for a "who am I" endpoint would publish it to anything that can reach the port,
 * and templating the token into the served HTML would hand it to every reader of the page.
 * /auth/status is the one public exception: it names the sign-in methods, not a person.
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

  const status = useQuery({
    queryKey: ["auth-status"],
    queryFn: fetchAuthStatus,
    retry: false,
    staleTime: Infinity,
    gcTime: Infinity,
  });

  // Any later 401 — an expired session, or a token changed out from under us — re-gates.
  useEffect(() => onRejected(() => setForced(true)), []);

  if (!forced && probe.isSuccess) {
    const viewer = viewerFrom(probe.data);
    if (viewer.canClaim) {
      return <ClaimScreen viewer={viewer} onClaimed={() => setAttempt((n) => n + 1)} />;
    }
    return <ViewerContext value={viewer}>{children}</ViewerContext>;
  }

  if (!forced && (probe.isPending || status.isPending)) {
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
  const google = status.data?.google === true;

  return (
    <SignInShell>
      {google ? (
        <GoogleSignIn
          hostedDomain={status.data?.hosted_domain ?? ""}
          claimed={status.data?.claimed === true}
        />
      ) : null}

      {google ? (
        <details className="mt-4">
          <summary className="cursor-pointer text-2xs text-faint hover:text-muted">
            Use a local token
          </summary>
          <TokenForm
            value={value}
            setValue={setValue}
            rejected={rejected}
            failure={failure}
            onSubmit={() => {
              setToken(value);
              setForced(false);
              setAttempt((n) => n + 1);
            }}
          />
        </details>
      ) : (
        <>
          <p className="mb-5 text-center text-xs leading-relaxed text-muted">
            This server is on the <code className="font-mono text-fg">dev</code> transport, which
            authenticates every API call with a shared bearer token.
          </p>
          <TokenForm
            value={value}
            setValue={setValue}
            rejected={rejected}
            failure={failure}
            onSubmit={() => {
              setToken(value);
              setForced(false);
              setAttempt((n) => n + 1);
            }}
          />
        </>
      )}
    </SignInShell>
  );
}

function SignInShell({ children }: { children: ReactNode }) {
  return (
    <div className="relative grid h-full place-items-center overflow-y-auto bg-background p-6">
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
          <h1 className="font-mono text-base font-semibold tracking-tight">
            podium<span className="text-accent">.</span>
          </h1>
        </div>
        {children}
      </div>
    </div>
  );
}

function GoogleSignIn({ hostedDomain, claimed }: { hostedDomain: string; claimed: boolean }) {
  const errorCode = new URLSearchParams(window.location.search).get("auth_error") ?? "";
  return (
    <div className="rounded-xl border border-border bg-card p-5 shadow-sm">
      <p className="text-xs leading-relaxed text-muted">
        {claimed && hostedDomain
          ? `This Podium belongs to the ${hostedDomain} Google Workspace. Sign in with that account.`
          : "Sign in with Google Workspace. The first person to confirm will claim this instance for their domain."}
      </p>
      {errorCode ? (
        <Alert variant="destructive" className="mt-3">
          {authErrorMessage(errorCode)}
        </Alert>
      ) : null}
      <Button asChild className="mt-4 w-full">
        <a href="/auth/google/start">Sign in with Google Workspace</a>
      </Button>
    </div>
  );
}

function TokenForm({
  value,
  setValue,
  rejected,
  failure,
  onSubmit,
}: {
  value: string;
  setValue: (v: string) => void;
  rejected: boolean;
  failure: string | undefined;
  onSubmit: () => void;
}) {
  return (
    <form
      className="rounded-xl border border-border bg-card p-5 shadow-sm"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
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
  );
}

function ClaimScreen({ viewer, onClaimed }: { viewer: Viewer; onClaimed: () => void }) {
  const domain = viewer.claimDomain;
  const [typed, setTyped] = useState("");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string>();
  const match = typed.trim().toLowerCase() === domain.toLowerCase();

  return (
    <SignInShell>
      <form
        className="rounded-xl border border-border bg-card p-5 shadow-sm"
        onSubmit={async (e) => {
          e.preventDefault();
          if (!match || busy) return;
          setBusy(true);
          setErr(undefined);
          try {
            await identity.claim({ hostedDomain: typed.trim() });
            onClaimed();
          } catch (caught) {
            setErr(errorMessage(caught));
            setBusy(false);
          }
        }}
      >
        <h2 className="text-sm font-medium text-fg">Claim this instance</h2>
        <p className="mt-2 text-xs leading-relaxed text-muted">
          Signed in as <code className="font-mono text-fg">{viewer.login}</code>. This Podium has
          no owner. Type <code className="font-mono text-fg">{domain}</code> to claim it for that
          Google Workspace. You will be the owner; later sign-ins from this Workspace will join as
          members.
        </p>
        <Label htmlFor="claim-domain" className="mt-4">
          Workspace domain
        </Label>
        <Input
          id="claim-domain"
          autoFocus
          autoComplete="off"
          placeholder={domain}
          value={typed}
          onChange={(e) => setTyped(e.target.value)}
          className="mt-1.5 font-mono"
        />
        {err ? (
          <Alert variant="destructive" className="mt-3">
            {err}
          </Alert>
        ) : null}
        <Button type="submit" className="mt-4 w-full" disabled={!match || busy}>
          {busy ? "Claiming…" : `Claim for ${domain}`}
        </Button>
      </form>
    </SignInShell>
  );
}
