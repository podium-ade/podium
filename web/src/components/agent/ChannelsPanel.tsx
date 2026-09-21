import { useState } from "react";
import { Hash, Pencil } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { SlackChannel } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { absolute, relative } from "../../lib/format";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageHeader } from "../PageHeader";
import { Skeleton } from "../Skeleton";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "../ui/dialog";
import { Label } from "../ui/label";
import { Textarea } from "../ui/textarea";
import { ConductorDown } from "./ConductorDown";

/** Matches store.MaxSlackChannelDescriptionRunes. */
const MAX_DESCRIPTION = 500;

/**
 * ChannelsPanel is the catalogue of Slack channels this bot has been in, and the note an
 * operator attaches so the assistant knows what a channel is for.
 *
 * The set of rows is Slack's: invite the bot, or mention it, and the channel appears. There
 * is no create or delete. An empty description is valid — the name still shows in chat.
 */
export function ChannelsPanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const [editing, setEditing] = useState<SlackChannel>();

  const list = useQuery({
    queryKey: ["agent", "slack-channels"],
    queryFn: () => agent.listSlackChannels({}),
  });
  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "slack-channels"] });

  const save = useMutation({
    mutationFn: (v: { id: string; description: string }) =>
      agent.setSlackChannelDescription({ id: v.id, description: v.description }),
    onSuccess: async (_res, v) => {
      toast(`Context for ${labelOf(editing, v.id)} saved. It applies to the next turn.`, "ok");
      setEditing(undefined);
      await reload();
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const channels = list.data?.channels ?? [];
  const connected = list.data?.slackConnected ?? false;

  if (list.isError && !isAgentUnreachable(list.error)) {
    return (
      <Empty icon={Hash} title="Could not read Slack channels" hint={errorMessage(list.error)} />
    );
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Channels"
        description="Tell the bot what each Slack channel is for. #support is customer complaints; #eng is engineering. The name also appears on mirrored threads in Chat."
        meta={
          channels.length > 0 ? (
            <Chip className="tabular">
              {channels.length} {channels.length === 1 ? "channel" : "channels"}
            </Chip>
          ) : undefined
        }
      />

      {list.isError && isAgentUnreachable(list.error) ? (
        <ConductorDown
          what="Slack channels could not be read"
          onRetry={() => void list.refetch()}
          retrying={list.isFetching}
        />
      ) : null}

      {list.isPending ? <ChannelsSkeleton /> : null}

      {!list.isPending && !connected && channels.length === 0 ? (
        <Empty
          icon={Hash}
          title="Slack is not connected"
          hint="Set both Slack tokens on this conductor. Channels the bot is invited to will appear here."
        />
      ) : null}

      {!list.isPending && connected && channels.length === 0 ? (
        <Empty
          icon={Hash}
          title="No Slack channels yet"
          hint="Invite the bot to a channel (/invite @Podium) or mention it. Channels it is in then appear here."
        />
      ) : null}

      {channels.length > 0 ? (
        <ul className="space-y-2.5">
          {channels.map((ch) => (
            <ChannelRow key={ch.id} channel={ch} onEdit={() => setEditing(ch)} />
          ))}
        </ul>
      ) : null}

      <DescriptionDialog
        key={editing?.id ?? "closed"}
        channel={editing}
        open={editing !== undefined}
        saving={save.isPending}
        onClose={() => setEditing(undefined)}
        onSave={(description) => {
          if (editing) save.mutate({ id: editing.id, description });
        }}
      />
    </div>
  );
}

function labelOf(ch: SlackChannel | undefined, id: string): string {
  const name = ch?.name;
  return name ? `#${name}` : id;
}

function ChannelRow({ channel, onEdit }: { channel: SlackChannel; onEdit: () => void }) {
  const title = channel.name ? `#${channel.name}` : channel.id;
  return (
    <li
      data-testid="slack-channel-row"
      className="rounded-xl border border-border bg-card px-4 py-3.5 shadow-xs"
    >
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-medium text-fg">{title}</span>
            {channel.name ? (
              <span className="font-mono text-2xs text-faint">{channel.id}</span>
            ) : (
              <Badge tone="idle">name unknown</Badge>
            )}
          </div>
          <p className="max-w-2xl text-sm leading-relaxed text-muted">
            {channel.description || "No context yet. The bot will not know what this channel is for."}
          </p>
          <p className="text-2xs text-faint" title={absolute(channel.updatedAt)}>
            updated {relative(channel.updatedAt)}
          </p>
        </div>
        <Button
          type="button"
          variant="outline"
          size="sm"
          data-testid="slack-channel-edit"
          onClick={onEdit}
        >
          <Pencil />
          Edit
        </Button>
      </div>
    </li>
  );
}

function DescriptionDialog({
  channel,
  open,
  saving,
  onClose,
  onSave,
}: {
  channel?: SlackChannel;
  open: boolean;
  saving: boolean;
  onClose: () => void;
  onSave: (description: string) => void;
}) {
  const [draft, setDraft] = useState(channel?.description ?? "");
  const title = channel?.name ? `#${channel.name}` : (channel?.id ?? "");

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) onClose();
        else setDraft(channel?.description ?? "");
      }}
    >
      <DialogContent>
        <form
          onSubmit={(e) => {
            e.preventDefault();
            onSave(draft);
          }}
        >
          <DialogHeader>
            <DialogTitle>Context for {title}</DialogTitle>
            <DialogDescription>
              The bot is briefed with this on every turn in this channel. It is not Slack&apos;s
              own purpose — only what you write here.
            </DialogDescription>
          </DialogHeader>
          <div className="space-y-2 py-2">
            <Label htmlFor="slack-channel-description">Description</Label>
            <Textarea
              id="slack-channel-description"
              data-testid="slack-channel-description"
              value={draft}
              maxLength={MAX_DESCRIPTION}
              rows={5}
              placeholder="Customer complaints. Be empathetic. Do not file GitHub issues from here."
              onChange={(e) => setDraft(e.target.value)}
            />
            <p className="text-2xs text-faint">
              {draft.trim().length}/{MAX_DESCRIPTION}
            </p>
          </div>
          <DialogFooter>
            <Button type="button" variant="outline" size="sm" onClick={onClose}>
              Cancel
            </Button>
            <Button type="submit" size="sm" disabled={saving} data-testid="slack-channel-save">
              {saving ? "Saving…" : "Save"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function ChannelsSkeleton() {
  return (
    <ul className="space-y-2.5" aria-hidden>
      {Array.from({ length: 3 }, (_, i) => (
        <li key={i} className="space-y-2 rounded-xl border border-border bg-card px-4 py-3.5">
          <Skeleton className="h-4 w-32" />
          <Skeleton className="h-3 w-2/3" />
        </li>
      ))}
    </ul>
  );
}
