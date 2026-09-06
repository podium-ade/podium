import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Eye, EyeOff, Lock } from "lucide-react";
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
import type { Secret } from "../../gen/podium/v1/secret_pb";
import { errorMessage, secrets } from "../../lib/client";
import { relative } from "../../lib/format";
import { cn } from "../../lib/utils";

/**
 * The server's own rule, copied from pkg/spec.SecretNameRE. It is narrow because a name can
 * end up as an environment variable on the node, so rejecting it here says the same thing the
 * API would, before the value has been typed.
 */
const NAME_RE = /^[A-Za-z_][A-Za-z0-9_.-]*$/;

/**
 * SetSecret behind a dialog, in one of two moods.
 *
 * With `lockedName` it is a rotation: the name is fixed, because a rotation that lands on a
 * typo silently creates a second secret and leaves the real one stale. Without one it is a new
 * secret — but the same call rotates an existing name, so typing a name that is already taken
 * turns the dialog into a rotation and says so before the button is pressed.
 *
 * The reveal toggle unmasks the draft in this field and nothing else. There is no stored value
 * to unmask: SetSecret is the only way a value ever moves, and it only moves inwards.
 */
export function SecretDialog({
  open,
  onOpenChange,
  existing,
  lockedName,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The current list, so a name that is already taken can be recognised as it is typed. */
  existing: Secret[];
  lockedName?: string;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState(lockedName ?? "");
  const [value, setValue] = useState("");
  const [reveal, setReveal] = useState(false);

  const locked = lockedName !== undefined;
  const trimmed = name.trim();
  const match = existing.find((s) => s.name === trimmed);
  const malformed = trimmed !== "" && !NAME_RE.test(trimmed);
  const flat = trimmed !== "" && !malformed && !trimmed.includes(".");

  const set = useMutation({
    mutationFn: () =>
      secrets.setSecret({ name: trimmed, value: new TextEncoder().encode(value) }),
    onSuccess: (res) => {
      const version = res.secret?.version ?? 0;
      toast(
        version > 1
          ? `${res.secret?.name} rotated to version ${version}.`
          : `${res.secret?.name} saved at version ${version}.`,
        "ok",
      );
      void qc.invalidateQueries({ queryKey: ["secrets"] });
      onOpenChange(false);
    },
    onError: (err) => toast(`SetSecret: ${errorMessage(err)}`),
  });

  const ready = trimmed !== "" && value !== "" && !malformed;
  const label = set.isPending
    ? "Saving…"
    : match
      ? `Rotate to version ${match.version + 1}`
      : "Save secret";

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{locked ? `Rotate ${lockedName}` : "New secret"}</DialogTitle>
          <DialogDescription>
            {locked
              ? "Only the value changes. Tasks scheduled after this get the new version; tasks already running keep the value they were assigned, and the one being replaced cannot be recovered."
              : "The value is written once and can never be read back. Keep your own copy of it somewhere you trust before you save it here."}
          </DialogDescription>
        </DialogHeader>

        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) set.mutate();
          }}
        >
          <div className="space-y-1.5">
            <Label htmlFor="secret-name">Name</Label>
            <div className="relative">
              <Input
                id="secret-name"
                value={name}
                onChange={(e) => setName(e.target.value)}
                readOnly={locked}
                autoFocus={!locked}
                aria-invalid={malformed}
                spellCheck={false}
                autoComplete="off"
                placeholder="github.deploy_token"
                className={cn(
                  "font-mono text-xs",
                  locked && "bg-raised pr-9 text-muted hover:border-input",
                )}
              />
              {locked ? (
                <Lock aria-hidden className="absolute top-2.5 right-3 size-4 text-faint" />
              ) : null}
            </div>
            {locked ? (
              <p className="text-2xs text-faint">
                Locked: a rotation that lands on a typo quietly creates a second secret and
                leaves this one stale.
                {match ? (
                  <>
                    {" "}
                    Version {match.version}, <span className="font-mono">{match.keyId}</span>,
                    last set {relative(match.updatedAt)}.
                  </>
                ) : null}
              </p>
            ) : malformed ? (
              <p className="text-2xs text-err">
                Letters, digits, dot, dash and underscore only, starting with a letter or an
                underscore. A name can become an environment variable on the node.
              </p>
            ) : (
              <p className="text-2xs text-faint">
                {flat
                  ? "Names are usually a dotted namespace, like github.deploy_token."
                  : "Task specs and the agent config reference this exact string."}
              </p>
            )}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="secret-value">Value</Label>
            <div className="relative">
              <Input
                id="secret-value"
                type={reveal ? "text" : "password"}
                value={value}
                onChange={(e) => setValue(e.target.value)}
                autoFocus={locked}
                spellCheck={false}
                autoComplete="off"
                className="pr-10 font-mono text-xs"
              />
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={reveal ? "Hide value" : "Show value"}
                onClick={() => setReveal((r) => !r)}
                className="absolute top-0.5 right-0.5"
              >
                {reveal ? <EyeOff /> : <Eye />}
              </Button>
            </div>
            <p className="text-2xs text-faint">
              Sent verbatim as bytes. A trailing newline is part of the secret — the CLI strips
              one from stdin, the API strips nothing.
            </p>
          </div>

          {match && !locked ? (
            <Alert variant="warn" title={`${match.name} already exists`}>
              It is at version {match.version}, last set {relative(match.updatedAt)}. Saving
              rotates it to version {match.version + 1} — this replaces the stored value rather
              than editing it, and the old one is gone.
            </Alert>
          ) : null}

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline" size="sm">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" size="sm" disabled={!ready || set.isPending}>
              {label}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
