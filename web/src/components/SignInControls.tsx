import { useState } from "react";
import type { ReactNode } from "react";
import { errorMessage, identity } from "../lib/client";
import { authErrorMessage } from "../lib/auth";
import type { Viewer } from "../lib/identity";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import { Input } from "./ui/input";
import { Label } from "./ui/label";

export function SignInShell({ children }: { children: ReactNode }) {
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

export function GoogleSignIn({ claimed }: { claimed: boolean }) {
  const errorCode = new URLSearchParams(window.location.search).get("auth_error") ?? "";
  return (
    <div className="rounded-xl border border-border bg-card p-5 shadow-sm">
      {claimed ? (
        <p className="text-center text-xs leading-relaxed text-muted">
          This instance of Podium is claimed.
          <br />
          <br />
          Sign in with a Google Workspace account from the organization that owns it.
        </p>
      ) : (
        <p className="text-xs leading-relaxed text-muted">
          Sign in with Google Workspace. The first person to confirm will claim this instance for
          their domain.
        </p>
      )}
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

export function TokenForm({
  value,
  setValue,
  rejected,
  failure,
  onSubmit,
  id = "dev-token",
}: {
  value: string;
  setValue: (v: string) => void;
  rejected: boolean;
  failure: string | undefined;
  onSubmit: () => void;
  id?: string;
}) {
  return (
    <form
      className="rounded-xl border border-border bg-card p-5 shadow-sm"
      onSubmit={(e) => {
        e.preventDefault();
        onSubmit();
      }}
    >
      <Label htmlFor={id}>Dev token</Label>
      <Input
        id={id}
        type="password"
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

export function ClaimScreen({ viewer, onClaimed }: { viewer: Viewer; onClaimed: () => void }) {
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
