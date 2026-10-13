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
import type { Registry } from "../../gen/podium/v1/registry_pb";
import { errorMessage, registries } from "../../lib/client";
import { relative } from "../../lib/format";
import { cn } from "../../lib/utils";

/**
 * The registries an operator is likely to be adding, and what each one wants for a login.
 * "other" is any registry that takes a plain username and password or token.
 */
type Kind = "gar" | "ghcr" | "other";

const KINDS: Record<
  Kind,
  { label: string; hostPlaceholder: string; username?: string; passwordLabel: string; hint: string }
> = {
  gar: {
    label: "Google Artifact Registry",
    hostPlaceholder: "us-docker.pkg.dev",
    username: "_json_key",
    passwordLabel: "Service account key (JSON)",
    hint: "Paste the whole key file of a service account that has Artifact Registry Reader on the repository. The host is the regional one your image references start with; europe-docker.pkg.dev is a separate registry.",
  },
  ghcr: {
    label: "GitHub Container Registry",
    hostPlaceholder: "ghcr.io",
    passwordLabel: "Personal access token",
    hint: "A classic token with read:packages, or a fine-grained one with access to the packages.",
  },
  other: {
    label: "Other",
    hostPlaceholder: "registry.example.com",
    passwordLabel: "Password or token",
    hint: "Whatever `docker login` would take for this registry.",
  },
};

/** A service-account key is a credential, not a payload. */
const MAX_FILE_BYTES = 1024 * 1024;

/** The server's own rule, from pkg/spec.RegistryHostRE, after its normalisation. */
const HOST_RE = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$/;

const fieldLabel = "text-sm font-bold text-fg";

function normalizeHost(raw: string): string {
  let host = raw.trim().toLowerCase();
  host = host.replace(/^https?:\/\//, "");
  const slash = host.indexOf("/");
  if (slash >= 0) host = host.slice(0, slash);
  if (host === "index.docker.io" || host === "registry-1.docker.io") return "docker.io";
  return host;
}

function kindOf(host: string): Kind {
  if (host.endsWith("pkg.dev") || host === "gcr.io" || host.endsWith(".gcr.io")) return "gar";
  if (host === "ghcr.io") return "ghcr";
  return "other";
}

function humanBytes(size: number): string {
  if (size < 1024) return `${size} B`;
  if (size < 1024 * 1024) return `${Math.round(size / 102.4) / 10} KB`;
  return `${Math.round(size / (1024 * 102.4)) / 10} MB`;
}

/** Read a key file as text. FileReader, not Blob.text: the test DOM's File has no text(). */
function readText(file: Blob): Promise<string> {
  return new Promise((resolve, reject) => {
    const reader = new FileReader();
    reader.addEventListener("load", () => resolve(typeof reader.result === "string" ? reader.result : ""));
    reader.addEventListener("error", () => reject(reader.error ?? new Error("Could not read the file.")));
    reader.readAsText(file);
  });
}

/**
 * SetRegistry behind a dialog. With `lockedHost` it replaces the login of a registry that is
 * already there: the host is fixed, and the username is prefilled because it is not a secret.
 * The password never is — there is nothing stored to prefill it from.
 */
export function RegistryDialog({
  open,
  onOpenChange,
  existing,
  lockedHost,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  existing: Registry[];
  lockedHost?: string;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const lockedRow = existing.find((r) => r.host === lockedHost);
  const [kind, setKind] = useState<Kind>(lockedHost ? kindOf(lockedHost) : "gar");
  const [host, setHost] = useState(lockedHost ?? "");
  const [username, setUsername] = useState(lockedRow?.username ?? KINDS[kind].username ?? "");
  const [password, setPassword] = useState("");
  const [reveal, setReveal] = useState(false);
  const [source, setSource] = useState<"paste" | "file">("paste");
  const [fileSecret, setFileSecret] = useState("");
  const [fileMeta, setFileMeta] = useState<{ name: string; size: number } | null>(null);
  const [fileError, setFileError] = useState("");
  const fileRef = useRef<HTMLInputElement>(null);
  const readGen = useRef(0);
  const pendingFocus = useRef<"paste" | "clear" | null>(null);

  const locked = lockedHost !== undefined;
  const normalized = normalizeHost(host);
  const malformed = host.trim() !== "" && !HOST_RE.test(normalized);
  const match = existing.find((r) => r.host === normalized);
  const preset = KINDS[kind];
  const multiline = kind === "gar";
  const usingFile = multiline && source === "file";
  const secret = usingFile ? fileSecret : password;

  const set = useMutation({
    mutationFn: () =>
      registries.setRegistry({
        host: normalized,
        username: username.trim(),
        password: new TextEncoder().encode(secret),
      }),
    onSuccess: (res) => {
      toast(`${res.registry?.host} saved.`, "ok");
      void qc.invalidateQueries({ queryKey: ["registries"] });
      onOpenChange(false);
    },
    onError: (err) => toast(`SetRegistry: ${errorMessage(err)}`),
  });

  const ready =
    normalized !== "" && !malformed && username.trim() !== "" && secret !== "" && !(usingFile && fileError);
  const label = set.isPending ? "Saving…" : match ? "Replace login" : "Save registry";

  useEffect(() => {
    if (pendingFocus.current === "paste" && !usingFile) {
      pendingFocus.current = null;
      document.getElementById("registry-password")?.focus();
    } else if (pendingFocus.current === "clear") {
      pendingFocus.current = null;
      document.getElementById("registry-source-file")?.focus();
    }
  }, [usingFile, fileMeta, fileError]);

  function chooseKind(next: Kind) {
    setKind(next);
    // A preset username is a fact about the registry, not a choice; a free-text one is kept.
    if (KINDS[next].username) setUsername(KINDS[next].username);
    else if (username === KINDS[kind].username) setUsername("");
  }

  function clearFile() {
    readGen.current += 1;
    setFileMeta(null);
    setFileError("");
    setFileSecret("");
    if (fileRef.current) fileRef.current.value = "";
  }

  function onFile(chosen: File | undefined) {
    const gen = ++readGen.current;
    if (chosen) setSource("file");
    setFileMeta(null);
    setFileError("");
    setFileSecret("");
    if (!chosen) return;
    if (chosen.size === 0) {
      setFileError(`${chosen.name} is empty, and a registry password with no value is refused.`);
      if (fileRef.current) fileRef.current.value = "";
      return;
    }
    if (chosen.size > MAX_FILE_BYTES) {
      setFileError(
        `${chosen.name} is ${humanBytes(chosen.size)}. A service account key is a credential, not a payload. Choose the JSON key itself.`,
      );
      if (fileRef.current) fileRef.current.value = "";
      return;
    }
    void readText(chosen).then(
      (text) => {
        if (gen !== readGen.current) return;
        setFileSecret(text);
        setFileMeta({ name: chosen.name, size: chosen.size });
        setPassword("");
      },
      () => {
        if (gen !== readGen.current) return;
        setFileError(`${chosen.name} could not be read.`);
        if (fileRef.current) fileRef.current.value = "";
      },
    );
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-h-[calc(100vh-2rem)] max-w-[640px] overflow-y-auto">
        <DialogHeader>
          <DialogTitle className="text-2xl leading-tight font-bold tracking-normal">
            {locked ? `Replace the login for ${lockedHost}` : "New registry"}
          </DialogTitle>
          <DialogDescription className="text-base text-soft">
            {locked
              ? "Tasks assigned after this pull with the new login. Tasks already running keep the one they were assigned. The password being replaced cannot be recovered."
              : "The password is written once and can never be read back. It leaves the server only inside an assignment, to a node about to pull an image from this registry."}
          </DialogDescription>
        </DialogHeader>

        <form
          className="grid gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) set.mutate();
          }}
        >
          <fieldset className="m-0 min-w-0 border-0 p-0">
            <legend className="mb-2 text-sm font-bold text-fg">Kind</legend>
            <div className="flex flex-wrap gap-2" role="radiogroup" aria-label="Registry kind">
              {(Object.keys(KINDS) as Kind[]).map((k) => (
                <label
                  key={k}
                  className={cn(
                    "relative inline-flex min-h-11 cursor-pointer items-center rounded-md border border-line-strong bg-panel px-3 py-2 font-semibold text-fg",
                    "hover:border-muted hover:bg-raised",
                    kind === k && "border-fg bg-raised",
                  )}
                >
                  <input
                    type="radio"
                    name="registry-kind"
                    value={k}
                    checked={kind === k}
                    onChange={() => chooseKind(k)}
                    className="absolute inset-0 m-0 cursor-pointer opacity-0"
                  />
                  <span>{KINDS[k].label}</span>
                </label>
              ))}
            </div>
          </fieldset>

          <div className="grid gap-1.5">
            <Label htmlFor="registry-host" className={fieldLabel}>
              Host
            </Label>
            <Input
              id="registry-host"
              value={host}
              onChange={(e) => setHost(e.target.value)}
              readOnly={locked}
              autoFocus={!locked}
              aria-invalid={malformed}
              aria-describedby={malformed ? "registry-host-error" : "registry-host-help"}
              spellCheck={false}
              autoComplete="off"
              autoCapitalize="off"
              placeholder={preset.hostPlaceholder}
              className={cn("font-mono text-xs", locked && "bg-raised text-fg hover:border-input")}
            />
            {malformed ? (
              <p id="registry-host-error" className="text-sm font-semibold text-err">
                The part of an image reference before its first slash, like{" "}
                <span className="font-mono">us-docker.pkg.dev</span>. No scheme and no path.
              </p>
            ) : (
              <p id="registry-host-help" className="text-sm text-muted">
                {normalized !== "" && normalized !== host.trim()
                  ? `Saved as ${normalized}.`
                  : "Every image pulled from this host, in any task or playbook, uses this login. One login per host."}
              </p>
            )}
          </div>

          <div className="grid gap-1.5">
            <Label htmlFor="registry-username" className={fieldLabel}>
              Username
            </Label>
            <Input
              id="registry-username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              readOnly={preset.username !== undefined}
              spellCheck={false}
              autoComplete="off"
              autoCapitalize="off"
              aria-describedby="registry-username-help"
              className={cn(
                "font-mono text-xs",
                preset.username !== undefined && "bg-raised text-fg hover:border-input",
              )}
            />
            <p id="registry-username-help" className="text-sm text-muted">
              {preset.username !== undefined
                ? "Google Artifact Registry uses the username _json_key."
                : "Whatever docker login would use for this registry."}
            </p>
          </div>

          {multiline ? (
            <div className="grid gap-2">
              <span id="registry-source-label" className="text-sm font-bold text-fg">
                How to set it
              </span>
              <div
                className="grid grid-cols-2 gap-2"
                role="group"
                aria-labelledby="registry-source-label"
              >
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
                      pendingFocus.current = "paste";
                      setSource("paste");
                      clearFile();
                    } else {
                      document.getElementById("registry-password")?.focus();
                    }
                  }}
                >
                  Paste JSON
                </button>
                <button
                  type="button"
                  id="registry-source-file"
                  aria-pressed={source === "file"}
                  className={cn(
                    "min-h-11 rounded-md border border-line-strong bg-panel px-3 text-sm font-bold text-fg",
                    "hover:border-fg hover:bg-raised",
                    source === "file" && "border-fg bg-raised",
                  )}
                  onClick={() => {
                    setSource("file");
                    fileRef.current?.click();
                  }}
                >
                  Choose a file
                </button>
              </div>
              <input
                ref={fileRef}
                id="registry-file"
                type="file"
                accept=".json,application/json"
                tabIndex={-1}
                aria-hidden
                className="hidden"
                onChange={(e) => onFile(e.target.files?.[0])}
              />
            </div>
          ) : null}

          {usingFile ? (
            <div className="grid gap-2">
              {fileError ? (
                <p className="text-sm font-semibold text-err">{fileError}</p>
              ) : (
                <p className="text-sm text-muted">
                  {fileMeta
                    ? `${fileMeta.name}, ${humanBytes(fileMeta.size)}. The file is the password. Its contents are not shown.`
                    : "Choose the JSON key of a service account with Artifact Registry Reader. The file is the password, and its contents are not shown."}
                </p>
              )}
              {fileMeta || fileError ? (
                <Button
                  type="button"
                  variant="ghost"
                  className="justify-self-start"
                  onClick={() => {
                    pendingFocus.current = "clear";
                    clearFile();
                  }}
                >
                  Clear file
                </Button>
              ) : null}
            </div>
          ) : (
            <div className="grid gap-1.5">
              <div className="flex items-center justify-between gap-2">
                <Label htmlFor="registry-password" className={fieldLabel}>
                  {preset.passwordLabel}
                </Label>
                {multiline ? null : (
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    aria-pressed={reveal}
                    onClick={() => setReveal((r) => !r)}
                  >
                    {reveal ? "Hide password" : "Show password"}
                  </Button>
                )}
              </div>
              {multiline ? (
                <Textarea
                  id="registry-password"
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoFocus={locked}
                  spellCheck={false}
                  autoComplete="off"
                  rows={6}
                  placeholder='{ "type": "service_account", … }'
                  aria-describedby="registry-password-help"
                  className="min-h-24 font-mono text-sm [-webkit-text-security:disc]"
                />
              ) : (
                <Input
                  id="registry-password"
                  type={reveal ? "text" : "password"}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoFocus={locked}
                  spellCheck={false}
                  autoComplete="new-password"
                  aria-describedby="registry-password-help"
                  className="font-mono text-xs"
                />
              )}
              <p id="registry-password-help" className="text-sm text-muted">
                {preset.hint}
              </p>
            </div>
          )}

          {match && !locked ? (
            <Alert variant="warn" title={`${match.host} already has a login`}>
              Saving replaces it (user <span className="font-mono">{match.username}</span>, last
              set {relative(match.updatedAt)}) rather than adding a second one. A registry has
              one login.
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
