import { useEffect, useRef, useState } from "react";
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
import { Textarea } from "../ui/textarea";
import { SecretScope, type Secret } from "../../gen/podium/v1/secret_pb";
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

const fieldLabel = "text-sm font-bold text-fg";

type FolderFile = { file: File; name: string };

function fileProblem(file: File): string | undefined {
  if (file.size === 0) return `${file.name} is empty, and a secret with no value is refused.`;
  if (file.size > MAX_FILE_BYTES) {
    return `${file.name} is ${humanBytes(file.size)}. A secret is a credential, not a payload. Choose the key file itself.`;
  }
  return undefined;
}

function folderPath(file: File): { folder: string; name: string } {
  const bits = (file.webkitRelativePath || file.name).split(/[/\\]/);
  if (bits.length > 1) return { folder: bits[0], name: bits.slice(1).join("/") };
  return { folder: "folder", name: file.name };
}

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
 * A value can also come from a file in a chosen folder, which is the way to put a PEM, an SSH
 * key, or a JSON credential in here. The file is read in the browser and sent byte for byte.
 * Its contents are not shown. Paste and folder are separate: two sources for one value would
 * only ever be a way to store the wrong one.
 */
export function SecretDialog({
  open,
  onOpenChange,
  existing,
  lockedName,
  scope = SecretScope.GLOBAL,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The current list, so a name that is already taken can be recognised as it is typed. */
  existing: Secret[];
  lockedName?: string;
  /** Global is the company list. Personal is the signed-in person's own. */
  scope?: SecretScope;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState(lockedName ?? "");
  const [value, setValue] = useState("");
  const [reveal, setReveal] = useState(false);
  const [source, setSource] = useState<"paste" | "folder">("paste");
  const [folderName, setFolderName] = useState("");
  const [folderFiles, setFolderFiles] = useState<FolderFile[]>([]);
  const [folderIndex, setFolderIndex] = useState(0);
  const [fileError, setFileError] = useState("");
  const folderRef = useRef<HTMLInputElement>(null);

  const locked = lockedName !== undefined;
  const trimmed = name.trim();
  const match = existing.find((s) => s.name === trimmed && (s.scope === scope || s.scope === undefined));
  const malformed = trimmed !== "" && !NAME_RE.test(trimmed);
  const flat = trimmed !== "" && !malformed && !trimmed.includes(".");
  const usingFolder = source === "folder";
  const chosen = usingFolder ? folderFiles[folderIndex] : undefined;
  const other = existing.find((s) => s.name === trimmed && s.scope !== scope && s.scope !== undefined);

  useEffect(() => {
    const input = folderRef.current;
    if (!input) return;
    input.setAttribute("webkitdirectory", "");
    input.setAttribute("directory", "");
  }, []);

  const set = useMutation({
    mutationFn: async () =>
      secrets.setSecret({
        name: trimmed,
        scope,
        value: chosen
          ? new Uint8Array(await chosen.file.arrayBuffer())
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

  const ready =
    trimmed !== "" &&
    !malformed &&
    !fileError &&
    (usingFolder ? chosen !== undefined : value !== "");
  const label = set.isPending
    ? "Saving…"
    : match
      ? `Rotate to version ${match.version + 1}`
      : "Set secret";

  function clearFolder() {
    setFolderName("");
    setFolderFiles([]);
    setFolderIndex(0);
    setFileError("");
    if (folderRef.current) folderRef.current.value = "";
  }

  function takeFolder(list: FileList | null) {
    const files = list ? [...list] : [];
    setFileError("");
    setFolderFiles([]);
    setFolderIndex(0);
    setValue("");
    if (!files.length) return;
    const mapped = files
      .map((file) => ({ file, name: folderPath(file).name }))
      .sort((a, b) => a.name.localeCompare(b.name));
    setFolderName(folderPath(files[0]).folder);
    setFolderFiles(mapped);
    setFileError(fileProblem(mapped[0].file) ?? "");
  }

  function chooseFolderIndex(index: number) {
    setFolderIndex(index);
    const next = folderFiles[index];
    setFileError(next ? (fileProblem(next.file) ?? "") : "");
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100vh-2rem)] max-w-[640px] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="text-2xl leading-tight font-bold tracking-normal">
            {locked ? "Set a new secret" : "New secret"}
          </DialogTitle>
          <DialogDescription className="text-base text-soft">
            {locked
              ? `Set a new secret for ${lockedName}. The current value cannot be read.`
              : "Set the value by pasting it or by choosing a folder. Once it is saved, the value cannot be read back."}
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
            <Label htmlFor="secret-name" className={fieldLabel}>
              Name
            </Label>
            <Input
              id="secret-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              readOnly={locked}
              autoFocus={!locked}
              aria-invalid={malformed}
              spellCheck={false}
              autoComplete="off"
              autoCapitalize="off"
              placeholder="github.deploy_token"
              className={cn("font-mono text-xs", locked && "bg-raised text-fg hover:border-input")}
            />
            {malformed ? (
              <p className="text-sm font-semibold text-err">
                Letters, digits, dot, dash and underscore only, starting with a letter or an
                underscore. A name can become an environment variable on the node.
              </p>
            ) : (
              <p className="text-sm text-muted">
                {locked
                  ? "This name stays fixed. The current value cannot be read."
                  : other
                    ? `A ${other.scope === SecretScope.PERSONAL ? "personal" : "global"} secret already uses this name. This save writes the ${scope === SecretScope.PERSONAL ? "personal" : "global"} one.`
                    : flat
                      ? "Names are usually a dotted namespace, like github.deploy_token."
                      : "Task specs and the agent config reference this exact string."}
                {locked && match ? (
                  <>
                    {" "}
                    Version {match.version}, <span className="font-mono">{match.keyId}</span>, last
                    set {relative(match.updatedAt)}.
                  </>
                ) : null}
              </p>
            )}
          </div>

          <div className="grid gap-2">
            <span id="secret-source-label" className="text-sm font-bold text-fg">
              How to set it
            </span>
            <div className="grid grid-cols-2 gap-2" role="group" aria-labelledby="secret-source-label">
              <button
                type="button"
                aria-pressed={source === "paste"}
                className={cn(
                  "min-h-11 rounded-md border border-line-strong bg-panel px-3 text-sm font-bold text-fg",
                  "hover:border-fg hover:bg-raised",
                  source === "paste" && "border-fg bg-raised",
                )}
                onClick={() => {
                  if (source !== "paste") {
                    setSource("paste");
                    clearFolder();
                  }
                  document.getElementById("secret-value")?.focus();
                }}
              >
                Paste a secret
              </button>
              <button
                type="button"
                id="secret-source-folder"
                aria-pressed={source === "folder"}
                className={cn(
                  "min-h-11 rounded-md border border-line-strong bg-panel px-3 text-sm font-bold text-fg",
                  "hover:border-fg hover:bg-raised",
                  source === "folder" && "border-fg bg-raised",
                )}
                onClick={() => {
                  setSource("folder");
                  folderRef.current?.click();
                }}
              >
                Choose a folder
              </button>
            </div>
            <input
              ref={folderRef}
              id="secret-folder"
              type="file"
              multiple
              tabIndex={-1}
              aria-hidden
              className="hidden"
              onChange={(e) => {
                setSource("folder");
                takeFolder(e.target.files);
              }}
            />
          </div>

          {usingFolder ? (
            <div className="grid gap-2">
              {folderFiles.length > 1 ? (
                <div className="grid gap-1.5">
                  <Label htmlFor="secret-folder-file" className={fieldLabel}>
                    File in this folder
                  </Label>
                  <select
                    id="secret-folder-file"
                    className="min-h-11 rounded-md border border-input bg-panel px-3 font-mono text-xs text-fg"
                    value={String(folderIndex)}
                    onChange={(e) => chooseFolderIndex(Number(e.target.value))}
                  >
                    {folderFiles.map((item, index) => (
                      <option key={item.name} value={index}>
                        {item.name}
                      </option>
                    ))}
                  </select>
                </div>
              ) : null}
              {fileError ? (
                <p className="text-sm font-semibold text-err">{fileError}</p>
              ) : (
                <p className="text-sm text-muted">
                  {chosen ? (
                    `${folderName} / ${chosen.name}, ${humanBytes(chosen.file.size)}. The file is the new secret. Its contents are not shown.`
                  ) : (
                    <>
                      Choose a folder above. An SSH key, a PEM, or a JSON credential in that
                      folder is the new secret. Its contents are not shown. Same bytes as{" "}
                      <code className="font-mono">podium secret set --from-file</code>.
                    </>
                  )}
                </p>
              )}
              {folderFiles.length > 0 || fileError ? (
                <Button type="button" variant="ghost" className="justify-self-start" onClick={clearFolder}>
                  Clear folder
                </Button>
              ) : null}
            </div>
          ) : (
            <div className="grid gap-1.5">
              <div className="flex items-center justify-between gap-2">
                <Label htmlFor="secret-value" className={fieldLabel}>
                  Secret
                </Label>
                <Button
                  type="button"
                  variant="ghost"
                  size="sm"
                  aria-pressed={reveal}
                  onClick={() => setReveal((r) => !r)}
                >
                  {reveal ? "Hide paste" : "Show paste"}
                </Button>
              </div>
              <Textarea
                id="secret-value"
                value={value}
                onChange={(e) => setValue(e.target.value)}
                autoFocus={locked}
                spellCheck={false}
                autoComplete="off"
                rows={4}
                aria-describedby="secret-value-help"
                className={cn("min-h-24 font-mono text-sm", !reveal && "[-webkit-text-security:disc]")}
              />
              <p id="secret-value-help" className="text-sm text-muted">
                This shows only what you paste. The stored value cannot be read. A trailing newline
                is part of the secret.
              </p>
            </div>
          )}

          {match && !locked ? (
            <Alert variant="warn" title={`${match.name} already exists`}>
              It is at version {match.version}, last set {relative(match.updatedAt)}. Saving
              rotates it to version {match.version + 1}. This replaces the stored value rather
              than editing it, and the old one is gone.
            </Alert>
          ) : null}

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" disabled={!ready || set.isPending}>
              {label}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}
