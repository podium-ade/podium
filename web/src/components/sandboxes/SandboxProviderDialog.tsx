import { useEffect, useRef, useState, type ReactNode } from "react";
import { Eye, EyeOff } from "lucide-react";
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
  type SectionKey,
} from "../../lib/sandboxProviders";
import { cn } from "@/lib/utils";

const fieldLabel = "text-sm font-bold text-fg";
const helpText = "m-0 text-[0.8125rem] leading-snug [overflow-wrap:anywhere] text-muted";

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

  // Which section each problem sits in, so the rail can point at it.
  const sectionOf = new Map(spec.fields.map((f) => [f.key, f.section]));
  const troubled = new Set<SectionKey>(Object.keys(errors).map((k) => sectionOf.get(k)!).filter(Boolean));
  if (cap === undefined || (spec.requireMaxConcurrent && cap === 0)) troubled.add("capacity");
  if (!configured && secret.trim() === "") troubled.add("access");

  const sections = SECTIONS.filter(
    (s) => s.key === "access" || s.key === "capacity" || spec.fields.some((f) => f.section === s.key),
  );
  const scroller = useRef<HTMLDivElement>(null);
  const [active, setActive] = useState<SectionKey>("access");

  // The rail follows the reader: the topmost section in view is the one lit.
  useEffect(() => {
    const root = scroller.current;
    if (!root || typeof IntersectionObserver === "undefined") return;
    const seen = new Map<string, boolean>();
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) seen.set(e.target.id, e.isIntersecting);
        const first = sections.find((s) => seen.get(sectionId(s.key)));
        if (first) setActive(first.key);
      },
      { root, rootMargin: "0px 0px -55% 0px" },
    );
    for (const s of sections) {
      const el = document.getElementById(sectionId(s.key));
      if (el) io.observe(el);
    }
    return () => io.disconnect();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [spec.name]);

  const jump = (key: SectionKey) => {
    setActive(key);
    // Scroll the form pane only: scrollIntoView would also scroll the dialog around it.
    const el = document.getElementById(sectionId(key));
    const root = scroller.current;
    if (el && root) root.scrollTo?.({ top: el.offsetTop, behavior: "smooth" });
  };

  const secretNoun = spec.secretLabel.toLowerCase();

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="flex h-[min(780px,calc(100vh-2rem))] max-w-[920px] flex-col gap-0 overflow-hidden p-0">
        <DialogHeader className="border-b border-hairline px-6 pt-6 pb-5">
          <DialogTitle className="text-xl leading-tight font-bold tracking-normal">
            {configured ? `Edit ${spec.label}` : `Set up ${spec.label}`}
          </DialogTitle>
          <DialogDescription className="text-sm text-soft">
            The {secretNoun} is written once and never read back. A saved change applies at once.
          </DialogDescription>
        </DialogHeader>

        <form
          className="flex min-h-0 flex-1 flex-col"
          noValidate
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) set.mutate();
          }}
        >
          <div className="grid min-h-0 flex-1 grid-rows-[minmax(0,1fr)] sm:grid-cols-[11rem_minmax(0,1fr)]">
            <nav aria-label="Settings sections" className="hidden border-r border-hairline bg-bg/60 p-3 sm:block">
              <ul className="m-0 grid list-none gap-0.5 p-0">
                {sections.map((s) => (
                  <li key={s.key}>
                    <button
                      type="button"
                      onClick={() => jump(s.key)}
                      aria-current={active === s.key ? "true" : undefined}
                      className={cn(
                        "flex w-full items-center justify-between gap-2 rounded-md px-3 py-2 text-left text-sm transition-colors",
                        "outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
                        active === s.key ? "bg-raised font-bold text-fg" : "text-soft hover:bg-raised hover:text-fg",
                      )}
                    >
                      {s.title}
                      {troubled.has(s.key) ? (
                        <span className="size-1.5 shrink-0 rounded-full bg-err" aria-label="needs attention" />
                      ) : null}
                    </button>
                  </li>
                ))}
              </ul>
            </nav>

            <div ref={scroller} className="relative min-h-0 overflow-y-auto overscroll-contain px-6 pb-10">
              {sections.map((section) => {
                const fields = spec.fields.filter((f) => f.section === section.key);
                return (
                  <section
                    key={section.key}
                    id={sectionId(section.key)}
                    aria-labelledby={`${sectionId(section.key)}-title`}
                    className="pt-7"
                  >
                    <div className="grid gap-1 pb-2">
                      <h3 id={`${sectionId(section.key)}-title`} className="m-0 text-base font-bold text-fg">
                        {section.title}
                      </h3>
                      {section.description ? (
                        <p className="m-0 max-w-[68ch] text-sm text-muted">{section.description}</p>
                      ) : null}
                    </div>

                    {section.key === "access" ? (
                      <Row
                        label={
                          <Label htmlFor="provider-secret" className={fieldLabel}>
                            {spec.secretLabel}
                          </Label>
                        }
                        help={
                          <p id="provider-secret-help" className={helpText}>
                            {spec.secretHelp}
                          </p>
                        }
                      >
                        <div className="relative">
                          <Input
                            id="provider-secret"
                            type={reveal ? "text" : "password"}
                            value={secret}
                            onChange={(e) => setSecret(e.target.value)}
                            autoFocus
                            spellCheck={false}
                            autoComplete="new-password"
                            placeholder={configured ? "Stored. Leave empty to keep it" : undefined}
                            aria-describedby="provider-secret-help"
                            className="pr-11 font-mono text-xs"
                          />
                          <button
                            type="button"
                            aria-pressed={reveal}
                            aria-label={reveal ? `Hide ${secretNoun}` : `Show ${secretNoun}`}
                            title={reveal ? `Hide ${secretNoun}` : `Show ${secretNoun}`}
                            onClick={() => setReveal((r) => !r)}
                            className="absolute inset-y-1 right-1 grid w-9 place-items-center rounded-md text-muted outline-none hover:bg-raised hover:text-fg focus-visible:ring-2 focus-visible:ring-ring/50"
                          >
                            {reveal ? <EyeOff className="size-4" /> : <Eye className="size-4" />}
                          </button>
                        </div>
                      </Row>
                    ) : null}

                    {section.key === "capacity" ? (
                      <Row
                        label={
                          <Label htmlFor="provider-max-concurrent" className={fieldLabel}>
                            <span>
                              Max concurrent sandboxes
                              {spec.requireMaxConcurrent ? null : <span className="font-normal text-muted"> (optional)</span>}
                            </span>
                          </Label>
                        }
                        help={
                          <p id="provider-max-concurrent-help" className={helpText}>
                            {spec.maxConcurrentHelp}
                            {accountLimit > 0 ? ` The account allows ${accountLimit} at once.` : ""}
                          </p>
                        }
                      >
                        <Input
                          id="provider-max-concurrent"
                          inputMode="numeric"
                          value={maxConcurrent}
                          onChange={(e) => setMaxConcurrent(e.target.value)}
                          placeholder={accountLimit > 0 ? String(accountLimit) : undefined}
                          required={spec.requireMaxConcurrent}
                          aria-invalid={cap === undefined}
                          aria-describedby="provider-max-concurrent-help"
                          className="max-w-40 font-mono text-xs tabular-nums"
                        />
                        {cap === undefined ? <p className="m-0 text-sm text-err">A whole number.</p> : null}
                        {cap !== undefined && accountLimit > 0 && cap > accountLimit ? (
                          <Alert variant="warn" title={`Higher than the ${spec.label} account allows`}>
                            The account runs at most {accountLimit} sandboxes at once, so tasks past that wait
                            for the account, not for this cap. Raise the account&apos;s limit, or set {accountLimit} or
                            less.
                          </Alert>
                        ) : null}
                      </Row>
                    ) : null}

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
                  </section>
                );
              })}
            </div>
          </div>

          <DialogFooter className="border-t border-hairline bg-panel px-6 py-4">
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

function sectionId(key: SectionKey): string {
  return `provider-section-${key}`;
}

/**
 * A settings row: what the field is and why on the left, the control on the right, so the
 * help reads as a margin note beside the input rather than a paragraph between inputs.
 */
function Row({ label, help, children }: { label: ReactNode; help?: ReactNode; children: ReactNode }) {
  return (
    <div className="grid gap-x-8 gap-y-2 border-t border-hairline py-4 md:grid-cols-[minmax(0,15rem)_minmax(0,1fr)]">
      <div className="grid content-start gap-1">
        {label}
        {help}
      </div>
      <div className="grid content-start gap-2">{children}</div>
    </div>
  );
}

/** One of the provider's own settings: a choice, or a field with its unit inside it. */
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
  const short = f.kind !== undefined;
  return (
    <Row
      label={
        <Label htmlFor={id} className={fieldLabel}>
          {f.label}
          {f.unit ? <span className="sr-only"> ({f.unit})</span> : null}
        </Label>
      }
      help={
        <p id={`${id}-help`} className={helpText}>
          {f.help}
        </p>
      }
    >
      {f.options ? (
        <select
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          aria-describedby={describedBy}
          className="min-h-11 max-w-60 rounded-md border border-line-strong bg-panel px-3 font-mono text-xs text-fg hover:border-muted focus-visible:ring-2 focus-visible:ring-ring/50 focus-visible:outline-none"
        >
          <option value="">Provider default</option>
          {f.options.map((o) => (
            <option key={o} value={o}>
              {o}
            </option>
          ))}
        </select>
      ) : (
        <div className={cn("relative", short && "max-w-60")}>
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
            className={cn("font-mono text-xs", short && "tabular-nums", f.unit && "pr-16")}
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
      {error ? (
        <p id={`${id}-error`} className="m-0 text-sm text-err">
          {error}
        </p>
      ) : null}
    </Row>
  );
}

/** parseCap reads the cap field: empty is 0, no cap; anything but a whole number is undefined. */
function parseCap(v: string): number | undefined {
  const t = v.trim();
  if (t === "") return 0;
  return /^\d+$/.test(t) ? Number(t) : undefined;
}
