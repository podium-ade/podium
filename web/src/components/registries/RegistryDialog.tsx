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
import { Textarea } from "../ui/textarea";
import { ToggleGroup, ToggleGroupItem } from "../ui/toggle-group";
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

/** The server's own rule, from pkg/spec.RegistryHostRE, after its normalisation. */
const HOST_RE = /^[a-z0-9]([a-z0-9.-]*[a-z0-9])?(:[0-9]{1,5})?$/;

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

  const locked = lockedHost !== undefined;
  const normalized = normalizeHost(host);
  const malformed = host.trim() !== "" && !HOST_RE.test(normalized);
  const match = existing.find((r) => r.host === normalized);
  const preset = KINDS[kind];
  const multiline = kind === "gar";

  const set = useMutation({
    mutationFn: () =>
      registries.setRegistry({
        host: normalized,
        username: username.trim(),
        password: new TextEncoder().encode(password),
      }),
    onSuccess: (res) => {
      toast(`${res.registry?.host} saved.`, "ok");
      void qc.invalidateQueries({ queryKey: ["registries"] });
      onOpenChange(false);
    },
    onError: (err) => toast(`SetRegistry: ${errorMessage(err)}`),
  });

  const ready = normalized !== "" && !malformed && username.trim() !== "" && password !== "";
  const label = set.isPending ? "Saving…" : match ? "Replace login" : "Save registry";

  function chooseKind(next: Kind) {
    setKind(next);
    // A preset username is a fact about the registry, not a choice; a free-text one is kept.
    if (KINDS[next].username) setUsername(KINDS[next].username);
    else if (username === KINDS[kind].username) setUsername("");
  }

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{locked ? `Replace the login for ${lockedHost}` : "New registry"}</DialogTitle>
          <DialogDescription>
            {locked
              ? "Tasks assigned after this pull with the new login; tasks already running keep the one they were assigned. The password being replaced cannot be recovered."
              : "The password is written once and can never be read back. It leaves the server only inside an assignment, to a node about to pull an image from this registry."}
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
            <Label>Kind</Label>
            <ToggleGroup
              type="single"
              value={kind}
              onValueChange={(v) => v && chooseKind(v as Kind)}
              aria-label="Registry kind"
            >
              {(Object.keys(KINDS) as Kind[]).map((k) => (
                <ToggleGroupItem key={k} value={k} aria-label={KINDS[k].label}>
                  {KINDS[k].label}
                </ToggleGroupItem>
              ))}
            </ToggleGroup>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="registry-host">Host</Label>
            <div className="relative">
              <Input
                id="registry-host"
                value={host}
                onChange={(e) => setHost(e.target.value)}
                readOnly={locked}
                autoFocus={!locked}
                aria-invalid={malformed}
                spellCheck={false}
                autoComplete="off"
                placeholder={preset.hostPlaceholder}
                className={cn(
                  "font-mono text-xs",
                  locked && "bg-raised pr-9 text-muted hover:border-input",
                )}
              />
              {locked ? (
                <Lock aria-hidden className="absolute top-2.5 right-3 size-4 text-faint" />
              ) : null}
            </div>
            {malformed ? (
              <p className="text-2xs text-err">
                The part of an image reference before its first slash, like{" "}
                <span className="font-mono">us-docker.pkg.dev</span> — no scheme, no path.
              </p>
            ) : (
              <p className="text-2xs text-faint">
                {normalized !== "" && normalized !== host.trim()
                  ? `Saved as ${normalized}.`
                  : "Every image pulled from this host, in any task or playbook, uses this login."}
              </p>
            )}
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="registry-username">Username</Label>
            <Input
              id="registry-username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              readOnly={preset.username !== undefined}
              spellCheck={false}
              autoComplete="off"
              className={cn(
                "font-mono text-xs",
                preset.username !== undefined && "bg-raised text-muted hover:border-input",
              )}
            />
          </div>

          <div className="space-y-1.5">
            <Label htmlFor="registry-password">{preset.passwordLabel}</Label>
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
                className={cn("font-mono text-xs", !reveal && "[-webkit-text-security:disc]")}
              />
            ) : (
              <div className="relative">
                <Input
                  id="registry-password"
                  type={reveal ? "text" : "password"}
                  value={password}
                  onChange={(e) => setPassword(e.target.value)}
                  autoFocus={locked}
                  spellCheck={false}
                  autoComplete="off"
                  className="pr-10 font-mono text-xs"
                />
                <Button
                  type="button"
                  variant="ghost"
                  size="icon-sm"
                  aria-label={reveal ? "Hide password" : "Show password"}
                  onClick={() => setReveal((r) => !r)}
                  className="absolute top-0.5 right-0.5"
                >
                  {reveal ? <EyeOff /> : <Eye />}
                </Button>
              </div>
            )}
            <p className="text-2xs text-faint">{preset.hint}</p>
          </div>

          {match && !locked ? (
            <Alert variant="warn" title={`${match.host} already has a login`}>
              Saving replaces it — user <span className="font-mono">{match.username}</span>, last
              set {relative(match.updatedAt)} — rather than adding a second one. A registry has
              one login.
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
