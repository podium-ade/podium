import { useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import type { IdentityPolicy } from "../../gen/podium/agent/v1/agent_pb";
import { agent, errorMessage, isAgentUnreachable } from "../../lib/client";
import { useBrand } from "../../lib/brand";
import { useToast } from "../Toast";
import { Button } from "../ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../ui/select";
import { ConductorDown } from "./ConductorDown";

const policyKey = ["agent", "identity-policy"];

/** Field is one setting of the policy: the proto field, what it is, and what it may be. */
type Field = {
  key: keyof Omit<IdentityPolicy, "$typeName" | "$unknown">;
  label: string;
  hint: string;
  options: (brand: string) => { value: string; label: string }[];
};

const personal = (brand: string, withApp: boolean) => [
  { value: "person_then_app", label: `Theirs, else ${brand}` },
  { value: "person_only", label: "Theirs only" },
  ...(withApp ? [{ value: "app", label: `Always ${brand}` }] : []),
];

/**
 * ACCOUNTS is whose account a turn uses when a person is behind it. A system routine's run
 * always uses the organization's, whatever these say.
 */
const ACCOUNTS: Field[] = [
  {
    key: "linear",
    label: "Linear",
    hint: "Reading and updating issues. Theirs is their own Linear MCP server; the organization's is the Linear app.",
    options: (b) => personal(b, true),
  },
  {
    key: "githubWrite",
    label: "GitHub pushes and pull requests",
    hint: "Theirs is the GitHub account they connected; the organization's is the GitHub App, which names them in every commit.",
    options: (b) => personal(b, true),
  },
  {
    key: "githubRead",
    label: "GitHub reads in routines",
    hint: "What a routine reads code and pull requests with. The GitHub App reads only repositories it is installed on, one at a time.",
    options: (b) => personal(b, false),
  },
  {
    key: "modelKey",
    label: "Model key in private chats",
    hint: "Shared conversations, Slack threads and Linear issues always spend the organization's key.",
    options: (b) => [
      { value: "person_then_app", label: `Theirs, else ${b}'s` },
      { value: "app", label: `Always ${b}'s` },
    ],
  },
];

/** OUTSIDERS is what somebody who is not a Podium person gets when they ask from elsewhere. */
const OUTSIDERS: Field[] = [
  {
    key: "slackOutsiders",
    label: "Slack members",
    hint: "A workspace member whose email is not somebody here.",
    options: (b) => [
      { value: "refuse", label: "Refuse" },
      { value: "answer", label: `Answer as ${b}` },
    ],
  },
  {
    key: "linearOutsiders",
    label: "Linear members",
    hint: "A workspace member whose email is not somebody here. Delegated issues only works the issue and takes no instructions from them.",
    options: (b) => [
      { value: "refuse", label: "Refuse" },
      { value: "fixed", label: "Delegated issues only" },
      { value: "answer", label: `Answer as ${b}` },
    ],
  },
  {
    key: "githubOutsiders",
    label: "GitHub users",
    hint: "Anybody who has not connected GitHub here, members included. A review is rate limited and takes no instructions from them.",
    options: () => [
      { value: "review", label: "Review command only" },
      { value: "refuse", label: "Refuse" },
    ],
  },
];

type Values = Record<Field["key"], string>;

function valuesOf(p: IdentityPolicy): Values {
  return {
    linear: p.linear,
    githubWrite: p.githubWrite,
    githubRead: p.githubRead,
    modelKey: p.modelKey,
    slackOutsiders: p.slackOutsiders,
    linearOutsiders: p.linearOutsiders,
    githubOutsiders: p.githubOutsiders,
  };
}

/**
 * IdentityPolicyPanel is Settings → Identity, for admins and owners: whose account each kind of
 * work uses when a person is behind a turn, and what somebody outside Podium gets when they ask
 * from Slack, Linear or GitHub.
 */
export function IdentityPolicyPanel() {
  const policy = useQuery({
    queryKey: policyKey,
    queryFn: () => agent.getIdentityPolicy({}),
  });

  if (isAgentUnreachable(policy.error)) {
    return (
      <div className="p-4">
        <ConductorDown
          what="Identity settings could not be read"
          onRetry={() => void policy.refetch()}
          retrying={policy.isFetching}
        />
      </div>
    );
  }
  if (policy.isError) {
    return <p className="p-4 text-sm text-err">{errorMessage(policy.error)}</p>;
  }
  const loaded = policy.data?.policy;
  if (!loaded) return <p className="p-4 text-sm text-muted">Loading…</p>;
  // Keyed on what was loaded, so a save that lands starts the fields from the new values.
  return <PolicyForm key={JSON.stringify(valuesOf(loaded))} loaded={valuesOf(loaded)} />;
}

function PolicyForm({ loaded }: { loaded: Values }) {
  const brand = useBrand();
  const qc = useQueryClient();
  const toast = useToast();
  const [values, setValues] = useState<Values>(loaded);
  // Only what this admin changed is sent; the server keeps the rest as it is stored, so a page
  // loaded before somebody else's save does not put their settings back. The answer is the
  // whole policy as it now stands, which the form then shows.
  const changed = (Object.keys(loaded) as Field["key"][]).filter((k) => loaded[k] !== values[k]);
  // A second click before the first save's state reaches the button would send it twice.
  const sending = useRef(false);
  const save = useMutation({
    mutationFn: (v: Partial<Values>) => agent.setIdentityPolicy({ policy: v }),
    onSuccess: (res) => {
      qc.setQueryData(policyKey, { policy: res.policy });
      toast("Identity settings saved.", "ok");
    },
    // A save that lands remounts the form on the new policy (its key), with a fresh guard; only
    // a failed one lets this form send again.
    onError: () => {
      sending.current = false;
    },
  });
  const set = (k: Field["key"], v: string) => setValues((cur) => ({ ...cur, [k]: v }));

  return (
    <form
      className="divide-y divide-border"
      onSubmit={(e) => {
        e.preventDefault();
        if (changed.length === 0 || sending.current) return;
        sending.current = true;
        save.mutate(Object.fromEntries(changed.map((k) => [k, values[k]])));
      }}
    >
      <Group
        id="identity-accounts"
        title="Whose account a person's work uses"
        lead={`When somebody here asks for something, it runs as them. Where they have not connected an account, ${brand} can use the organization's. System routines always use the organization's.`}
        fields={ACCOUNTS}
        values={values}
        brand={brand}
        onChange={set}
      />
      <Group
        id="identity-outsiders"
        title={`People who are not in ${brand}`}
        lead={`Somebody who asks from Slack, Linear or GitHub without being a person here. Answered, they run as ${brand}: the organization's key, its Linear app and GitHub App, and no one's personal secrets.`}
        fields={OUTSIDERS}
        values={values}
        brand={brand}
        onChange={set}
      />
      <div className="flex items-center gap-3 p-4">
        <Button type="submit" size="sm" disabled={changed.length === 0 || save.isPending} data-testid="identity-save">
          {save.isPending ? "Saving…" : "Save"}
        </Button>
        {save.isError ? (
          <p role="alert" className="text-sm text-err">
            {errorMessage(save.error)}
          </p>
        ) : null}
      </div>
    </form>
  );
}

function Group({
  id,
  title,
  lead,
  fields,
  values,
  brand,
  onChange,
}: {
  id: string;
  title: string;
  lead: string;
  fields: Field[];
  values: Values;
  brand: string;
  onChange: (k: Field["key"], v: string) => void;
}) {
  return (
    <section aria-labelledby={`${id}-heading`} className="grid gap-3 p-4">
      <div className="grid max-w-2xl gap-1">
        <h2 id={`${id}-heading`} className="text-sm font-semibold text-fg">
          {title}
        </h2>
        <p className="text-sm text-muted">{lead}</p>
      </div>
      <div className="grid divide-y divide-border rounded-md border border-border">
        {fields.map((f) => (
          <div key={f.key} className="grid items-center gap-x-6 gap-y-2 px-3 py-3 sm:grid-cols-[minmax(0,1fr)_14rem]">
            <div className="grid gap-0.5">
              <label htmlFor={`identity-${f.key}`} className="text-sm font-medium text-fg">
                {f.label}
              </label>
              <p className="text-xs text-faint">{f.hint}</p>
            </div>
            <Select value={values[f.key]} onValueChange={(v) => onChange(f.key, v)}>
              <SelectTrigger id={`identity-${f.key}`} size="sm" data-testid={`identity-${f.key}`}>
                <SelectValue />
              </SelectTrigger>
              <SelectContent>
                {f.options(brand).map((o) => (
                  <SelectItem key={o.value} value={o.value}>
                    {o.label}
                  </SelectItem>
                ))}
              </SelectContent>
            </Select>
          </div>
        ))}
      </div>
    </section>
  );
}
