import { useEffect, useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Chip } from "../components/Badge";
import { Empty } from "../components/Empty";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import { Code, connectCode, errorMessage, secrets } from "../lib/client";
import { absolute, relative } from "../lib/format";

/**
 * The Secrets screen shows metadata and nothing else.
 *
 * There is no read endpoint in `SecretService` and there must never be one: a value goes in
 * once and only ever comes back out inside an `Assign`, on its way to the node about to run the
 * task that asked for it. `ListSecrets` carries no value and no ciphertext, so the screen
 * cannot leak one even by accident — and there is deliberately no "reveal" control to add one
 * to later.
 */
export function SecretsPage() {
  const qc = useQueryClient();
  const toast = useToast();
  const [name, setName] = useState("");
  const [value, setValue] = useState("");
  const [confirming, setConfirming] = useState<string>();

  const query = useQuery({
    queryKey: ["secrets"],
    queryFn: () => secrets.listSecrets({}),
    placeholderData: (prev) => prev,
  });

  useEffect(() => {
    if (query.error) toast(`ListSecrets: ${errorMessage(query.error)}`);
  }, [query.error, toast]);

  const set = useMutation({
    mutationFn: () =>
      secrets.setSecret({ name: name.trim(), value: new TextEncoder().encode(value) }),
    onSuccess: (res) => {
      toast(`${res.secret?.name} saved at version ${res.secret?.version}.`, "ok");
      setName("");
      setValue("");
      void qc.invalidateQueries({ queryKey: ["secrets"] });
    },
    onError: (err) => toast(`SetSecret: ${errorMessage(err)}`),
  });

  const remove = useMutation({
    mutationFn: (secretName: string) => secrets.deleteSecret({ name: secretName }),
    onSuccess: (_res, secretName) => {
      toast(`${secretName} deleted.`, "ok");
      void qc.invalidateQueries({ queryKey: ["secrets"] });
    },
    onError: (err) => toast(`DeleteSecret: ${errorMessage(err)}`),
  });

  const rows = useMemo(() => query.data?.secrets ?? [], [query.data]);
  // A key_id that differs from the rest means a master-key rotation stopped half way: those
  // rows are still readable with the old key and nothing else is.
  const mixedKeys = useMemo(() => new Set(rows.map((s) => s.keyId)).size > 1, [rows]);
  const noMasterKey = connectCode(query.error) === Code.FailedPrecondition;

  return (
    <div className="space-y-4">
      <h1 className="text-base font-semibold">Secrets</h1>

      <p className="text-xs text-muted">
        A secret&apos;s value cannot be viewed after it is saved. There is no read API and this
        screen has no way to show one: a value leaves the server only inside an assignment, on
        its way to the node that is about to run a task that named it. To change one, set it
        again — the version increases and tasks scheduled after that get the new value.
      </p>

      {noMasterKey ? (
        <p className="rounded border border-warn/50 bg-warn/10 px-3 py-2 text-xs text-warn">
          This server has no master key configured, so it cannot store secrets. Set{" "}
          <code className="font-mono">PODIUM_MASTER_KEY_FILE</code> and restart it.
        </p>
      ) : null}

      <section className="rounded border border-border bg-panel p-3">
        <h2 className="text-sm font-medium">Set a secret</h2>
        <form
          className="mt-3 space-y-3 text-xs"
          onSubmit={(e) => {
            e.preventDefault();
            set.mutate();
          }}
        >
          <label className="flex flex-col gap-1">
            <span className="text-muted">Name</span>
            <input
              aria-label="Secret name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder="DB_PASSWORD"
              className="w-full max-w-sm rounded border border-border bg-bg px-2 py-1 font-mono outline-none focus:border-accent"
            />
          </label>
          <label className="flex flex-col gap-1">
            <span className="text-muted">Value</span>
            <textarea
              aria-label="Secret value"
              value={value}
              onChange={(e) => setValue(e.target.value)}
              rows={4}
              spellCheck={false}
              autoComplete="off"
              className="w-full rounded border border-border bg-bg px-2 py-1 font-mono outline-none focus:border-accent"
            />
          </label>
          <p className="text-muted">
            The value is sent verbatim as bytes. A trailing newline is part of the secret — the
            CLI strips one from stdin, the API strips nothing.
          </p>
          <button
            type="submit"
            disabled={set.isPending || name.trim() === "" || value === ""}
            className="rounded bg-accent px-3 py-1.5 font-medium text-bg disabled:opacity-50"
          >
            {set.isPending ? "Saving…" : "Save secret"}
          </button>
        </form>
      </section>

      {query.isPending ? (
        <TableSkeleton cols={5} />
      ) : rows.length === 0 ? (
        <Empty title="No secrets" hint="Nothing is stored yet. Set one above." />
      ) : (
        <div className="overflow-x-auto rounded border border-border">
          <table className="w-full text-left text-sm">
            <thead className="bg-panel text-xs text-muted">
              <tr>
                <th className="px-3 py-2 font-medium">Name</th>
                <th className="px-3 py-2 font-medium">Version</th>
                <th className="px-3 py-2 font-medium">Updated</th>
                <th className="px-3 py-2 font-medium">Updated by</th>
                <th className="px-3 py-2 font-medium">Key</th>
                <th className="px-3 py-2 font-medium" />
              </tr>
            </thead>
            <tbody>
              {rows.map((s) => (
                <tr key={s.name} className="border-t border-border">
                  <td className="px-3 py-1.5 font-mono text-xs">{s.name}</td>
                  <td className="px-3 py-1.5 font-mono text-xs">{s.version}</td>
                  <td
                    className="px-3 py-1.5 text-xs whitespace-nowrap"
                    title={absolute(s.updatedAt)}
                  >
                    {relative(s.updatedAt)}
                  </td>
                  <td className="px-3 py-1.5 text-xs">{s.createdBy || "—"}</td>
                  <td className="px-3 py-1.5 font-mono text-xs">
                    <Chip>{s.keyId || "—"}</Chip>
                  </td>
                  <td className="px-3 py-1.5 text-right text-xs">
                    {confirming === s.name ? (
                      <span className="flex items-center justify-end gap-2">
                        <span className="text-muted">Delete {s.name}?</span>
                        <button
                          type="button"
                          onClick={() => {
                            setConfirming(undefined);
                            remove.mutate(s.name);
                          }}
                          className="rounded border border-err/60 px-2 py-0.5 text-err"
                        >
                          Yes, delete
                        </button>
                        <button
                          type="button"
                          onClick={() => setConfirming(undefined)}
                          className="rounded border border-border px-2 py-0.5"
                        >
                          Keep
                        </button>
                      </span>
                    ) : (
                      <button
                        type="button"
                        aria-label={`Delete ${s.name}`}
                        onClick={() => setConfirming(s.name)}
                        className="rounded border border-border px-2 py-0.5 hover:border-err hover:text-err"
                      >
                        Delete
                      </button>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {mixedKeys ? (
        <p className="text-xs text-warn">
          These secrets are not all encrypted under the same master key, which means a rotation
          stopped part way. Finish it with{" "}
          <code className="font-mono">podium-server rotate-master-key</code>.
        </p>
      ) : null}

      <p className="text-xs text-muted">
        Deleting a secret does not stop tasks that are already running. The next task that names
        it fails at admission with{" "}
        <code className="font-mono">missing secret &quot;NAME&quot;</code>.
      </p>
    </div>
  );
}
