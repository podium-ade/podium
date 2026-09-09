import { useRef, useState } from "react";
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
import { humanBytes, relative } from "../../lib/format";
import { cn } from "../../lib/utils";

/**
 * The server's own rule, copied from pkg/spec.SecretNameRE. It is narrow because a name can
 * end up as an environment variable on the node, so rejecting it here says the same thing the
 * API would, before the value has been typed.
 */
const NAME_RE = /^[A-Za-z_][A-Za-z0-9_.-]*$/;

/**
 * The largest file this dialog will read. It is NOT a limit the API has — `SetSecret` caps
 * nothing and `--from-file` reads whatever it is pointed at — it is a guard against the wrong
 * file. Every credential worth storing is a few kilobytes; a megabyte means the picker landed
 * on a disk image, and reading that into the tab helps nobody.
 */
const MAX_FILE_BYTES = 1024 * 1024;

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
 *
 * A value can also come from a **file**, which is the only sane way to put a PEM, an SSH key or
 * a JSON credential in here — pasting one into a single-line field mangles it. The file is read
 * in the browser and sent byte for byte, exactly as `podium secret set --from-file` does, so a
 * key written by ssh-keygen arrives as the bytes ssh-keygen wrote. It wins over anything typed:
 * two sources for one value would only ever be a way to store the wrong one.
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
  const [file, setFile] = useState<File>();
  const [fileError, setFileError] = useState<string>();
  const fileInput = useRef<HTMLInputElement>(null);

  const locked = lockedName !== undefined;
  const trimmed = name.trim();
  const match = existing.find((s) => s.name === trimmed);
  const malformed = trimmed !== "" && !NAME_RE.test(trimmed);
  const flat = trimmed !== "" && !malformed && !trimmed.includes(".");

  // A file is read here rather than when it is picked: holding a credential in component state
  // for as long as the dialog is open buys nothing, and the File itself is a handle, not bytes.
  const set = useMutation({
    mutationFn: async () =>
      secrets.setSecret({
        name: trimmed,
        value: file
          ? new Uint8Array(await file.arrayBuffer())
          : new TextEncoder().encode(value),
      }),
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

  const ready = trimmed !== "" && (value !== "" || file !== undefined) && !malformed;

  /**
   * Both refusals name the file, and both clear the picker: an input still showing a filename
   * the dialog has rejected is an input that says a file is selected when none is.
   */
  function pick(chosen: File | undefined) {
    setFileError(undefined);
    setFile(undefined);
    if (!chosen) return;
    if (chosen.size === 0) {
      setFileError(`${chosen.name} is empty, and a secret with no value is refused.`);
    } else if (chosen.size > MAX_FILE_BYTES) {
      setFileError(
        `${chosen.name} is ${humanBytes(chosen.size)}. A secret is a credential, not a payload — ` +
          `pick the key file itself.`,
      );
    } else {
      setFile(chosen);
      return;
    }
    if (fileInput.current) fileInput.current.value = "";
  }
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
                disabled={file !== undefined}
                spellCheck={false}
                autoComplete="off"
                className="pr-10 font-mono text-xs"
              />
              <Button
                type="button"
                variant="ghost"
                size="icon-sm"
                aria-label={reveal ? "Hide value" : "Show value"}
                disabled={file !== undefined}
                onClick={() => setReveal((r) => !r)}
                className="absolute top-0.5 right-0.5"
              >
                {reveal ? <EyeOff /> : <Eye />}
              </Button>
            </div>
            <p className="text-2xs text-faint">
              {file
                ? "The file below is the value. Clear it to type one instead."
                : "Sent verbatim as bytes. A trailing newline is part of the secret — the CLI strips one from stdin, the API strips nothing."}
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="secret-file">Or from a file</Label>
            <Input
              id="secret-file"
              ref={fileInput}
              type="file"
              disabled={set.isPending}
              onChange={(e) => pick(e.target.files?.[0])}
            />
            {fileError ? (
              <p className="text-2xs text-err">{fileError}</p>
            ) : file ? (
              <p className="text-2xs leading-relaxed text-faint">
                <span className="font-mono text-muted">{file.name}</span>,{" "}
                <span className="tabular">{humanBytes(file.size)}</span>, sent byte for byte. A
                trailing newline in the file is part of the secret — nothing here strips one.
              </p>
            ) : (
              <p className="text-2xs leading-relaxed text-faint">
                An SSH key, a PEM or a JSON credential — the shape that does not survive being
                pasted into the field above. Read in this browser and sent byte for byte, the same
                as <code className="font-mono">podium secret set --from-file</code>.
              </p>
            )}
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
