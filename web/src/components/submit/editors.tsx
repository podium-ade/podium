import type { ReactNode } from "react";
import { Plus, X } from "lucide-react";
import { Button } from "../ui/button";
import { Input } from "../ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../ui/select";

export interface EnvRow {
  id: number;
  key: string;
  value: string;
}

export interface SecretRow {
  id: number;
  name: string;
  target: string;
  key: string;
}

const CELL = "h-8 font-mono text-xs";

/** The column captions, so a stack of rows reads as a table rather than as loose inputs. */
function Columns({ children }: { children: ReactNode }) {
  return <div className="text-2xs font-medium tracking-wide text-faint uppercase">{children}</div>;
}

function RemoveButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="icon-sm"
      aria-label={label}
      title={label}
      onClick={onClick}
      className="text-faint hover:text-err"
    >
      <X />
    </Button>
  );
}

function AddButton({ label, onClick }: { label: string; onClick: () => void }) {
  return (
    <Button
      type="button"
      variant="ghost"
      size="sm"
      onClick={onClick}
      className="w-full justify-start border border-dashed border-border text-muted hover:border-accent/50 hover:text-fg"
    >
      <Plus />
      {label}
    </Button>
  );
}

/** env: a key and a value per row, aligned in two columns. */
export function KeyValueEditor({
  rows,
  onChange,
}: {
  rows: EnvRow[];
  onChange: (rows: EnvRow[]) => void;
}) {
  const edit = (id: number, patch: Partial<EnvRow>) =>
    onChange(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)));

  return (
    <div className="space-y-2">
      {rows.length > 0 ? (
        <div className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)_2rem] gap-2">
          <Columns>Name</Columns>
          <Columns>Value</Columns>
          <span />
        </div>
      ) : null}
      {rows.map((row, i) => (
        <div key={row.id} className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.6fr)_2rem] gap-2">
          <Input
            aria-label={`Environment name ${i + 1}`}
            value={row.key}
            onChange={(e) => edit(row.id, { key: e.target.value })}
            placeholder="CI"
            spellCheck={false}
            className={CELL}
          />
          <Input
            aria-label={`Environment value ${i + 1}`}
            value={row.value}
            onChange={(e) => edit(row.id, { value: e.target.value })}
            placeholder="true"
            spellCheck={false}
            className={CELL}
          />
          <RemoveButton
            label={`Remove ${row.key || `environment variable ${i + 1}`}`}
            onClick={() => onChange(rows.filter((r) => r.id !== row.id))}
          />
        </div>
      ))}
      <AddButton
        label="Add variable"
        onClick={() => onChange([...rows, { id: nextId(rows), key: "", value: "" }])}
      />
    </div>
  );
}

/**
 * secrets: the name of a stored value, and where in the container it should appear.
 *
 * The value itself is never here — a ref carries a name, and the server resolves it on the
 * way to the node. What the key means depends on the target, so the caption changes with it.
 */
export function SecretsEditor({
  rows,
  onChange,
  problems,
}: {
  rows: SecretRow[];
  onChange: (rows: SecretRow[]) => void;
  /** Problems for `secrets[i].name | target | key`, looked up by the caller. */
  problems: (index: number, field: "name" | "target" | "key") => string[];
}) {
  const edit = (id: number, patch: Partial<SecretRow>) =>
    onChange(rows.map((r) => (r.id === id ? { ...r, ...patch } : r)));

  return (
    <div className="space-y-2">
      {rows.length > 0 ? (
        <div className="grid grid-cols-[minmax(0,1fr)_6.5rem_minmax(0,1.4fr)_2rem] gap-2">
          <Columns>Secret</Columns>
          <Columns>Target</Columns>
          <Columns>Env var or path</Columns>
          <span />
        </div>
      ) : null}
      {rows.map((row, i) => {
        const bad = [...problems(i, "name"), ...problems(i, "target"), ...problems(i, "key")];
        return (
          <div key={row.id} className="space-y-1">
            <div className="grid grid-cols-[minmax(0,1fr)_6.5rem_minmax(0,1.4fr)_2rem] gap-2">
              <Input
                aria-label={`Secret name ${i + 1}`}
                aria-invalid={problems(i, "name").length > 0 || undefined}
                value={row.name}
                onChange={(e) => edit(row.id, { name: e.target.value })}
                placeholder="DB_PASSWORD"
                spellCheck={false}
                className={CELL}
              />
              <Select value={row.target} onValueChange={(v) => edit(row.id, { target: v })}>
                <SelectTrigger
                  size="sm"
                  aria-label={`Secret target ${i + 1}`}
                  aria-invalid={problems(i, "target").length > 0 || undefined}
                  className="h-8 font-mono text-xs"
                >
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="env">env</SelectItem>
                  <SelectItem value="file">file</SelectItem>
                </SelectContent>
              </Select>
              <Input
                aria-label={`Secret key ${i + 1}`}
                aria-invalid={problems(i, "key").length > 0 || undefined}
                value={row.key}
                onChange={(e) => edit(row.id, { key: e.target.value })}
                placeholder={row.target === "file" ? "/podium/secrets/db" : "DB_PASSWORD"}
                spellCheck={false}
                className={CELL}
              />
              <RemoveButton
                label={`Remove ${row.name || `secret ${i + 1}`}`}
                onClick={() => onChange(rows.filter((r) => r.id !== row.id))}
              />
            </div>
            {bad.map((p) => (
              <p key={p} className="text-2xs break-words text-err">
                {p}
              </p>
            ))}
          </div>
        );
      })}
      <AddButton
        label="Add secret"
        onClick={() =>
          onChange([...rows, { id: nextId(rows), name: "", target: "env", key: "" }])
        }
      />
    </div>
  );
}

function nextId(rows: { id: number }[]): number {
  return rows.reduce((max, r) => Math.max(max, r.id), 0) + 1;
}
