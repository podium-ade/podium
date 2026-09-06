import { useState, type ReactNode } from "react";
import { Check, Copy } from "lucide-react";
import { Button } from "../ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "../ui/card";
import { formatGoDuration, type SpecInit } from "../../lib/spec";

/**
 * The document CreateTask will actually be given, rendered from the same fields the form
 * edits — so what the operator checks before firing is the thing that fires.
 */
export function SpecPreview({ yaml, empty }: { yaml: string; empty: boolean }) {
  const [copied, setCopied] = useState(false);

  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Will be submitted</CardTitle>
        </div>
        {empty ? null : (
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label="Copy the spec"
            title="Copy the spec"
            onClick={() => {
              void navigator.clipboard?.writeText(yaml);
              setCopied(true);
              setTimeout(() => setCopied(false), 1200);
            }}
          >
            {copied ? <Check className="text-ok" /> : <Copy />}
          </Button>
        )}
      </CardHeader>
      <CardContent>
        {empty ? (
          <p className="text-xs leading-relaxed text-faint">
            Name an image and this shows the spec that will be sent.
          </p>
        ) : (
          <pre className="max-h-[26rem] overflow-auto rounded-lg border border-hairline bg-bg px-3 py-2.5 font-mono text-2xs leading-5 text-muted">
            {yaml}
          </pre>
        )}
      </CardContent>
    </Card>
  );
}

/**
 * The same answer for the YAML tab, where echoing the document back would only repeat the
 * editor: what the decoder made of it, field by field, including the parts no form field owns.
 */
export function SpecDigest({ spec }: { spec?: SpecInit }) {
  return (
    <Card>
      <CardHeader>
        <div>
          <CardTitle>Will be submitted</CardTitle>
        </div>
      </CardHeader>
      <CardContent>
        {spec ? (
          <dl className="space-y-2">
            {digest(spec).map(([label, value]) => (
              <div key={label} className="space-y-0.5">
                <dt className="text-2xs text-faint">{label}</dt>
                <dd className="font-mono text-2xs leading-5 break-all text-muted">{value}</dd>
              </div>
            ))}
          </dl>
        ) : (
          <p className="text-xs leading-relaxed text-faint">
            The document does not parse yet, so there is nothing to send.
          </p>
        )}
      </CardContent>
    </Card>
  );
}

/** Every field the decoder read, and nothing it did not: an unset field has no row. */
function digest(spec: SpecInit): [string, ReactNode][] {
  const rows: [string, ReactNode][] = [["Image", spec.image || "—"]];

  if (spec.command && spec.command.length > 0) rows.push(["Command", spec.command.join(" ")]);
  if (spec.workingDir) rows.push(["Working directory", spec.workingDir]);

  const env = Object.keys(spec.env ?? {});
  if (env.length > 0) rows.push(["Environment", env.sort().join(", ")]);
  if (spec.labels && spec.labels.length > 0) rows.push(["Labels", spec.labels.join(", ")]);

  if (spec.timeout) {
    const { seconds, nanos } = spec.timeout as { seconds?: bigint | number; nanos?: number };
    rows.push(["Timeout", formatGoDuration(Number(seconds ?? 0), nanos ?? 0)]);
  }
  if (spec.maxAttempts) rows.push(["Max attempts", String(spec.maxAttempts)]);

  const resources = [
    spec.resources?.cpu ? `${spec.resources.cpu} CPU` : "",
    spec.resources?.memoryMb ? `${Number(spec.resources.memoryMb)} MB` : "",
    spec.resources?.pids ? `${spec.resources.pids} PIDs` : "",
  ].filter((p) => p !== "");
  if (resources.length > 0) rows.push(["Resources", resources.join(" · ")]);

  if (spec.secrets && spec.secrets.length > 0) {
    rows.push(["Secrets", spec.secrets.map((s) => s.name || "?").join(", ")]);
  }
  const sidecars = Object.keys(spec.sidecars ?? {});
  if (sidecars.length > 0) rows.push(["Sidecars", sidecars.join(", ")]);

  const hardening = [
    spec.hardening?.readOnlyRootfs ? "read-only rootfs" : "",
    (spec.hardening?.capabilities ?? []).join(", "),
  ].filter((p) => p !== "");
  if (hardening.length > 0) rows.push(["Hardening", hardening.join(" · ")]);

  if (spec.retryOnNodeLoss) rows.push(["On node loss", "re-run on another node"]);
  return rows;
}
