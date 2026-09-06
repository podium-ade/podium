import { useState } from "react";
import { useMutation } from "@tanstack/react-query";
import { Check, Copy, Plus } from "lucide-react";
import { admin, errorMessage } from "../lib/client";
import { enrollCommand, isTailnetServer } from "../lib/enroll";
import { absolute } from "../lib/format";
import { Chip } from "./Badge";
import { Alert } from "./ui/alert";
import { Button } from "./ui/button";
import {
  Dialog,
  DialogClose,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "./ui/dialog";
import { Input } from "./ui/input";
import { Label } from "./ui/label";
import { ToggleGroup, ToggleGroupItem } from "./ui/toggle-group";

/** The TTLs an operator actually wants. A token is redeemed minutes after it is minted. */
const TTLS: { value: string; label: string; seconds: number }[] = [
  { value: "15m", label: "15 minutes", seconds: 900 },
  { value: "1h", label: "1 hour", seconds: 3600 },
  { value: "8h", label: "8 hours", seconds: 28800 },
  { value: "24h", label: "24 hours", seconds: 86400 },
];

function parseLabels(raw: string): string[] {
  return raw
    .split(",")
    .map((l) => l.trim())
    .filter((l) => l !== "");
}

/**
 * EnrollPanel is the whole "add a machine to the fleet" flow: pick labels and a lifetime, mint
 * the token, then read off the one line to paste on the new host.
 *
 * The enrollment token is single-use and the server keeps only its SHA-256, so it is shown once
 * and can never be re-fetched — which is why the minted state is a step of its own rather than
 * a line appended under a form the operator might navigate away from.
 */
export function EnrollPanel({ server = window.location.origin }: { server?: string }) {
  const [open, setOpen] = useState(false);
  const [labels, setLabels] = useState("");
  const [ttl, setTtl] = useState("1h");
  const [copied, setCopied] = useState(false);

  const create = useMutation({
    mutationFn: () =>
      admin.createEnrollmentToken({
        labels: parseLabels(labels),
        ttl: {
          seconds: BigInt(TTLS.find((t) => t.value === ttl)?.seconds ?? 3600),
          nanos: 0,
        },
      }),
  });

  const command = create.data ? enrollCommand(server, create.data.token) : "";
  const minted = parseLabels(labels);

  function onOpenChange(next: boolean) {
    setOpen(next);
    if (!next) {
      // Reopening must not re-show a spent token, and the form should start clean.
      create.reset();
      setLabels("");
      setTtl("1h");
      setCopied(false);
    }
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogTrigger asChild>
        <Button size="sm">
          <Plus />
          Add a node
        </Button>
      </DialogTrigger>

      <DialogContent className="max-w-xl">
        {create.data ? (
          <>
            <DialogHeader>
              <DialogTitle>Run this on the new machine</DialogTitle>
              <DialogDescription>
                The token below is Podium&apos;s own and it enrolls exactly one machine. Here is
                everything that machine needs.
              </DialogDescription>
            </DialogHeader>

            <Alert variant="warn">
              Shown once, single use — the server keeps nothing but its SHA-256, so it cannot be
              shown again. Expires {absolute(create.data.expiresAt)}. If you lose it or it
              expires, mint another.
            </Alert>

            <ol className="space-y-4 text-xs">
              <li className="space-y-1">
                <p className="font-medium text-fg">
                  1. Put <span className="font-mono">podium-node</span> on the machine
                </p>
                <p className="leading-relaxed text-muted">
                  It needs a Docker engine it can talk to (API 1.43+, cgroup v2) and outbound
                  network to this control plane. It never listens for inbound connections, so
                  there are no ports to open.
                </p>
              </li>

              <li className="space-y-2">
                <p className="font-medium text-fg">2. Run this as one line</p>
                <div className="overflow-hidden rounded-lg border border-border bg-bg">
                  <div className="flex items-center justify-between gap-2 border-b border-hairline px-3 py-1.5">
                    <span className="text-2xs text-faint">shell, on the new machine</span>
                    <Button
                      variant="ghost"
                      size="xs"
                      onClick={() => {
                        void navigator.clipboard?.writeText(command);
                        setCopied(true);
                      }}
                    >
                      {copied ? <Check className="text-ok" /> : <Copy />}
                      {copied ? "Copied" : "Copy"}
                    </Button>
                  </div>
                  <pre
                    data-testid="enroll-command"
                    className="px-3 py-2.5 font-mono text-xs leading-relaxed break-words whitespace-pre-wrap text-fg"
                  >
                    {command}
                  </pre>
                </div>
                {isTailnetServer(server) ? (
                  <p className="leading-relaxed text-muted">
                    <span className="font-mono">$TS_AUTHKEY</span> has to be set in that shell to
                    a reusable, pre-approved Tailscale auth key tagged{" "}
                    <span className="font-mono">tag:podium-node</span>. That key is a Tailscale
                    credential and gets the daemon onto your tailnet; the enrollment token above
                    is Podium&apos;s and tells Podium which node this is. You need both, and
                    neither substitutes for the other.
                  </p>
                ) : (
                  <p className="leading-relaxed text-muted">
                    <span className="font-mono">$PODIUM_DEV_TOKEN</span> has to be set in that
                    shell to the same shared token this server was started with. The dev
                    transport is loopback-only: it works when the node and the server are the
                    same machine, and cannot reach a node anywhere else.
                  </p>
                )}
              </li>

              <li className="space-y-1">
                <p className="font-medium text-fg">3. Watch for it in this list</p>
                <p className="leading-relaxed text-muted">
                  The node enrolls on its first connection and appears here within a few seconds,
                  online and with its capacity filled in.
                  {minted.length > 0 ? " It carries the labels you chose:" : null}
                </p>
                {minted.length > 0 ? (
                  <div className="flex flex-wrap gap-1 pt-0.5">
                    {minted.map((l) => (
                      <Chip key={l} className="font-mono">
                        {l}
                      </Chip>
                    ))}
                  </div>
                ) : null}
              </li>
            </ol>

            <DialogFooter>
              <DialogClose asChild>
                <Button size="sm">Done</Button>
              </DialogClose>
            </DialogFooter>
          </>
        ) : (
          <form
            className="contents"
            onSubmit={(e) => {
              e.preventDefault();
              setCopied(false);
              create.mutate();
            }}
          >
            <DialogHeader>
              <DialogTitle>Add a node</DialogTitle>
              <DialogDescription>
                A node is any machine with a Docker engine that dials this control plane and runs
                tasks on it. Enrolling one is two steps: mint a single-use token here, then run
                one command on the machine.
              </DialogDescription>
            </DialogHeader>

            <div className="space-y-1.5">
              <Label htmlFor="enroll-labels">Labels</Label>
              <Input
                id="enroll-labels"
                aria-label="Labels"
                value={labels}
                onChange={(e) => setLabels(e.target.value)}
                placeholder="linux/amd64, gpu"
                className="font-mono"
              />
              <p className="text-2xs leading-relaxed text-faint">
                Comma separated, and baked into the token. Labels are the only thing scheduling
                matches on: a task that requires <span className="font-mono">gpu</span> is placed
                only on a node carrying that label. Leave this empty to set{" "}
                <span className="font-mono">PODIUM_NODE_LABELS</span> on the machine instead.
              </p>
            </div>

            <div className="space-y-1.5">
              <Label id="enroll-ttl-label">Token expires in</Label>
              <ToggleGroup
                type="single"
                value={ttl}
                onValueChange={(v) => v && setTtl(v)}
                aria-labelledby="enroll-ttl-label"
              >
                {TTLS.map((t) => (
                  <ToggleGroupItem key={t.value} value={t.value}>
                    {t.label}
                  </ToggleGroupItem>
                ))}
              </ToggleGroup>
              <p className="text-2xs leading-relaxed text-faint">
                How long the machine has to redeem it. Enrolling one machine spends it either
                way.
              </p>
            </div>

            {create.error ? (
              <Alert variant="destructive" title="Could not create an enrollment token">
                {errorMessage(create.error)}. Minting one needs admin rights on this control
                plane — check the token this console is using.
              </Alert>
            ) : null}

            <DialogFooter>
              <DialogClose asChild>
                <Button type="button" variant="outline" size="sm">
                  Cancel
                </Button>
              </DialogClose>
              <Button type="submit" size="sm" disabled={create.isPending}>
                {create.isPending ? "Creating…" : "Create enrollment token"}
              </Button>
            </DialogFooter>
          </form>
        )}
      </DialogContent>
    </Dialog>
  );
}
