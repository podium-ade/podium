import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { useToast } from "../Toast";
import { Alert } from "../ui/alert";
import { Button } from "../ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Input } from "../ui/input";
import { Label } from "../ui/label";
import type { SandboxProvider } from "../../gen/podium/v1/sandbox_pb";
import { errorMessage, sandboxProviders } from "../../lib/client";
import type { ProviderSpec } from "../../lib/sandboxProviders";

const fieldLabel = "text-sm font-bold text-fg";

/**
 * SetSandboxProvider behind a dialog. Settings are prefilled from what is stored; the
 * credential never is — there is nothing to read it back from — so leaving it empty on a
 * configured provider keeps the one it has.
 */
export function SandboxProviderDialog({
  spec,
  current,
  open,
  onOpenChange,
}: {
  spec: ProviderSpec;
  current?: SandboxProvider;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [values, setValues] = useState<Record<string, string>>(() => ({ ...current?.settings }));
  const [secret, setSecret] = useState("");
  const [reveal, setReveal] = useState(false);
  const [maxConcurrent, setMaxConcurrent] = useState(current?.maxConcurrent ? String(current.maxConcurrent) : "");
  const configured = current?.hasSecret ?? false;
  const cap = parseCap(maxConcurrent);
  const accountLimit = current?.accountLimit ?? 0;

  const set = useMutation({
    mutationFn: () => {
      const settings: Record<string, string> = {};
      for (const f of spec.fields) {
        const v = (values[f.key] ?? "").trim();
        if (v !== "") settings[f.key] = v;
      }
      return sandboxProviders.setSandboxProvider({
        name: spec.name,
        settings,
        maxConcurrent: cap ?? 0,
        secret: new TextEncoder().encode(secret.trim()),
      });
    },
    onSuccess: () => {
      toast(`${spec.label} saved.`, "ok");
      void qc.invalidateQueries({ queryKey: ["sandboxProviders"] });
      onOpenChange(false);
    },
    onError: (err) => toast(`SetSandboxProvider: ${errorMessage(err)}`),
  });

  const ready =
    (configured || secret.trim() !== "") && cap !== undefined && (!spec.requireMaxConcurrent || cap > 0);

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100vh-2rem)] max-w-[640px] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="text-2xl leading-tight font-bold tracking-normal">
            {configured ? `Edit ${spec.label}` : `Set up ${spec.label}`}
          </DialogTitle>
          <DialogDescription className="text-base text-soft">
            The {spec.secretLabel.toLowerCase()} is written once and can never be read back.
            Podium runs {spec.label} sandboxes itself with it, and a saved change applies at once.
          </DialogDescription>
        </DialogHeader>

        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) set.mutate();
          }}
        >
          <div className="grid gap-1.5">
            <div className="flex items-center justify-between gap-2">
              <Label htmlFor="provider-secret" className={fieldLabel}>
                {spec.secretLabel}
              </Label>
              <Button
                type="button"
                variant="ghost"
                size="sm"
                aria-pressed={reveal}
                onClick={() => setReveal((r) => !r)}
              >
                {reveal ? `Hide ${spec.secretLabel.toLowerCase()}` : `Show ${spec.secretLabel.toLowerCase()}`}
              </Button>
            </div>
            <Input
              id="provider-secret"
              type={reveal ? "text" : "password"}
              value={secret}
              onChange={(e) => setSecret(e.target.value)}
              autoFocus
              spellCheck={false}
              autoComplete="new-password"
              placeholder={configured ? "Leave empty to keep the stored one" : undefined}
              aria-describedby="provider-secret-help"
              className="font-mono text-xs"
            />
            <p id="provider-secret-help" className="text-sm break-words text-muted">
              {spec.secretHelp}
            </p>
          </div>

          <div className="grid gap-1.5">
            <Label htmlFor="provider-max-concurrent" className={fieldLabel}>
              Max concurrent sandboxes{spec.requireMaxConcurrent ? "" : " (optional)"}
            </Label>
            <Input
              id="provider-max-concurrent"
              inputMode="numeric"
              value={maxConcurrent}
              onChange={(e) => setMaxConcurrent(e.target.value)}
              placeholder={accountLimit > 0 ? String(accountLimit) : undefined}
              required={spec.requireMaxConcurrent}
              aria-invalid={cap === undefined}
              aria-describedby="provider-max-concurrent-help"
              className="font-mono text-xs"
            />
            <p id="provider-max-concurrent-help" className="text-sm text-muted">
              {spec.maxConcurrentHelp}
              {accountLimit > 0 ? ` The account allows ${accountLimit} at once.` : ""}
            </p>
            {cap !== undefined && accountLimit > 0 && cap > accountLimit ? (
              <Alert variant="warn" title={`Higher than the ${spec.label} account allows`}>
                The account runs at most {accountLimit} sandboxes at once, so tasks past that wait
                for the account, not for this cap. Raise the account&apos;s limit, or set {accountLimit} or
                less.
              </Alert>
            ) : null}
          </div>

          {spec.fields.map((f) => {
            const id = `provider-${f.key}`;
            return (
              <div key={f.key} className="grid gap-1.5">
                <Label htmlFor={id} className={fieldLabel}>
                  {f.label}
                </Label>
                {f.options ? (
                  <select
                    id={id}
                    value={values[f.key] ?? ""}
                    onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
                    aria-describedby={`${id}-help`}
                    className="min-h-11 rounded-md border border-input bg-panel px-3 font-mono text-xs text-fg"
                  >
                    <option value="">Provider default</option>
                    {f.options.map((o) => (
                      <option key={o} value={o}>
                        {o}
                      </option>
                    ))}
                  </select>
                ) : (
                  <Input
                    id={id}
                    value={values[f.key] ?? ""}
                    onChange={(e) => setValues((v) => ({ ...v, [f.key]: e.target.value }))}
                    placeholder={f.placeholder}
                    spellCheck={false}
                    autoComplete="off"
                    autoCapitalize="off"
                    aria-describedby={`${id}-help`}
                    className="font-mono text-xs"
                  />
                )}
                <p id={`${id}-help`} className="text-sm text-muted">
                  {f.help}
                </p>
              </div>
            );
          })}

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={!ready || set.isPending}>
              {set.isPending ? "Saving…" : `Save ${spec.label}`}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

/** parseCap reads the cap field: empty is 0, no cap; anything but a whole number is undefined. */
function parseCap(v: string): number | undefined {
  const t = v.trim();
  if (t === "") return 0;
  return /^\d+$/.test(t) ? Number(t) : undefined;
}
