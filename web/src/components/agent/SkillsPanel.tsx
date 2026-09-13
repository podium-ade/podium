import { useId, useRef, useState } from "react";
import { Database, FileCode2, Folder, Plus, Puzzle, Trash2, Upload, X } from "lucide-react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { AgentSkill } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { readDirectoryFiles, zipSkillFolder } from "../../lib/skillZip";
import { absolute, humanBytes, relative } from "../../lib/format";
import { Badge, Chip } from "../Badge";
import { Empty } from "../Empty";
import { PageHeader } from "../PageHeader";
import { Skeleton } from "../Skeleton";
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
import { Label } from "../ui/label";
import { Switch } from "../ui/switch";
import { Textarea } from "../ui/textarea";
import { Tooltip } from "../ui/tooltip";
import { ConductorDown } from "./ConductorDown";

/**
 * SkillsPanel is the Agent Skill library: what this conductor can hand a turn, and the only
 * way to add one without a shell on its host.
 *
 * Adding one is the same class of decision as giving a playbook a credential: a skill is
 * instructions and scripts somebody else wrote, and they run inside the turn's container
 * with that turn's GitHub token and model credential.
 */
export function SkillsPanel() {
  const qc = useQueryClient();
  const toast = useToast();
  const [adding, setAdding] = useState(false);

  const list = useQuery({ queryKey: ["agent", "skills"], queryFn: () => agent.listSkills({}) });
  const reload = () => qc.invalidateQueries({ queryKey: ["agent", "skills"] });

  const setEnabled = useMutation({
    mutationFn: (v: { name: string; enabled: boolean }) => agent.setSkillEnabled(v),
    onSuccess: async (_res, v) => {
      toast(`${v.name} is ${v.enabled ? "enabled" : "disabled"}. It applies to the next turn.`, "ok");
      await reload();
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const remove = useMutation({
    mutationFn: (name: string) => agent.deleteSkill({ name }),
    onSuccess: async (_res, name) => {
      toast(`${name} deleted.`, "ok");
      await reload();
    },
    onError: (err) => toast(errorMessage(err)),
  });

  const skills = list.data?.skills ?? [];
  const running = skills.filter((s) => !s.shadowed);
  const shadowed = skills.filter((s) => s.shadowed);

  if (list.isError && !isAgentUnreachable(list.error)) {
    return <Empty icon={Puzzle} title="Could not read the skills" hint={errorMessage(list.error)} />;
  }

  return (
    <div className="space-y-5">
      <PageHeader
        title="Skills"
        description={
          <>
            An Agent Skill is a <code className="font-mono">SKILL.md</code> and its files: a
            procedure somebody else wrote, which the model loads when a task matches its
            description. A playbook names the ones its turns may load, and names nothing by
            default.
          </>
        }
        meta={
          running.length > 0 ? (
            <>
              <Chip className="tabular">
                {running.length} {running.length === 1 ? "skill" : "skills"}
              </Chip>
              <Chip className="tabular">
                {running.filter((s) => !s.enabled).length} disabled
              </Chip>
            </>
          ) : undefined
        }
        actions={
          <Button type="button" size="sm" data-testid="skill-new" onClick={() => setAdding(true)}>
            <Plus />
            New skill
          </Button>
        }
      />

      {list.isError && isAgentUnreachable(list.error) ? (
        <ConductorDown
          what="The skills could not be read"
          onRetry={() => void list.refetch()}
          retrying={list.isFetching}
        />
      ) : null}

      {list.isPending ? <SkillsSkeleton /> : null}

      {!list.isPending && running.length === 0 ? (
        <Empty
          icon={Puzzle}
          title="No skills"
          hint={
            list.data?.skillsDir
              ? `Upload a SKILL.md or a folder here, or put a directory with a SKILL.md in ${list.data.skillsDir} on the conductor's host.`
              : "Upload a SKILL.md, a folder of the skill's files, or paste the markdown."
          }
          action={
            <Button type="button" size="sm" onClick={() => setAdding(true)}>
              <Plus />
              New skill
            </Button>
          }
        />
      ) : null}

      {running.length > 0 ? (
        <ul className="space-y-2.5">
          {running.map((s) => (
            <SkillRow
              key={`${s.origin}:${s.name}`}
              skill={s}
              busy={
                (setEnabled.isPending && setEnabled.variables?.name === s.name) ||
                (remove.isPending && remove.variables === s.name)
              }
              onToggle={(enabled) => setEnabled.mutate({ name: s.name, enabled })}
              onDelete={() => remove.mutate(s.name)}
            />
          ))}
        </ul>
      ) : null}

      {shadowed.length > 0 ? (
        <section className="space-y-2.5">
          <h2 className="text-sm font-semibold text-fg">Shadowed</h2>
          <p className="max-w-3xl text-xs leading-relaxed text-muted">
            A directory of the same name exists in{" "}
            <code className="font-mono">{list.data?.skillsDir}</code> on the conductor&apos;s host,
            and the host wins. These uploads never run. Deleting them is the only way to make
            this list say what a turn will actually get.
          </p>
          <ul className="space-y-2.5">
            {shadowed.map((s) => (
              <SkillRow
                key={`shadowed:${s.name}`}
                skill={s}
                busy={remove.isPending && remove.variables === s.name}
                onDelete={() => remove.mutate(s.name)}
              />
            ))}
          </ul>
        </section>
      ) : null}

      {list.data?.skillsDir ? (
        <p className="text-2xs leading-relaxed text-faint">
          This conductor also reads <code className="font-mono">{list.data.skillsDir}</code> on its
          own host. Those are files, not rows: they cannot be changed here, they win a name
          clash, and they are the way in when this screen or the database is unavailable.
        </p>
      ) : null}

      <AddSkillDialog
        open={adding}
        onOpenChange={setAdding}
        maxBytes={Number(list.data?.maxBytes ?? 0)}
        maxFiles={list.data?.maxFiles ?? 0}
        existing={new Set(skills.map((s) => s.name))}
        onDone={async (name, replaced) => {
          setAdding(false);
          toast(`${name} ${replaced ? "replaced" : "added"}. It applies to the next turn.`, "ok");
          await reload();
        }}
      />
    </div>
  );
}

/**
 * SkillRow is one skill. The digest is shown truncated with the whole of it in the title,
 * because it is the only handle on *which bytes* a turn is being handed and an operator
 * comparing two installs needs to be able to read it.
 */
function SkillRow({
  skill,
  busy,
  onToggle,
  onDelete,
}: {
  skill: AgentSkill;
  busy: boolean;
  onToggle?: (enabled: boolean) => void;
  onDelete: () => void;
}) {
  const [confirming, setConfirming] = useState(false);
  const uid = useId();
  const fromHost = !skill.editable;

  return (
    <li
      data-testid="skill-row"
      className="rounded-xl border border-border bg-card px-4 py-3.5 shadow-xs"
    >
      <div className="flex flex-wrap items-start gap-x-3 gap-y-2">
        <div className="min-w-0 flex-1 space-y-1.5">
          <div className="flex flex-wrap items-center gap-2">
            <span className="font-mono text-sm font-medium text-fg">{skill.name}</span>
            {fromHost ? (
              <Badge tone="idle" dot={false}>
                <FileCode2 className="size-3" />
                host directory
              </Badge>
            ) : (
              <Badge tone="idle" dot={false}>
                <Database className="size-3" />
                uploaded
              </Badge>
            )}
            {skill.shadowed ? <Badge tone="warn">shadowed</Badge> : null}
            {!skill.enabled && !skill.shadowed ? <Badge tone="lost">disabled</Badge> : null}
          </div>
          {skill.description ? (
            <p className="max-w-2xl text-xs leading-relaxed text-muted">{skill.description}</p>
          ) : null}
          {skill.problem ? (
            <p className="max-w-2xl text-xs leading-relaxed text-err">{skill.problem}</p>
          ) : null}
        </div>
        <div className="flex shrink-0 items-center gap-2">
          {onToggle && !fromHost && !skill.shadowed ? (
            <div className="flex items-center gap-2">
              <Switch
                id={`${uid}-enabled`}
                aria-label={`${skill.name} is enabled`}
                checked={skill.enabled}
                disabled={busy}
                onCheckedChange={onToggle}
              />
              <Label htmlFor={`${uid}-enabled`} className="cursor-pointer">
                Enabled
              </Label>
            </div>
          ) : null}
          {fromHost ? null : (
            <Button
              type="button"
              variant="ghost"
              size="sm"
              data-testid="skill-delete"
              aria-label={`Delete ${skill.name}`}
              disabled={busy}
              onClick={() => setConfirming(true)}
              className="hover:bg-err/12 hover:text-err"
            >
              <Trash2 />
              Delete
            </Button>
          )}
        </div>
      </div>

      <div className="mt-3 flex flex-wrap items-center gap-1.5 border-t border-hairline pt-2.5">
        <Chip className="tabular">{humanBytes(skill.sizeBytes)}</Chip>
        <Chip className="tabular">
          {skill.fileCount} {skill.fileCount === 1 ? "file" : "files"}
        </Chip>
        {skill.sha256 ? (
          <Tooltip label={skill.sha256}>
            <Chip className="font-mono">{skill.sha256.slice(0, 12)}…</Chip>
          </Tooltip>
        ) : null}
        {skill.playbooks.length > 0 ? (
          <Chip>{skill.playbooks.map((p) => `/${p}`).join(" ")}</Chip>
        ) : (
          <Chip>no playbook names it</Chip>
        )}
        {skill.uploadedBy ? (
          <span className="ml-auto shrink-0 text-2xs text-faint">
            by {skill.uploadedBy}
            {skill.uploadedAt ? (
              <>
                {" · "}
                <span title={absolute(skill.uploadedAt)}>{relative(skill.uploadedAt)}</span>
              </>
            ) : null}
          </span>
        ) : null}
      </div>

      <Dialog open={confirming} onOpenChange={setConfirming}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>Delete {skill.name}?</DialogTitle>
            <DialogDescription>
              The bundle goes with it. There is no undo, and no copy is kept anywhere else.
            </DialogDescription>
          </DialogHeader>
          {skill.playbooks.length > 0 ? (
            <Alert variant="warn" role="note">
              {skill.playbooks.map((p) => `/${p}`).join(", ")}{" "}
              {skill.playbooks.length === 1 ? "names" : "name"} this skill. Every turn of{" "}
              {skill.playbooks.length === 1 ? "that playbook" : "those playbooks"} will fail until
              the name is taken out of it.
            </Alert>
          ) : null}
          <DialogFooter>
            <Button type="button" variant="outline" size="sm" onClick={() => setConfirming(false)}>
              Keep
            </Button>
            <Button
              type="button"
              variant="destructive"
              size="sm"
              data-testid="skill-delete-confirm"
              aria-label={`Confirm deleting ${skill.name}`}
              disabled={busy}
              onClick={() => {
                setConfirming(false);
                onDelete();
              }}
            >
              {busy ? "Deleting…" : "Delete skill"}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </li>
  );
}

/**
 * AddSkillDialog is the two ways in: a SKILL.md (or a folder of the skill's files), or a
 * pasted SKILL.md.
 *
 * Which one was sent is not declared — the conductor recognises an archive by its own header
 * — so this form does not have to be right about it, and cannot be wrong about it. The name
 * is not asked for either: it comes out of the frontmatter, because that is the only name
 * the harness will load the skill under.
 */
function AddSkillDialog({
  open,
  onOpenChange,
  maxBytes,
  maxFiles,
  existing,
  onDone,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  maxBytes: number;
  maxFiles: number;
  existing: Set<string>;
  onDone: (name: string, replaced: boolean) => void | Promise<void>;
}) {
  const uid = useId();
  const fileInput = useRef<HTMLInputElement>(null);
  const dirInput = useRef<HTMLInputElement>(null);
  const [file, setFile] = useState<File>();
  const [folder, setFolder] = useState<{ name: string; zip: Uint8Array }>();
  const [text, setText] = useState("");
  const [replace, setReplace] = useState(false);
  const [error, setError] = useState<string>();

  function reset() {
    setFile(undefined);
    setFolder(undefined);
    setText("");
    setReplace(false);
    setError(undefined);
    if (fileInput.current) fileInput.current.value = "";
    if (dirInput.current) dirInput.current.value = "";
  }

  function clearPicked() {
    setFile(undefined);
    setFolder(undefined);
    setError(undefined);
    if (fileInput.current) fileInput.current.value = "";
    if (dirInput.current) dirInput.current.value = "";
  }

  const upload = useMutation({
    mutationFn: async () => {
      const content = folder
        ? folder.zip
        : file
          ? new Uint8Array(await file.arrayBuffer())
          : new TextEncoder().encode(text);
      const filename = folder ? `${folder.name}.zip` : file?.name ?? "";
      return agent.uploadSkill({ content, filename, replace });
    },
    onSuccess: async (res) => {
      const name = res.skill?.name ?? "The skill";
      reset();
      await onDone(name, res.replaced);
    },
    onError: (err) => setError(errorMessage(err)),
  });

  // The pasted name, so the dialog can warn about a collision before the conductor refuses
  // one. It is a courtesy: the frontmatter the server parses is the answer, and this regex
  // only reads the obvious case.
  const pastedName = /^---\s*$[\s\S]*?^name:\s*(\S+)\s*$/m.exec(text)?.[1] ?? "";
  const collides = pastedName !== "" && existing.has(pastedName);
  const picked = file !== undefined || folder !== undefined;
  const ready = (picked || text.trim() !== "") && !upload.isPending;

  return (
    <Dialog
      open={open}
      onOpenChange={(next) => {
        if (!next) reset();
        onOpenChange(next);
      }}
    >
      <DialogContent>
        <DialogHeader>
          <DialogTitle>Add a skill</DialogTitle>
          <DialogDescription>
            A <Mono>SKILL.md</Mono>, a folder of the skill&apos;s files, or the text pasted
            below. The name comes from the file&apos;s own frontmatter — it is the only name the
            harness will load it under.
          </DialogDescription>
        </DialogHeader>
        <form
          className="space-y-4"
          onSubmit={(e) => {
            e.preventDefault();
            if (ready) upload.mutate();
          }}
        >
          <div className="space-y-1.5">
            <Label>SKILL.md or folder</Label>
            <input
              id={`${uid}-file`}
              ref={fileInput}
              type="file"
              accept=".md,text/markdown,.zip,application/zip"
              className="sr-only"
              disabled={upload.isPending}
              onChange={(e) => {
                setFile(e.target.files?.[0]);
                setFolder(undefined);
                setError(undefined);
              }}
            />
            <input
              ref={dirInput}
              type="file"
              className="sr-only"
              disabled={upload.isPending}
              multiple
              // Directory picks are a Chromium/WebKit file-picker feature, not a standard
              // input attribute, so they are passed through rather than typed.
              {...{ webkitdirectory: "", directory: "" }}
              onChange={async (e) => {
                const list = e.target.files;
                if (!list || list.length === 0) return;
                try {
                  const entries = await readDirectoryFiles(list);
                  if (entries.length === 0) {
                    setError("that folder has no files to upload");
                    return;
                  }
                  const root = entries[0]?.path.split("/")[0] || "skill";
                  setFolder({ name: root, zip: zipSkillFolder(entries) });
                  setFile(undefined);
                  setError(undefined);
                } catch (err) {
                  setError(err instanceof Error ? err.message : "the folder could not be read");
                }
              }}
            />
            <div className="flex flex-wrap items-center gap-2">
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={upload.isPending}
                onClick={() => fileInput.current?.click()}
              >
                <FileCode2 />
                Choose SKILL.md
              </Button>
              <Button
                type="button"
                variant="outline"
                size="sm"
                disabled={upload.isPending}
                onClick={() => dirInput.current?.click()}
              >
                <Folder />
                Choose folder
              </Button>
              {picked ? (
                <span className="inline-flex min-w-0 items-center gap-1 font-mono text-xs text-muted">
                  <span className="truncate">{file?.name ?? folder?.name}</span>
                  <Button
                    type="button"
                    variant="ghost"
                    size="icon-xs"
                    aria-label="Remove selected file"
                    disabled={upload.isPending}
                    onClick={clearPicked}
                  >
                    <X />
                  </Button>
                </span>
              ) : null}
            </div>
            <p className="text-2xs leading-relaxed text-faint">
              Up to {maxBytes > 0 ? humanBytes(maxBytes) : "128 KB"} unpacked and {maxFiles || 64}{" "}
              files, UTF-8 text only. Nothing in a bundle is executable: files land 0644 and a
              script is run through its interpreter.
            </p>
          </div>

          <div className="space-y-1.5">
            <Label htmlFor={`${uid}-text`}>Or paste a SKILL.md</Label>
            <Textarea
              id={`${uid}-text`}
              value={text}
              rows={8}
              spellCheck={false}
              disabled={picked || upload.isPending}
              onChange={(e) => {
                setText(e.target.value);
                setError(undefined);
              }}
              placeholder={"---\nname: pr-review\ndescription: Use when reviewing a diff.\n---\n\nRead the diff, then…"}
              className="font-mono text-xs"
            />
            {picked ? (
              <p className="text-2xs text-faint">
                A file is selected, so the text box is ignored. Remove it to paste instead.
              </p>
            ) : null}
          </div>

          {collides ? (
            <div className="flex items-start gap-3 rounded-lg border border-hairline px-3 py-2.5">
              <Switch
                id={`${uid}-replace`}
                aria-label="Replace the stored skill of this name"
                checked={replace}
                onCheckedChange={setReplace}
                className="mt-0.5"
              />
              <div className="min-w-0 space-y-0.5">
                <Label htmlFor={`${uid}-replace`} className="text-fg">
                  Replace the stored <Mono>{pastedName}</Mono>
                </Label>
                <p className="text-2xs leading-relaxed text-faint">
                  Every playbook that names it gets these bytes from the next turn on.
                </p>
              </div>
            </div>
          ) : null}

          {error ? (
            <Alert variant="destructive" role="alert" title="The conductor refused this skill">
              {error}
              {!replace && /already stored/.test(error) ? (
                <>
                  {" "}
                  <button
                    type="button"
                    className="text-accent underline"
                    onClick={() => {
                      setReplace(true);
                      setError(undefined);
                    }}
                  >
                    Replace it instead
                  </button>
                  .
                </>
              ) : null}
            </Alert>
          ) : null}

          <DialogFooter>
            <DialogClose asChild>
              <Button type="button" variant="outline" size="sm">
                Cancel
              </Button>
            </DialogClose>
            <Button type="submit" size="sm" disabled={!ready}>
              <Upload />
              {upload.isPending ? "Uploading…" : replace ? "Replace skill" : "Add skill"}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  );
}

function SkillsSkeleton() {
  return (
    <ul aria-busy="true" aria-label="Loading" className="space-y-2.5">
      {[0, 1, 2].map((i) => (
        <li key={i} className="space-y-3 rounded-xl border border-border bg-card px-4 py-3.5">
          <Skeleton className="h-3.5 w-1/3" />
          <Skeleton className="h-3 w-3/5" />
          <div className="flex gap-2">
            <Skeleton className="h-4 w-16" />
            <Skeleton className="h-4 w-14" />
            <Skeleton className="h-4 w-28" />
          </div>
        </li>
      ))}
    </ul>
  );
}

function Mono({ children }: { children: React.ReactNode }) {
  return <code className="font-mono">{children}</code>;
}
