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
import {
  SECTIONS,
  fieldErrors,
  placeholderOf,
  type ProviderField,
  type ProviderSpec,
} from "../../lib/sandboxProviders";
import { cn } from "@/lib/utils";

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

  const errors = fieldErrors(spec, values);
  // Why Save is off, in the footer beside it: a disabled button explains nothing on its own.
  const blocker =
    !configured && secret.trim() === ""
      ? `Add the ${spec.secretLabel.toLowerCase()} to save.`
      : cap === undefined || Object.keys(errors).length > 0
        ? "Fix the fields marked above to save."
        : spec.requireMaxConcurrent && cap === 0
          ? "Set Max concurrent sandboxes to save."
          : undefined;
  const ready = blocker === undefined;

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
          className="grid gap-6"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) set.mutate();
          }}
        >
          {SECTIONS.map((section) => {
            const fields = spec.fields.filter((f) => f.section === section.key);
            const secretHere = section.key === "access";
            const capHere = section.key === "capacity";
            if (fields.length === 0 && !secretHere && !capHere) return null;
            const headingId = `provider-section-${section.key}`;
            return (
              <section
                key={section.key}
                aria-labelledby={headingId}
                className="grid gap-4 border-t border-hairline pt-5 first:border-t-0 first:pt-0"
              >
                <div className="grid gap-1">
                  <h3 id={headingId} className="m-0 text-xs font-bold tracking-wide text-muted uppercase">
                    {section.title}
                  </h3>
                  {section.description ? <p className="m-0 text-sm text-soft">{section.description}</p> : null}
                </div>

                {secretHere ? (
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
                    <p id="provider-secret-help" className="m-0 text-sm break-words text-muted">
                      {spec.secretHelp}
                    </p>
                  </div>
                ) : null}

                {capHere ? (
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
                    <p id="provider-max-concurrent-help" className="m-0 text-sm text-muted">
                      {spec.maxConcurrentHelp}
                      {accountLimit > 0 ? ` The account allows ${accountLimit} at once.` : ""}
                    </p>
                    {cap === undefined ? <p className="m-0 text-sm text-err">A whole number.</p> : null}
                    {cap !== undefined && accountLimit > 0 && cap > accountLimit ? (
                      <Alert variant="warn" title={`Higher than the ${spec.label} account allows`}>
                        The account runs at most {accountLimit} sandboxes at once, so tasks past that wait
                        for the account, not for this cap. Raise the account&apos;s limit, or set {accountLimit} or
                        less.
                      </Alert>
                    ) : null}
                  </div>
                ) : null}

                {fields.length > 0 ? (
                  <div className={cn("grid gap-4", section.key === "resources" && fields.length > 1 && "sm:grid-cols-2")}>
                    {fields.map((f) => (
                      <SettingField
                        key={f.key}
                        field={f}
                        value={values[f.key] ?? ""}
                        values={values}
                        error={errors[f.key]}
                        onChange={(v) => setValues((old) => ({ ...old, [f.key]: v }))}
                      />
                    ))}
                  </div>
                ) : null}
              </section>
            );
          })}

          <DialogFooter>
            {blocker ? (
              <p className="m-0 mr-auto text-sm text-muted" data-testid="provider-save-blocker">
                {blocker}
              </p>
            ) : null}
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

/**
 * One of the provider's own settings: a choice, or a field with its unit inside it. An error
 * goes under the help, which stays, so fields side by side keep their help in line.
 */
function SettingField({
  field: f,
  value,
  values,
  error,
  onChange,
}: {
  field: ProviderField;
  value: string;
  values: Record<string, string>;
  error?: string;
  onChange: (v: string) => void;
}) {
  const id = `provider-${f.key}`;
  const describedBy = error ? `${id}-error ${id}-help` : `${id}-help`;
  return (
    <div className="grid content-start gap-1.5">
      <Label htmlFor={id} className={fieldLabel}>
        {f.label}
        {f.unit ? <span className="sr-only"> ({f.unit})</span> : null}
      </Label>
      {f.options ? (
        <select
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-describedby={describedBy}
          className="min-h-11 rounded-md border border-line-strong bg-panel px-3 font-mono text-xs text-fg hover:border-muted"
        >
          <option value="">Provider default</option>
          {f.options.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      ) : (
        <div className="relative">
          <Input
            id={id}
            value={value}
            onChange={(e) => onChange(e.target.value)}
            placeholder={placeholderOf(f, values)}
            inputMode={f.kind === "integer" ? "numeric" : f.kind === "number" ? "decimal" : undefined}
            spellCheck={false}
            autoComplete="off"
            autoCapitalize="off"
            aria-invalid={error ? true : undefined}
            aria-describedby={describedBy}
            className={cn("font-mono text-xs", f.unit && "pr-16")}
          />
          {f.unit ? (
            <span
              aria-hidden
              className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-muted"
            >
              {f.unit}
            </span>
          ) : null}
        </div>
      )}
      <p id={`${id}-help`} className="m-0 text-sm text-muted">
        {f.help}
      </p>
      {error ? (
        <p id={`${id}-error`} className="m-0 text-sm text-err">
          {error}
        </p>
      ) : null}
    </div>
  );
}

/** parseCap reads the cap field: empty is 0, no cap; anything but a whole number is undefined. */
function parseCap(v: string): number | undefined {
  const t = v.trim();
  if (t === "") return 0;
  return /^\d+$/.test(t) ? Number(t) : undefined;
}
