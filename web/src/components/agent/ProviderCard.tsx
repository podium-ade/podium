import { useEffect, useState } from "react";
import type { ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  Check,
  Copy,
  ExternalLink,
  Eye,
  EyeOff,
  KeyRound,
  RotateCw,
  ShieldCheck,
} from "lucide-react";
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
import { cn } from "../../lib/utils";
import { Badge, Chip } from "../Badge";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { Card, CardContent, CardFooter, CardHeader, CardTitle } from "../ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
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
 *
 * A configured provider shows no input. An empty password field on a card that already says
 * "Connected" reads as a credential that has gone missing, so replacing one is a deliberate
 * act behind "Rotate key".
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
  const [rotating, setRotating] = useState(false);
  const [signing, setSigning] = useState<Signing>();
  const [starting, setStarting] = useState(false);

  const keySet = settings?.keySet ?? false;
  const hint = settings?.keyHint ?? "";
  const oauth = provider.subscription !== undefined && onStartOAuth !== undefined;
  const stored = keySet && !loading;
  const editing = !loading && (!keySet || rotating);
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
            setRotating(false);
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
      setRotating(false);
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
      setRotating(false);
      setResult(undefined);
      setValue("");
      toast(`The ${provider.name} credential was removed.`, "ok");
    } catch (err) {
      toast(errorMessage(err));
    } finally {
      setClearing(false);
    }
  }

  const subscriptionStored = settings?.authKind === "oauth";

  return (
    <Card
      data-testid={tid("provider-card")}
      // A provider with no credential is an empty slot, and it should look like one before a
      // word of it is read.
      className={cn("flex flex-col", !stored && !loading && "border-dashed bg-card/40")}
    >
      {/* space-y-0: CardHeader spaces the children of its first block, and this one is a row. */}
      <CardHeader className="[&>div:first-child]:space-y-0">
        <div className="flex min-w-0 items-center gap-3">
          <span
            className={cn(
              "grid size-9 shrink-0 place-items-center rounded-lg border",
              stored ? "border-transparent bg-accent/12" : "border-border bg-raised/50",
            )}
          >
            <BackendMark id={provider.backendID} />
          </span>
          <div className="min-w-0">
            <CardTitle className="flex flex-wrap items-center gap-2">
              {provider.name}
              {loading ? (
                <Skeleton className="h-4 w-16" />
              ) : (
                <Badge tone={keySet ? "ok" : "idle"}>{keySet ? "Connected" : "Not set"}</Badge>
              )}
            </CardTitle>
            <p className="mt-1 text-xs text-muted">runs {provider.models}</p>
          </div>
        </div>
      </CardHeader>

      <CardContent className="flex-1 space-y-3">
        {loading ? (
          <div className="space-y-3" aria-busy="true" aria-label="Loading">
            <Skeleton className="h-4 w-3/4" />
            <Skeleton className="h-9 w-full" />
            <Skeleton className="h-8 w-36" />
          </div>
        ) : (
          <>
            {stored ? (
              <div className="flex items-start gap-2.5 rounded-lg border border-hairline bg-raised/40 px-3 py-2.5">
                {subscriptionStored ? (
                  <ShieldCheck className="mt-px size-3.5 shrink-0 text-ok" />
                ) : (
                  <KeyRound className="mt-px size-3.5 shrink-0 text-muted" />
                )}
                <p
                  data-testid={tid("provider-key-meta")}
                  className="min-w-0 font-mono text-xs break-words text-muted"
                >
                  <Connected settings={settings} />
                </p>
              </div>
            ) : null}

            {keySet ? null : (
              <p className="text-xs leading-relaxed text-muted">
                Agents use this credential to talk to {provider.models}. It is encrypted at rest
                by podium-server and handed only to the containers that run {provider.models}{" "}
                turns. It leaves this host only to reach {provider.name}.
              </p>
            )}

            {editing && oauth ? (
              <div className="inline-flex items-center gap-0.5 rounded-lg border border-border bg-panel p-0.5">
                {(["key", "subscription"] as const).map((m) => (
                  <button
                    key={m}
                    type="button"
                    aria-pressed={mode === m}
                    data-testid={tid(`provider-mode-${m}`)}
                    onClick={() => setMode(m)}
                    className={cn(
                      "inline-flex h-7 items-center rounded-md px-2.5 text-xs font-medium",
                      "transition-colors duration-150 outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                      mode === m ? "bg-raised text-fg shadow-xs" : "text-muted hover:text-fg",
                    )}
                  >
                    {m === "key" ? "API key" : "Subscription"}
                  </button>
                ))}
              </div>
            ) : null}

            {editing && oauth && mode === "subscription" ? (
              <SubscriptionPanel
                provider={provider}
                signing={signing}
                starting={starting}
                onStart={() => void startSignIn()}
                onCancel={() => setSigning(undefined)}
                tid={tid}
              />
            ) : null}

            {editing && !(oauth && mode === "subscription") ? (
              <form
                className="space-y-2"
                onSubmit={(e) => {
                  e.preventDefault();
                  void save();
                }}
              >
                <Label htmlFor={tid("provider-key")}>
                  {keySet ? "Replacement API key" : "API key"}
                </Label>
                <div className="relative">
                  <Input
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
                    className="pr-10 font-mono text-sm"
                  />
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-sm"
                    aria-pressed={reveal}
                    aria-label={reveal ? "Hide the key" : "Show the key"}
                    onClick={() => setReveal((v) => !v)}
                    className="absolute top-1/2 right-0.5 -translate-y-1/2"
                  >
                    {reveal ? <EyeOff /> : <Eye />}
                  </Button>
                </div>
                <p className="text-xs text-muted">
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
                <div className="flex flex-wrap items-center gap-2">
                  <Button
                    type="submit"
                    size="sm"
                    data-testid={tid("provider-key-save")}
                    disabled={value.trim() === "" || saving}
                  >
                    {saving ? <Spinner /> : null}
                    {saving ? `Checking with ${provider.name}…` : "Validate & save"}
                  </Button>
                  {rotating ? (
                    <Button
                      type="button"
                      variant="ghost"
                      size="sm"
                      onClick={() => {
                        setRotating(false);
                        setValue("");
                      }}
                    >
                      Keep the current key
                    </Button>
                  ) : null}
                </div>
              </form>
            ) : null}

            {result ? (
              <div
                data-testid={tid("provider-key-status")}
                role="status"
                aria-live="polite"
                className={cn(
                  "rounded-lg border px-3 py-2 text-xs",
                  result.tone === "ok"
                    ? "border-ok/30 bg-ok/8 text-ok"
                    : result.tone === "err"
                      ? "border-err/30 bg-err/8 text-err"
                      : "border-warn/30 bg-warn/8 text-warn",
                )}
              >
                <p>{result.text}</p>
                {result.tone !== "ok" && result.detail ? (
                  <p
                    data-testid={tid("provider-key-detail")}
                    className="mt-1.5 max-w-2xl rounded-md border border-border bg-raised px-2 py-1.5 text-xs break-words text-muted"
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
      </CardContent>

      {stored ? (
        <CardFooter>
          {rotating ? null : (
            <Button variant="outline" size="sm" onClick={() => setRotating(true)}>
              <RotateCw />
              {subscriptionStored ? "Sign in again" : "Rotate key"}
            </Button>
          )}
          <Button
            variant="danger"
            size="sm"
            data-testid={tid("provider-key-remove")}
            className="ml-auto"
            onClick={() => setConfirming(true)}
          >
            {subscriptionStored ? "Sign out" : "Remove key"}
          </Button>
        </CardFooter>
      ) : null}

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent className="max-w-md" showClose={false}>
          <DialogHeader>
            <DialogTitle>
              {subscriptionStored
                ? `Sign out of ${provider.name}?`
                : `Remove the ${provider.name} key?`}
            </DialogTitle>
            <DialogDescription>
              {provider.models} turns will fail until a credential is set again. Podium deletes
              the secret; nothing already running is stopped, and every session that has already
              answered keeps its history.
            </DialogDescription>
          </DialogHeader>
          <DialogFooter>
            <Button variant="outline" size="sm" onClick={() => setConfirming(false)}>
              Cancel
            </Button>
            <Button
              variant="destructive"
              size="sm"
              disabled={clearing}
              onClick={() => void clear()}
            >
              {clearing ? "Removing…" : "Confirm"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </Card>
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
        <Button size="sm" data-testid={tid("provider-oauth-start")} disabled={starting} onClick={onStart}>
          {starting ? <Spinner /> : <ShieldCheck />}
          {starting ? "Asking for a code…" : provider.subscription?.label}
        </Button>
        <p className="text-xs leading-relaxed text-muted">
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
      className="space-y-4 rounded-xl border border-accent/35 bg-accent/6 p-4 animate-in fade-in-0 duration-200"
    >
      <ol className="space-y-3.5 text-xs">
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
          <ExternalLink aria-hidden className="size-3 text-faint" />
        </li>
        <li className="flex flex-wrap items-center gap-2">
          <Step n={2} />
          <span className="text-fg">Enter this code</span>
          <div className="flex w-full items-center gap-2 pl-7">
            <code
              data-testid={tid("provider-oauth-code")}
              className="rounded-lg border border-border bg-bg px-4 py-2 font-mono text-2xl font-semibold tracking-[0.22em] text-fg select-all"
            >
              {signing.userCode}
            </code>
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                void navigator.clipboard?.writeText(signing.userCode).then(
                  () => setCopied(true),
                  () => setCopied(false),
                );
              }}
            >
              {copied ? <Check /> : <Copy />}
              {copied ? "Copied" : "Copy"}
            </Button>
          </div>
        </li>
      </ol>
      <div className="flex flex-wrap items-center gap-2 border-t border-hairline pt-3 text-xs">
        <Spinner />
        <span className="text-muted">Waiting for you to approve it…</span>
        {left ? <span className="tabular text-faint">expires in {left}</span> : null}
        <Button type="button" variant="ghost" size="xs" className="ml-auto" onClick={onCancel}>
          Cancel
        </Button>
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
      className="inline-flex size-5 shrink-0 items-center justify-center rounded-full border border-border bg-panel text-[10px] text-muted"
    >
      {n}
    </span>
  );
}
