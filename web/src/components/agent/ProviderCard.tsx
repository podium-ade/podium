import { useEffect, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  ProviderKeyErrorSchema,
  type PollProviderOAuthResponse,
  type ProviderSettings,
  type SetProviderKeyResponse,
  type StartProviderOAuthResponse,
} from "../../gen/podium/agent/v1/agent_pb";
import type { Provider } from "../../lib/agents";
import { errorMessage, isAgentUnreachable } from "../../lib/client";
import { relative } from "../../lib/format";
import { Badge, Chip } from "../Badge";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { BackendMark } from "./BackendMark";

/** How many model ids the confirmation card shows before it stops. */
const MODEL_CHIPS = 6;

type Result =
  | { tone: "ok"; text: string; models: string[]; note?: string }
  | { tone: "err" | "warn"; text: string; detail?: { label: string; text: string } };

/** Signing is the device-code flow as this card sees it. */
type Signing = {
  flowID: string;
  userCode: string;
  url: string;
  urlComplete: string;
  expiresAt: number;
  interval: number;
};

export type ProviderCardProps = {
  provider: Provider;
  settings?: ProviderSettings;
  loading?: boolean;
  /** The RPCs, injected: the card itself talks to nothing. */
  onSave: (key: string) => Promise<SetProviderKeyResponse>;
  onClear: () => Promise<void>;
  /** Present only for a provider whose descriptor offers a subscription. */
  onStartOAuth?: () => Promise<StartProviderOAuthResponse>;
  onPollOAuth?: (flowID: string) => Promise<PollProviderOAuthResponse>;
  /** Called when a sign-in stored a credential, so the page can re-read the settings. */
  onSignedIn?: () => void;
};

/**
 * ProviderCard is one provider's row of the settings tab: paste a key or sign in, see it
 * validated, see it stored.
 *
 * The card is presentational — every call is a prop — and it is deliberately talkative about
 * what happened to the credential, because "saved" has to mean "agents will run". A refusal,
 * a provider that could not be reached and a conductor that is down are three different
 * sentences and three different colours.
 */
export function ProviderCard({
  provider,
  settings,
  loading,
  onSave,
  onClear,
  onStartOAuth,
  onPollOAuth,
  onSignedIn,
}: ProviderCardProps) {
  const toast = useToast();
  const [mode, setMode] = useState<"key" | "subscription">("key");
  const [value, setValue] = useState("");
  const [reveal, setReveal] = useState(false);
  const [saving, setSaving] = useState(false);
  const [result, setResult] = useState<Result>();
  const [confirming, setConfirming] = useState(false);
  const [clearing, setClearing] = useState(false);
  const [signing, setSigning] = useState<Signing>();
  const [starting, setStarting] = useState(false);
  const cancelRef = useRef<HTMLButtonElement>(null);

  // The confirm takes focus when it opens, so the destructive path is where the keyboard is.
  useEffect(() => {
    if (confirming) cancelRef.current?.focus();
  }, [confirming]);

  const keySet = settings?.keySet ?? false;
  const hint = settings?.keyHint ?? "";
  const oauth = provider.subscription !== undefined && onStartOAuth !== undefined;
  const tid = (name: string) => `${name}-${provider.id}`;

  // The polling loop. It lives in an effect so that leaving the tab, cancelling, or the
  // component unmounting all stop it — a poll that outlived its card would keep asking the
  // provider about a sign-in nobody is waiting for.
  useEffect(() => {
    if (!signing || !onPollOAuth) return;
    let live = true;
    let timer: ReturnType<typeof setTimeout>;

    const tick = async () => {
      if (!live) return;
      try {
        const res = await onPollOAuth(signing.flowID);
        if (!live) return;
        switch (res.state) {
          case "done":
            setSigning(undefined);
            setResult({
              tone: "ok",
              text: res.provider?.account
                ? `Signed in as ${res.provider.account}. ${provider.models} turns will run.`
                : `Signed in. ${provider.models} turns will run.`,
              models: [],
            });
            onSignedIn?.();
            return;
          case "denied":
          case "expired":
            setSigning(undefined);
            setResult({
              tone: res.state === "denied" ? "err" : "warn",
              text:
                res.state === "denied"
                  ? "The sign-in was refused."
                  : "The code expired before it was approved.",
              detail: res.detail ? { label: `${provider.name} said`, text: res.detail } : undefined,
            });
            return;
          default: {
            // pending and slow_down are the same instruction with a different delay, and
            // the server is what decides which.
            const wait = Math.max(1, res.interval || signing.interval) * 1000;
            timer = setTimeout(() => void tick(), wait);
          }
        }
      } catch (err) {
        if (!live) return;
        setSigning(undefined);
        setResult(failure(err, provider));
      }
    };

    timer = setTimeout(() => void tick(), signing.interval * 1000);
    return () => {
      live = false;
      clearTimeout(timer);
    };
  }, [signing, onPollOAuth, onSignedIn, provider]);

  async function save() {
    const key = value.trim();
    if (!key || saving) return;
    setSaving(true);
    setResult(undefined);
    try {
      const res = await onSave(key);
      const saved = res.provider?.keyHint ?? "";
      setValue("");
      setReveal(false);
      setResult({
        tone: "ok",
        text: `Saved. ••••${saved} works — ${res.models.length} ${
          res.models.length === 1 ? "model" : "models"
        } visible.`,
        models: res.models,
        note: res.status || undefined,
      });
    } catch (err) {
      setResult(failure(err, provider));
    } finally {
      setSaving(false);
    }
  }

  async function startSignIn() {
    if (!onStartOAuth || starting) return;
    setStarting(true);
    setResult(undefined);
    try {
      const res = await onStartOAuth();
      setSigning({
        flowID: res.flowId,
        userCode: res.userCode,
        url: res.verificationUri,
        urlComplete: res.verificationUriComplete,
        expiresAt: Number(res.expiresAt?.seconds ?? 0) * 1000,
        interval: Math.max(1, res.interval || 5),
      });
    } catch (err) {
      setResult(failure(err, provider));
    } finally {
      setStarting(false);
    }
  }

  async function clear() {
    setClearing(true);
    try {
      await onClear();
      setConfirming(false);
      setResult(undefined);
      setValue("");
      toast(`The ${provider.name} credential was removed.`, "ok");
    } catch (err) {
      toast(errorMessage(err));
    } finally {
      setClearing(false);
    }
  }

  return (
    <section data-testid={tid("provider-card")} className="rounded-xl border border-border bg-card shadow-xs">
      <header className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3">
        <BackendMark id={provider.backendID} />
        <h2 className="text-sm font-semibold">{provider.name}</h2>
        <span className="text-xs text-muted">runs {provider.models}</span>
        {loading ? (
          <Skeleton className="h-5 w-20" />
        ) : (
          <Badge tone={keySet ? "ok" : "idle"}>{keySet ? "Connected" : "Not set"}</Badge>
        )}
        {keySet ? (
          <p className="ml-auto font-mono text-xs text-muted" data-testid={tid("provider-key-meta")}>
            <Connected settings={settings} />
          </p>
        ) : null}
      </header>

      <div className="space-y-3 px-4 py-4">
        {loading ? (
          <div className="space-y-3" aria-busy="true" aria-label="Loading">
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-8 w-full" />
            <Skeleton className="h-8 w-36" />
          </div>
        ) : (
          <>
            {keySet ? null : (
              <p className="max-w-2xl text-xs text-muted">
                Agents use this credential to talk to {provider.models}. It is encrypted at rest
                by podium-server and handed only to the containers that run {provider.models}{" "}
                turns. It leaves this host only to reach {provider.name}.
              </p>
            )}

            {oauth ? (
              <div
                role="tablist"
                aria-label={`${provider.name}: how to connect`}
                className="inline-flex overflow-hidden rounded border border-border"
              >
                {(["key", "subscription"] as const).map((m) => (
                  <button
                    key={m}
                    type="button"
                    role="tab"
                    aria-selected={mode === m}
                    data-testid={tid(`provider-mode-${m}`)}
                    onClick={() => setMode(m)}
                    className={`px-3 py-1 text-xs ${
                      mode === m ? "bg-accent text-bg" : "text-muted hover:text-fg"
                    }`}
                  >
                    {m === "key" ? "API key" : "Subscription"}
                  </button>
                ))}
              </div>
            ) : null}

            {oauth && mode === "subscription" ? (
              <SubscriptionPanel
                provider={provider}
                signing={signing}
                starting={starting}
                onStart={() => void startSignIn()}
                onCancel={() => setSigning(undefined)}
                tid={tid}
              />
            ) : (
              <form
                className="flex flex-wrap items-end gap-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  void save();
                }}
              >
                <div className="min-w-64 flex-1">
                  <label className="block text-xs text-muted" htmlFor={tid("provider-key")}>
                    API key
                  </label>
                  <div className="mt-1 flex items-center gap-1">
                    <input
                      id={tid("provider-key")}
                      data-testid={tid("provider-key-input")}
                      type={reveal ? "text" : "password"}
                      autoComplete="off"
                      spellCheck={false}
                      placeholder={
                        keySet
                          ? hint
                            ? `Paste a new key to replace ••••${hint}`
                            : "Paste a key to replace the one that is set"
                          : provider.keyPlaceholder
                      }
                      value={value}
                      // People copy out of a .env file, so the quotes and the whitespace
                      // come with it. Trimming on the way in is kinder than an error.
                      onChange={(e) => setValue(trimPasted(e.target.value))}
                      className="w-full rounded border border-border bg-bg px-2 py-1.5 font-mono text-sm outline-none focus:border-accent focus-visible:ring-1 focus-visible:ring-accent"
                    />
                    <button
                      type="button"
                      aria-pressed={reveal}
                      aria-label={reveal ? "Hide the key" : "Show the key"}
                      onClick={() => setReveal((v) => !v)}
                      className="rounded border border-border px-2 py-1.5 text-xs text-muted hover:text-fg focus-visible:ring-1 focus-visible:ring-accent"
                    >
                      {reveal ? "Hide" : "Show"}
                    </button>
                  </div>
                  <p className="mt-1 text-xs text-muted">
                    From{" "}
                    <a
                      href={provider.consoleURL}
                      target="_blank"
                      rel="noreferrer noopener"
                      className="text-accent hover:underline"
                    >
                      {new URL(provider.consoleURL).host}
                    </a>
                    .
                  </p>
                </div>
                <button
                  type="submit"
                  data-testid={tid("provider-key-save")}
                  disabled={value.trim() === "" || saving}
                  className="flex items-center gap-2 rounded bg-accent px-3 py-1.5 text-sm font-medium text-bg hover:opacity-90 disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-accent"
                >
                  {saving ? <Spinner /> : null}
                  {saving ? `Checking with ${provider.name}…` : "Validate & save"}
                </button>
              </form>
            )}

            {result ? (
              <div
                data-testid={tid("provider-key-status")}
                role="status"
                aria-live="polite"
                className={`text-xs ${
                  result.tone === "ok"
                    ? "text-ok"
                    : result.tone === "err"
                      ? "text-err"
                      : "text-warn"
                }`}
              >
                <p>{result.text}</p>
                {result.tone !== "ok" && result.detail ? (
                  <p
                    data-testid={tid("provider-key-detail")}
                    className="mt-1.5 max-w-2xl break-words rounded border border-border bg-raised px-2 py-1.5 text-xs text-muted"
                  >
                    <span className="font-semibold">{result.detail.label}:</span>{" "}
                    {result.detail.text}
                  </p>
                ) : null}
                {result.tone === "ok" && result.note ? (
                  <p className="mt-1 text-warn">{result.note}</p>
                ) : null}
                {result.tone === "ok" && result.models.length > 0 ? (
                  <p className="mt-2 flex flex-wrap gap-1">
                    {result.models.slice(0, MODEL_CHIPS).map((m) => (
                      <Chip key={m}>{m}</Chip>
                    ))}
                    {result.models.length > MODEL_CHIPS ? (
                      <Chip>+{result.models.length - MODEL_CHIPS} more</Chip>
                    ) : null}
                  </p>
                ) : null}
              </div>
            ) : null}
          </>
        )}
      </div>

      {keySet && !loading ? (
        <div className="m-4 mt-0 rounded border border-err/40 px-3 py-2">
          {confirming ? (
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span className="text-warn">
                {provider.models} turns will fail until a credential is set again. Remove?
              </span>
              <button
                type="button"
                ref={cancelRef}
                onClick={() => setConfirming(false)}
                onKeyDown={(e) => {
                  if (e.key === "Escape") setConfirming(false);
                }}
                className="rounded border border-border px-2 py-1 text-muted hover:text-fg focus-visible:ring-1 focus-visible:ring-accent"
              >
                Cancel
              </button>
              <button
                type="button"
                disabled={clearing}
                onClick={() => void clear()}
                className="rounded bg-err px-2 py-1 font-medium text-bg hover:opacity-90 disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-accent"
              >
                Confirm
              </button>
            </div>
          ) : (
            <div className="flex flex-wrap items-center gap-2 text-xs">
              <span className="text-muted">
                Removing it stops every {provider.models} turn until a new one is set.
              </span>
              <button
                type="button"
                data-testid={tid("provider-key-remove")}
                onClick={() => setConfirming(true)}
                className="ml-auto rounded border border-err/50 px-2 py-1 text-err hover:bg-err/10 focus-visible:ring-1 focus-visible:ring-accent"
              >
                {settings?.authKind === "oauth" ? "Sign out" : "Remove key"}
              </button>
            </div>
          )}
        </div>
      ) : null}
    </section>
  );
}

/**
 * SubscriptionPanel is the device-code flow: one button, then a code to read out and a URL
 * to open, then waiting.
 *
 * The code is the whole interaction, so it is rendered at a size somebody can read off a
 * screen and type on a phone, with a copy button for when they cannot.
 */
function SubscriptionPanel({
  provider,
  signing,
  starting,
  onStart,
  onCancel,
  tid,
}: {
  provider: Provider;
  signing?: Signing;
  starting: boolean;
  onStart: () => void;
  onCancel: () => void;
  tid: (name: string) => string;
}) {
  const [copied, setCopied] = useState(false);
  const left = useCountdown(signing?.expiresAt);

  if (!signing) {
    return (
      <div className="space-y-2">
        <button
          type="button"
          data-testid={tid("provider-oauth-start")}
          disabled={starting}
          onClick={onStart}
          className="flex items-center gap-2 rounded bg-accent px-3 py-1.5 text-sm font-medium text-bg hover:opacity-90 disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-accent"
        >
          {starting ? <Spinner /> : null}
          {starting ? "Asking for a code…" : provider.subscription?.label}
        </button>
        <p className="max-w-2xl text-xs text-muted">
          {provider.subscription?.hint} You approve it on {provider.name}&apos;s own site; this
          host never sees your password. The access token it issues is refreshed here in the
          background, so you sign in once.
        </p>
      </div>
    );
  }

  return (
    <div
      data-testid={tid("provider-oauth-panel")}
      className="max-w-2xl space-y-3 rounded border border-accent/40 bg-raised px-4 py-3"
    >
      <ol className="space-y-3 text-xs">
        <li className="flex flex-wrap items-center gap-2">
          <Step n={1} />
          <span className="text-fg">Open</span>
          <a
            href={signing.urlComplete || signing.url}
            target="_blank"
            rel="noreferrer noopener"
            className="font-mono text-accent hover:underline"
          >
            {signing.url}
          </a>
        </li>
        <li className="flex flex-wrap items-center gap-2">
          <Step n={2} />
          <span className="text-fg">Enter this code</span>
          <code
            data-testid={tid("provider-oauth-code")}
            className="rounded border border-border bg-bg px-3 py-1.5 font-mono text-lg tracking-[0.2em] text-fg"
          >
            {signing.userCode}
          </code>
          <button
            type="button"
            onClick={() => {
              void navigator.clipboard?.writeText(signing.userCode).then(
                () => setCopied(true),
                () => setCopied(false),
              );
            }}
            className="rounded border border-border px-2 py-1 text-muted hover:text-fg"
          >
            {copied ? "Copied" : "Copy"}
          </button>
        </li>
      </ol>
      <div className="flex flex-wrap items-center gap-2 border-t border-border pt-2 text-xs">
        <Spinner />
        <span className="text-muted">Waiting for you to approve it…</span>
        {left ? <span className="text-muted">expires in {left}</span> : null}
        <button
          type="button"
          onClick={onCancel}
          className="ml-auto rounded border border-border px-2 py-1 text-muted hover:text-fg"
        >
          Cancel
        </button>
      </div>
    </div>
  );
}

/** Connected is the one line that says what is actually stored. */
function Connected({ settings }: { settings?: ProviderSettings }) {
  if (settings?.authKind === "oauth") {
    return (
      <>
        subscription
        {settings.account ? ` · ${settings.account}` : ""}
        {settings.setAt ? ` · ${relative(settings.setAt)}` : ""}
        {settings.refreshable ? " · auto-renewing" : " · not renewable"}
      </>
    );
  }
  if (settings?.keyHint) {
    return (
      <>
        ••••{settings.keyHint}
        {settings.setBy ? ` · set by ${settings.setBy}` : ""}
        {settings.setAt ? ` · ${relative(settings.setAt)}` : ""}
      </>
    );
  }
  // A key set with `podium secret set`, or replaced with it since this one was saved here.
  // There is a credential and turns will run; nothing is known about it.
  return <>set outside this UI</>;
}

/** useCountdown renders the time left as a human would say it, ticking once a second. */
function useCountdown(until?: number): string {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!until) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [until]);
  if (!until) return "";
  const secs = Math.max(0, Math.round((until - now) / 1000));
  const m = Math.floor(secs / 60);
  return m > 0 ? `${m}m ${String(secs % 60).padStart(2, "0")}s` : `${secs}s`;
}

/**
 * failure turns one error into one sentence. The two Unavailable cases are the interesting
 * part: the conductor being down and the conductor being unable to reach the provider
 * arrive with the same code, and only the message tells them apart.
 */
function failure(err: unknown, provider: Provider): Result {
  if (isAgentUnreachable(err)) {
    // The proxy wrote this one; the provider was never asked, so there is nothing to add.
    return { tone: "warn", text: "podium-agent is not reachable." };
  }
  const said = providerMessage(err);
  if (err instanceof ConnectError && err.code === Code.FailedPrecondition) {
    // Configuration, not a refusal: the conductor's own sentence says which knob is unset.
    return { tone: "warn", text: err.rawMessage };
  }
  if (err instanceof ConnectError && err.code === Code.Unavailable) {
    return {
      tone: "warn",
      text: `Couldn't reach ${provider.name} to validate. Nothing was saved. Try again.`,
      detail: said ? { label: "Details", text: said } : undefined,
    };
  }
  if (err instanceof ConnectError && err.code === Code.PermissionDenied) {
    // The server's words, verbatim: it is the one that talked to the provider.
    return {
      tone: "err",
      text: err.rawMessage,
      detail: said ? { label: `${provider.name} said`, text: said } : undefined,
    };
  }
  return { tone: "err", text: errorMessage(err) };
}

/**
 * providerMessage is what the provider itself said about the failure, out of the Connect
 * error's ProviderKeyError detail.
 *
 * This string comes from **the provider**, not out of a task container: it is not the
 * untrusted task output docs/security.md is about, and it is shown to the operator rather
 * than withheld. "Anthropic rejected this key" on its own once sent someone hunting for a
 * new key for an hour when the provider had already said the key needed a header Podium does
 * not send. Do not harden this away.
 *
 * It is still another company's text, so it is rendered as a React text node and never as
 * markup — markup in it is characters on a page, exactly as in lib/markdown.ts. The
 * conductor has already bounded it and scrubbed anything key-shaped out of it, and the
 * paragraph it lands in wraps rather than overflowing the card.
 */
function providerMessage(err: unknown): string | undefined {
  if (!(err instanceof ConnectError)) return undefined;
  const [detail] = err.findDetails(ProviderKeyErrorSchema);
  return detail?.providerMessage.trim() || undefined;
}

/** trimPasted strips the whitespace and the matched quotes a paste brings with it. */
function trimPasted(raw: string): string {
  let s = raw.trim();
  while (s.length >= 2) {
    const q = s[0];
    if ((q === '"' || q === "'") && s[s.length - 1] === q) {
      s = s.slice(1, -1).trim();
      continue;
    }
    break;
  }
  return s;
}

function Spinner() {
  return (
    <span
      aria-hidden="true"
      className="inline-block size-3 shrink-0 animate-spin rounded-full border-2 border-current border-t-transparent"
    />
  );
}

function Step({ n }: { n: number }): ReactNode {
  return (
    <span
      aria-hidden="true"
      className="inline-flex size-5 shrink-0 items-center justify-center rounded-full border border-border text-[10px] text-muted"
    >
      {n}
    </span>
  );
}
