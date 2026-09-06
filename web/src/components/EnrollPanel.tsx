import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { admin, errorMessage } from "../lib/client";
import { enrollCommand, isTailnetServer } from "../lib/enroll";
import { absolute } from "../lib/format";
import { useToast } from "./Toast";
import { Button } from "./ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "./ui/card";
import { Input } from "./ui/input";

const UNITS: Record<string, number> = { minutes: 60, hours: 3600, days: 86400 };

/**
 * EnrollPanel turns CreateEnrollmentToken into the one line an operator pastes on the new node.
 * The enrollment token is single-use and the server keeps only its SHA-256, so it is shown once
 * and never re-fetched.
 */
export function EnrollPanel({ server = window.location.origin }: { server?: string }) {
  const [labels, setLabels] = useState("");
  const [ttl, setTtl] = useState(1);
  const [unit, setUnit] = useState("hours");
  const [copied, setCopied] = useState(false);
  const toast = useToast();

  const create = useMutation({
    mutationFn: () =>
      admin.createEnrollmentToken({
        labels: labels
          .split(",")
          .map((l) => l.trim())
          .filter((l) => l !== ""),
        ttl: { seconds: BigInt(Math.max(1, Math.round(ttl)) * UNITS[unit]), nanos: 0 },
      }),
    onError: (err) => toast(`CreateEnrollmentToken: ${errorMessage(err)}`),
  });

  const command = create.data ? enrollCommand(server, create.data.token) : "";

  return (
    <Card>
      <CardHeader>
        <CardTitle>Add a node</CardTitle>
      </CardHeader>
      <CardContent>
      <form
        className="flex flex-wrap items-end gap-3 text-xs"
        onSubmit={(e) => {
          e.preventDefault();
          setCopied(false);
          create.mutate();
        }}
      >
        <label className="flex flex-col gap-1">
          <span className="text-muted">Labels (comma separated)</span>
          <Input
            aria-label="Labels"
            value={labels}
            onChange={(e) => setLabels(e.target.value)}
            placeholder="linux/arm64, browser"
            className="w-64 font-mono"
          />
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-muted">TTL</span>
          <Input
            aria-label="TTL"
            type="number"
            min={1}
            value={ttl}
            onChange={(e) => setTtl(Number(e.target.value))}
            className="w-20"
          />
        </label>
        <label className="flex flex-col gap-1">
          <span className="text-muted">Unit</span>
          <select
            aria-label="TTL unit"
            value={unit}
            onChange={(e) => setUnit(e.target.value)}
            className="rounded border border-border bg-bg px-2 py-1 outline-none focus:border-accent"
          >
            {Object.keys(UNITS).map((u) => (
              <option key={u} value={u}>
                {u}
              </option>
            ))}
          </select>
        </label>
        <Button type="submit" size="sm" disabled={create.isPending}>
          {create.isPending ? "Creating…" : "Create enrollment token"}
        </Button>
      </form>

      {create.data ? (
        <div className="mt-3 space-y-2">
          <div className="flex items-center gap-3 text-xs">
            <span className="text-warn">
              Shown once. Expires {absolute(create.data.expiresAt)}.
            </span>
            <button
              type="button"
              onClick={() => {
                void navigator.clipboard?.writeText(command);
                setCopied(true);
              }}
              className="rounded border border-border px-2 py-0.5 hover:border-accent"
            >
              {copied ? "Copied" : "Copy"}
            </button>
          </div>
          <pre
            data-testid="enroll-command"
            className="overflow-x-auto rounded border border-border bg-bg p-2 font-mono text-xs whitespace-pre-wrap"
          >
            {command}
          </pre>
          {isTailnetServer(server) ? (
            <p className="text-xs text-muted">
              Run it on the new host with <code className="font-mono">TS_AUTHKEY</code> set to a
              reusable, pre-approved Tailscale auth key tagged{" "}
              <code className="font-mono">tag:podium-node</code>. That key is a Tailscale
              credential and is a different thing from the enrollment token above, which is
              Podium&apos;s own and single-use.
            </p>
          ) : (
            <p className="text-xs text-muted">
              Run it on the new host with <code className="font-mono">PODIUM_DEV_TOKEN</code> set
              to the same shared token this server was started with.
            </p>
          )}
        </div>
      ) : null}
      </CardContent>
    </Card>
  );
}
