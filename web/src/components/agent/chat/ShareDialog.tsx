import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Copy, Link2, RefreshCw, Share2, X } from "lucide-react";
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
import { Avatar } from "./Origin";

const selectCls =
  "rounded-md border border-border bg-bg px-2 py-1 text-sm text-fg disabled:text-muted";

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
            People you share with see the whole conversation, including what was said before.
            Editors can post and ask Podium; viewers read.
          </DialogDescription>
        </DialogHeader>

        {sharing.isError ? <Alert variant="warn">{errorMessage(sharing.error)}</Alert> : null}

        {s?.privateSource || s?.sourcePrivacyUnknown ? (
          <Alert variant="warn" role="note" data-testid="share-private-warning">
            {s.privateSource
              ? "This chat mirrors a private Slack conversation. Sharing it shows what was said there to people who are not in it."
              : "This chat mirrors a Slack channel that may be private. Sharing it shows what was said there to people who may not be in it."}
          </Alert>
        ) : null}

        {s ? (
          shared ? (
            <Alert role="note" data-testid="share-key-note">
              This chat is shared, so it runs on the company’s model key. Each person’s own
              secrets, MCP servers and GitHub account still apply to what they ask.
            </Alert>
          ) : owner ? (
            <Alert
              variant="warn"
              role="note"
              data-testid="share-key-warning"
              title="Sharing moves this chat to the company’s model key"
            >
              From the first share on, every answer here, yours included, is paid for with the
              company’s key instead of your own, and it stays that way even if you stop sharing.
              Each person’s own secrets, MCP servers and GitHub account still apply to what
              they ask.
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

        <section aria-label="People with access" className="space-y-1">
          <h3 className="text-xs font-medium tracking-wide text-faint uppercase">People with access</h3>
          <ul className="space-y-1" data-testid="share-people">
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
              <li className="text-sm text-muted">Nobody in Podium has taken part yet.</li>
            ) : null}
          </ul>
        </section>

        {s ? (
          <section aria-label="General access" className="space-y-2">
            <h3 className="text-xs font-medium tracking-wide text-faint uppercase">General access</h3>
            <div className="flex flex-wrap items-center gap-2">
              <select
                aria-label="General access"
                data-testid="share-general-access"
                className={selectCls}
                value={s.generalAccess || "restricted"}
                disabled={!owner || busy}
                onChange={(e) => update.mutate({ generalAccess: e.target.value })}
              >
                {["restricted", "members", "link"].map((a) => (
                  <option key={a} value={a}>
                    {generalAccessLabel(a)}
                  </option>
                ))}
              </select>
              {s.generalAccess && s.generalAccess !== "restricted" ? (
                <select
                  aria-label="General role"
                  data-testid="share-general-role"
                  className={selectCls}
                  value={s.generalRole || "viewer"}
                  disabled={!owner || busy}
                  onChange={(e) => update.mutate({ generalRole: e.target.value })}
                >
                  <option value="viewer">{roleLabel("viewer")}</option>
                  <option value="editor">{roleLabel("editor")}</option>
                </select>
              ) : null}
            </div>
            <p className="text-xs text-muted">{generalAccessHint(s.generalAccess, s.generalRole)}</p>
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
    <li data-testid="share-person" data-login={grant.login} className="flex items-center gap-2 py-1">
      <Avatar name={name} src={grant.avatar} login={grant.login} />
      <span className="min-w-0 flex-1">
        <span className="block truncate text-sm text-fg">{name}</span>
        {grant.name && grant.name !== grant.login ? (
          <span className="block truncate text-2xs text-muted">{grant.login}</span>
        ) : null}
      </span>
      {editable && role !== "owner" ? (
        <>
          <select
            aria-label={`Role for ${name}`}
            className={selectCls}
            value={role}
            disabled={disabled}
            onChange={(e) => onRole?.(e.target.value)}
          >
            <option value="viewer">{roleLabel("viewer")}</option>
            <option value="editor">{roleLabel("editor")}</option>
          </select>
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
        <span className="text-xs text-muted">{roleLabel(role)}</span>
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
          className="h-9 flex-1"
        />
        <select
          aria-label="Role for new people"
          className={selectCls}
          value={role}
          onChange={(e) => setRole(e.target.value)}
        >
          <option value="editor">{roleLabel("editor")}</option>
          <option value="viewer">{roleLabel("viewer")}</option>
        </select>
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
