import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Building2, Check, Copy, Link2, Lock, RefreshCw, Share2, X } from "lucide-react";
import type { ChatGrant, ChatSharing } from "../../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage } from "../../../lib/client";
import {
  generalAccessHint,
  generalAccessLabel,
  roleLabel,
  shareLink,
} from "../../../lib/sharing";
import { useToast } from "../../Toast";
import { Alert } from "../../ui/alert";
import { Button } from "../../ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
} from "../../ui/dialog";
import { Input } from "../../ui/input";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../../ui/select";
import { cn } from "@/lib/utils";
import { Avatar } from "./Origin";

/**
 * Picker is the dialog's dropdown, drawn by the app's own select so its menu follows the theme
 * (a native select's list is painted by the browser, light even in dark mode). "field" sits
 * beside an input at its height, "ghost" is the quiet picker on a person's row and under general
 * access.
 */
function Picker({
  look = "ghost",
  value,
  options,
  onValueChange,
  disabled,
  className,
  ...trigger
}: {
  look?: "field" | "ghost";
  value: string;
  options: { value: string; label: string }[];
  onValueChange?: (value: string) => void;
  disabled?: boolean;
  className?: string;
  "aria-label": string;
  "data-testid"?: string;
}) {
  return (
    <Select value={value} onValueChange={onValueChange} disabled={disabled}>
      <SelectTrigger
        {...trigger}
        className={cn(
          "w-auto shrink-0 cursor-pointer gap-1.5 text-sm",
          "disabled:cursor-default disabled:opacity-100 disabled:text-muted disabled:[&>svg]:hidden",
          look === "field"
            ? "min-h-11"
            : "min-h-8 h-8 border-transparent bg-transparent px-2 hover:border-transparent hover:bg-raised disabled:hover:bg-transparent",
          className,
        )}
      >
        <SelectValue />
      </SelectTrigger>
      <SelectContent className="min-w-40">
        {options.map((o) => (
          <SelectItem key={o.value} value={o.value} className="text-sm">
            {o.label}
          </SelectItem>
        ))}
      </SelectContent>
    </Select>
  );
}

/**
 * privateSourceWarning is what the share dialog says about a mirrored conversation that is, or
 * may be, private where it lives: a Slack channel or DM, or an issue in a private Linear team.
 */
function privateSourceWarning(origin: string, certain: boolean): string {
  if (origin === "linear") {
    return certain
      ? "This chat mirrors an issue in a private Linear team. Sharing it shows what was said there to people who may not be able to see it."
      : "This chat mirrors a Linear issue whose team may be private. Sharing it shows what was said there to people who may not be able to see it.";
  }
  return certain
    ? "This chat mirrors a private Slack conversation. Sharing it shows what was said there to people who are not in it."
    : "This chat mirrors a Slack channel that may be private. Sharing it shows what was said there to people who may not be in it.";
}

const ROLE_OPTIONS = (["editor", "viewer"] as const).map((r) => ({ value: r, label: roleLabel(r) }));
const ACCESS_OPTIONS = ["restricted", "members", "link"].map((a) => ({ value: a, label: generalAccessLabel(a) }));

const ACCESS_ICON = { restricted: Lock, members: Building2, link: Link2 } as const;

/**
 * ShareButton opens the share dialog. Everybody who can open a chat may see who else can;
 * only the owner is offered the controls — the server refuses everybody else anyway.
 */
export function ShareButton({
  chatId,
  title,
  multiplayer = false,
}: {
  chatId: string;
  title: string;
  multiplayer?: boolean;
}) {
  const [open, setOpen] = useState(false);
  return (
    <>
      <Button
        type="button"
        variant="outline"
        size="sm"
        data-testid="chat-share"
        onClick={() => setOpen(true)}
        className="shrink-0"
      >
        <Share2 />
        Share
      </Button>
      {open ? (
        <ShareDialog
          chatId={chatId}
          title={title}
          multiplayer={multiplayer}
          open={open}
          onOpenChange={setOpen}
        />
      ) : null}
    </>
  );
}

/**
 * ShareDialog is laid out like a document's: add people, the people with access (the owner
 * pinned first), and general access with its link.
 *
 * multiplayer is the chat's own flag. A shared chat runs on the company's model key, for
 * good, so the dialog warns before the first share and says so afterwards.
 */
export function ShareDialog({
  chatId,
  title,
  multiplayer = false,
  open,
  onOpenChange,
}: {
  chatId: string;
  title: string;
  multiplayer?: boolean;
  open: boolean;
  onOpenChange: (open: boolean) => void;
}) {
  const qc = useQueryClient();
  const toast = useToast();
  const key = ["agent", "chat-sharing", chatId];
  const sharing = useQuery({
    queryKey: key,
    queryFn: async () => (await agent.getChatSharing({ chatId })).sharing,
  });
  const settle = (next: ChatSharing | undefined) => {
    if (next) qc.setQueryData(key, next);
    void qc.invalidateQueries({ queryKey: ["agent", "chats"] });
    void qc.invalidateQueries({ queryKey: ["agent", "sessions"] });
  };
  const share = useMutation({
    mutationFn: (v: { login: string; role: string }) => agent.shareChat({ chatId, ...v }),
    onSuccess: (res) => settle(res.sharing),
    onError: (err) => toast(errorMessage(err)),
  });
  const unshare = useMutation({
    mutationFn: (login: string) => agent.unshareChat({ chatId, login }),
    onSuccess: (res) => settle(res.sharing),
    onError: (err) => toast(errorMessage(err)),
  });
  const update = useMutation({
    mutationFn: (v: { generalAccess?: string; generalRole?: string; rotateLink?: boolean }) =>
      agent.updateChatSharing({ chatId, ...v }),
    onSuccess: (res) => settle(res.sharing),
    onError: (err) => toast(errorMessage(err)),
  });

  const s = sharing.data;
  const owner = s?.myRole === "owner";
  const busy = share.isPending || unshare.isPending || update.isPending;
  // Shared already: the chat says so, or this dialog has just shared it and the chat's own
  // flag has not come back on the stream yet.
  const shared =
    multiplayer ||
    (s !== undefined && (s.grants.length > 0 || (s.generalAccess !== "" && s.generalAccess !== "restricted")));

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent data-testid="share-dialog" className="max-w-lg">
        <DialogHeader>
          <DialogTitle>Share “{title || "this chat"}”</DialogTitle>
          <DialogDescription>
            Everyone you add sees the whole conversation, earlier messages included. Editors
            can post and ask Podium; viewers can only read.
          </DialogDescription>
        </DialogHeader>

        {sharing.isError ? <Alert variant="warn">{errorMessage(sharing.error)}</Alert> : null}

        {s?.privateSource || s?.sourcePrivacyUnknown ? (
          <Alert variant="warn" role="note" data-testid="share-private-warning">
            {privateSourceWarning(s.origin, s.privateSource)}
          </Alert>
        ) : null}

        {s ? (
          shared ? (
            <Alert role="note" data-testid="share-key-note">
              This chat is shared, so Podium answers it using the company’s model key.
              Everyone keeps their own GitHub account, secrets and MCP servers.
            </Alert>
          ) : owner ? (
            <Alert
              variant="warn"
              role="note"
              data-testid="share-key-warning"
              title="Shared chats use the company’s model key"
            >
              Once shared, Podium’s replies here, yours included, are billed to the company
              instead of your own key, even if you stop sharing later. Everyone keeps their own
              GitHub account, secrets and MCP servers.
            </Alert>
          ) : null
        ) : null}

        {owner ? (
          <AddPeople
            disabled={busy}
            exclude={[s.owner?.login ?? "", ...s.grants.map((g) => g.login)]}
            onAdd={(login, role) => share.mutate({ login, role })}
          />
        ) : null}

        <section aria-label="People with access" className="space-y-1.5">
          <h3 className="text-xs font-medium tracking-wide text-faint uppercase">People with access</h3>
          <ul className="-mx-2" data-testid="share-people">
            {s?.owner?.login ? <PersonRow grant={s.owner} role="owner" /> : null}
            {s?.grants.map((g) => (
              <PersonRow
                key={g.login}
                grant={g}
                role={g.role}
                editable={owner}
                disabled={busy}
                onRole={(role) => share.mutate({ login: g.login, role })}
                onRemove={() => unshare.mutate(g.login)}
              />
            ))}
            {s && !s.owner?.login && s.grants.length === 0 ? (
              <li className="px-2 py-1 text-sm text-muted">Nobody in Podium has taken part yet.</li>
            ) : null}
          </ul>
        </section>

        {s ? (
          <section aria-label="General access" className="space-y-1.5 border-t border-border pt-4">
            <h3 className="text-xs font-medium tracking-wide text-faint uppercase">General access</h3>
            <GeneralAccess
              access={s.generalAccess || "restricted"}
              role={s.generalRole || "viewer"}
              editable={owner}
              disabled={busy}
              onAccess={(generalAccess) => update.mutate({ generalAccess })}
              onRole={(generalRole) => update.mutate({ generalRole })}
            />
            {owner && s.generalAccess === "link" && s.linkToken ? (
              <LinkRow
                url={shareLink(window.location.origin, chatId, s.linkToken)}
                disabled={busy}
                onReset={() => update.mutate({ rotateLink: true })}
              />
            ) : null}
          </section>
        ) : null}
      </DialogContent>
    </Dialog>
  );
}

function PersonRow({
  grant,
  role,
  editable = false,
  disabled = false,
  onRole,
  onRemove,
}: {
  grant: ChatGrant;
  role: string;
  editable?: boolean;
  disabled?: boolean;
  onRole?: (role: string) => void;
  onRemove?: () => void;
}) {
  const name = grant.name || grant.login;
  return (
    <li
      data-testid="share-person"
      data-login={grant.login}
      className="flex min-h-11 items-center gap-3 rounded-md px-2 py-1.5"
    >
      <Avatar name={name} src={grant.avatar} login={grant.login} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm text-fg">{name}</span>
        {grant.name && grant.name !== grant.login ? (
          <span className="block truncate text-2xs text-muted">{grant.login}</span>
        ) : null}
      </span>
      {editable && role !== "owner" ? (
        <>
          <Picker
            aria-label={`Role for ${name}`}
            value={role}
            options={ROLE_OPTIONS}
            disabled={disabled}
            onValueChange={(v) => onRole?.(v)}
          />
          <Button
            type="button"
            variant="ghost"
            size="icon-sm"
            aria-label={`Remove ${name}`}
            disabled={disabled}
            onClick={onRemove}
          >
            <X />
          </Button>
        </>
      ) : (
        <span className="pr-2 text-sm text-muted">{roleLabel(role)}</span>
      )}
    </li>
  );
}

/** AddPeople finds Podium people by name or login and shares with the one picked. */
function AddPeople({
  disabled,
  exclude,
  onAdd,
}: {
  disabled: boolean;
  exclude: string[];
  onAdd: (login: string, role: string) => void;
}) {
  const [query, setQuery] = useState("");
  const [role, setRole] = useState("editor");
  const q = query.trim();
  const members = useQuery({
    queryKey: ["agent", "members", q],
    queryFn: () => agent.searchMembers({ query: q, limit: 8 }),
    enabled: q !== "",
    staleTime: 30_000,
  });
  const found = (members.data?.members ?? []).filter((m) => !exclude.includes(m.login));
  const add = (login: string) => {
    onAdd(login, role);
    setQuery("");
  };
  return (
    <section aria-label="Add people" className="space-y-1">
      <div className="flex items-center gap-2">
        <Input
          aria-label="Add people"
          data-testid="share-add-input"
          placeholder="Add people by name or email"
          value={query}
          disabled={disabled}
          onChange={(e) => setQuery(e.target.value)}
          className="flex-1"
        />
        <Picker
          look="field"
          aria-label="Role for new people"
          value={role}
          options={ROLE_OPTIONS}
          onValueChange={setRole}
        />
      </div>
      {q !== "" ? (
        <ul className="rounded-md border border-border bg-panel p-1" data-testid="share-add-results">
          {found.map((m) => (
            <li key={m.login}>
              <button
                type="button"
                data-testid="share-add-result"
                disabled={disabled}
                onClick={() => add(m.login)}
                className="flex w-full items-center gap-2 rounded px-2 py-1.5 text-left text-sm hover:bg-raised"
              >
                <Avatar name={m.name || m.login} src={m.avatar} login={m.login} />
                <span className="min-w-0 flex-1 truncate">{m.name || m.login}</span>
                {m.name ? <span className="truncate text-2xs text-muted">{m.login}</span> : null}
              </button>
            </li>
          ))}
          {members.isSuccess && found.length === 0 ? (
            <li className="px-2 py-1.5 text-sm text-muted">
              Nobody in Podium matches. People have to sign in once before they can be shared with.
            </li>
          ) : null}
        </ul>
      ) : null}
    </section>
  );
}

/**
 * GeneralAccess is who besides the people listed can open the chat: an icon for the mode, the
 * mode's picker with what it means under it, and, when it lets anybody in, their role.
 */
function GeneralAccess({
  access,
  role,
  editable,
  disabled,
  onAccess,
  onRole,
}: {
  access: string;
  role: string;
  editable: boolean;
  disabled: boolean;
  onAccess: (access: string) => void;
  onRole: (role: string) => void;
}) {
  const Icon = ACCESS_ICON[access as keyof typeof ACCESS_ICON] ?? Lock;
  const open = access !== "restricted";
  return (
    <div className="flex items-center gap-3">
      <span
        aria-hidden
        className={cn(
          "grid size-8 shrink-0 place-items-center rounded-full",
          open ? "bg-ok-bg text-ok" : "bg-raised text-muted",
        )}
      >
        <Icon className="size-4" />
      </span>
      <div className="min-w-0 flex-1">
        <Picker
          aria-label="General access"
          data-testid="share-general-access"
          className="-ml-2 max-w-full font-medium"
          value={access}
          options={ACCESS_OPTIONS}
          disabled={!editable || disabled}
          onValueChange={onAccess}
        />
        <p className="text-xs text-muted">{generalAccessHint(access, role)}</p>
      </div>
      {open ? (
        <Picker
          aria-label="General role"
          data-testid="share-general-role"
          value={role}
          options={ROLE_OPTIONS}
          disabled={!editable || disabled}
          onValueChange={onRole}
        />
      ) : null}
    </div>
  );
}

function LinkRow({ url, disabled, onReset }: { url: string; disabled: boolean; onReset: () => void }) {
  const [copied, setCopied] = useState(false);
  const toast = useToast();
  return (
    <div className="flex flex-wrap items-center gap-2" data-testid="share-link">
      <Link2 className="size-4 text-muted" aria-hidden />
      <code className="min-w-0 flex-1 truncate rounded bg-raised px-1.5 py-0.5 text-xs text-muted">{url}</code>
      <Button
        type="button"
        variant="outline"
        size="sm"
        data-testid="share-copy-link"
        onClick={() => {
          void navigator.clipboard
            ?.writeText(url)
            .then(() => {
              setCopied(true);
              setTimeout(() => setCopied(false), 2000);
            })
            .catch(() => toast("Could not copy the link."));
        }}
      >
        {copied ? <Check /> : <Copy />}
        {copied ? "Copied" : "Copy link"}
      </Button>
      <Button
        type="button"
        variant="ghost"
        size="sm"
        data-testid="share-reset-link"
        disabled={disabled}
        onClick={onReset}
        title="Make a new link. The old one stops letting anybody in; people who already joined keep access."
      >
        <RefreshCw />
        Reset link
      </Button>
    </div>
  );
}
