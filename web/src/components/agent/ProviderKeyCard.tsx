import { useEffect, useRef, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import {
  ProviderKeyErrorSchema,
  type ProviderSettings,
  type SetProviderKeyResponse,
} from "../../gen/podium/agent/v1/agent_pb";
import { errorMessage, isAgentUnreachable } from "../../lib/client";
import { relative } from "../../lib/format";
import { Badge, Chip } from "../Badge";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";

/** How many model ids the confirmation card shows before it stops. */
const MODEL_CHIPS = 6;

type Result =
  | { tone: "ok"; text: string; models: string[]; note?: string }
  | { tone: "err" | "warn"; text: string; detail?: { label: string; text: string } };

export type ProviderKeyCardProps = {
  settings?: ProviderSettings;
  loading?: boolean;
  /** The RPCs, injected: the card itself talks to nothing. */
  onSave: (key: string) => Promise<SetProviderKeyResponse>;
  onClear: () => Promise<void>;
};

/**
 * The Anthropic row of the settings tab: paste a key, see it validated, see it stored.
 *
 * The card is presentational — every call is a prop — and it is deliberately talkative about
 * what happened to the key, because "saved" has to mean "agents will run". A refusal, a
 * provider that could not be reached and a conductor that is down are three different
 * sentences and three different colours.
 */
export function ProviderKeyCard({ settings, loading, onSave, onClear }: ProviderKeyCardProps) {
  const toast = useToast();
  const [value, setValue] = useState("");
  const [reveal, setReveal] = useState(false);
  const [saving, setSaving] = useState(false);
  const [result, setResult] = useState<Result>();
  const [confirming, setConfirming] = useState(false);
  const [clearing, setClearing] = useState(false);
  const cancelRef = useRef<HTMLButtonElement>(null);

  // The confirm takes focus when it opens, so the destructive path is where the keyboard is.
  useEffect(() => {
    if (confirming) cancelRef.current?.focus();
  }, [confirming]);

  const keySet = settings?.keySet ?? false;
  const hint = settings?.keyHint ?? "";

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
      setResult(failure(err));
    } finally {
      setSaving(false);
    }
  }

  async function clear() {
    setClearing(true);
    try {
      await onClear();
      setConfirming(false);
      setResult(undefined);
      setValue("");
      toast("The Anthropic key was removed.", "ok");
    } catch (err) {
      toast(errorMessage(err));
    } finally {
      setClearing(false);
    }
  }

  return (
    <section className="rounded border border-border bg-panel">
      <header className="flex flex-wrap items-center gap-3 border-b border-border px-4 py-3">
        <ProviderMark />
        <h2 className="text-sm font-semibold">Anthropic</h2>
        {loading ? (
          <Skeleton className="h-5 w-20" />
        ) : (
          <Badge tone={keySet ? "ok" : "idle"}>{keySet ? "Connected" : "Not set"}</Badge>
        )}
        {keySet ? (
          <p className="ml-auto font-mono text-xs text-muted" data-testid="provider-key-meta">
            {hint ? (
              <>
                ••••{hint}
                {settings?.setBy ? ` · set by ${settings.setBy}` : ""}
                {settings?.setAt ? ` · ${relative(settings.setAt)}` : ""}
              </>
            ) : (
              // A key set with `podium secret set`, or replaced with it since this one was
              // saved here. There is a key and turns will run; nothing is known about it.
              "set outside this UI"
            )}
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
                Agents use this key to talk to Claude. It is encrypted at rest by
                podium-server and handed only to the containers that run agent turns. It
                leaves this host only to reach Anthropic.
              </p>
            )}

            <form
              className="flex flex-wrap items-end gap-2"
              onSubmit={(e) => {
                e.preventDefault();
                void save();
              }}
            >
              <div className="min-w-64 flex-1">
                <label className="block text-xs text-muted" htmlFor="provider-key">
                  API key
                </label>
                <div className="mt-1 flex items-center gap-1">
                  <input
                    id="provider-key"
                    data-testid="provider-key-input"
                    type={reveal ? "text" : "password"}
                    autoComplete="off"
                    spellCheck={false}
                    placeholder={
                      keySet
                        ? hint
                          ? `Paste a new key to replace ••••${hint}`
                          : "Paste a key to replace the one that is set"
                        : "sk-ant-…"
                    }
                    value={value}
                    // People copy out of a .env file, so the quotes and the whitespace come
                    // with it. Trimming on the way in is kinder than an error message.
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
              </div>
              <button
                type="submit"
                data-testid="provider-key-save"
                disabled={value.trim() === "" || saving}
                className="flex items-center gap-2 rounded bg-accent px-3 py-1.5 text-sm font-medium text-bg hover:opacity-90 disabled:opacity-40 focus-visible:ring-1 focus-visible:ring-accent"
              >
                {saving ? (
                  <span
                    aria-hidden="true"
                    className="inline-block size-3 animate-spin rounded-full border-2 border-bg border-t-transparent"
                  />
                ) : null}
                {saving ? "Checking with Anthropic…" : "Validate & save"}
              </button>
            </form>

            {result ? (
              <div
                data-testid="provider-key-status"
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
                    data-testid="provider-key-detail"
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
                Agents will fail until a key is set again. Remove?
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
                Removing the key stops every agent turn until a new one is set.
              </span>
              <button
                type="button"
                data-testid="provider-key-remove"
                onClick={() => setConfirming(true)}
                className="ml-auto rounded border border-err/50 px-2 py-1 text-err hover:bg-err/10 focus-visible:ring-1 focus-visible:ring-accent"
              >
                Remove key
              </button>
            </div>
          )}
        </div>
      ) : null}
    </section>
  );
}

/**
 * failure turns one error into one sentence. The two Unavailable cases are the interesting
 * part: the conductor being down and the conductor being unable to reach Anthropic arrive
 * with the same code, and only the message separates them.
 */
function failure(err: unknown): Result {
  if (isAgentUnreachable(err)) {
    // The proxy wrote this one; Anthropic was never asked, so there is nothing to add.
    return { tone: "warn", text: "podium-agent is not reachable." };
  }
  const said = providerMessage(err);
  if (err instanceof ConnectError && err.code === Code.Unavailable) {
    return {
      tone: "warn",
      text: "Couldn't reach Anthropic to validate. Nothing was saved. Try again.",
      detail: said ? { label: "Details", text: said } : undefined,
    };
  }
  if (err instanceof ConnectError && err.code === Code.PermissionDenied) {
    // The server's words, verbatim: it is the one that talked to Anthropic.
    return {
      tone: "err",
      text: err.rawMessage,
      detail: said ? { label: "Anthropic said", text: said } : undefined,
    };
  }
  return { tone: "err", text: errorMessage(err) };
}

/**
 * providerMessage is what the provider itself said about the failure, out of the Connect
 * error's ProviderKeyError detail.
 *
 * This string comes from **Anthropic**, not out of a task container: it is not the untrusted
 * task output docs/security.md is about, and it is shown to the operator rather than
 * withheld. "Anthropic rejected this key" on its own once sent someone hunting for a new key
 * for an hour when the provider had already said the key needed a header Podium does not
 * send. Do not harden this away.
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

/** A small neutral provider glyph. Decorative: the provider is named in text beside it. */
function ProviderMark() {
  return (
    <svg aria-hidden="true" viewBox="0 0 16 16" className="size-4 text-accent" fill="currentColor">
      <path d="M8 1.2 9.6 6.4 14.8 8 9.6 9.6 8 14.8 6.4 9.6 1.2 8 6.4 6.4Z" />
    </svg>
  );
}
