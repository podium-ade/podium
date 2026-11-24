import { useEffect, useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Shield, Users } from "lucide-react";
import { Empty } from "../components/Empty";
import { TableSkeleton } from "../components/Skeleton";
import { useToast } from "../components/Toast";
import { Alert } from "../components/ui/alert";
import { Badge } from "../components/ui/badge";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "../components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "../components/ui/table";
import { Tooltip } from "../components/ui/tooltip";
import type { User } from "../gen/podium/v1/user_pb";
import { errorMessage, users } from "../lib/client";
import { absolute, relative } from "../lib/format";
import { useViewer } from "../lib/identity";
import {
  RoleAdmin,
  RoleMember,
  RoleOwner,
  canManageUsers,
  canonicalRole,
  roleLabel,
} from "../lib/rbac";
import { cn } from "../lib/utils";

const ROLES = [RoleMember, RoleAdmin, RoleOwner] as const;

const CAPABILITIES: { label: string; member: boolean; admin: boolean; owner: boolean }[] = [
  { label: "Submit, follow and cancel tasks", member: true, admin: true, owner: true },
  { label: "Chat, sessions, usage and memory", member: true, admin: true, owner: true },
  { label: "See nodes, secrets, registries and who is here", member: true, admin: true, owner: true },
  { label: "Enroll, drain, rekey and delete nodes", member: false, admin: true, owner: true },
  { label: "Set and delete secrets and registries", member: false, admin: true, owner: true },
  { label: "Configure the assistant, keys, connections, playbooks, skills and MCP", member: false, admin: true, owner: true },
  { label: "Change anyone's role", member: false, admin: false, owner: true },
];

/**
 * UsersPage is who has signed in, and what each role may do.
 *
 * After a Google Workspace claim the tables are the whole of RBAC a human sees. The local
 * token and every node still do everything; Slack and Linear are not on this screen.
 */
export function UsersPage() {
  const viewer = useViewer();
  const toast = useToast();
  const qc = useQueryClient();
  const manage = canManageUsers(viewer);

  const query = useQuery({
    queryKey: ["users"],
    queryFn: () => users.listUsers({}),
    placeholderData: (prev) => prev,
  });

  useEffect(() => {
    if (query.error) toast(`ListUsers: ${errorMessage(query.error)}`);
  }, [query.error, toast]);

  const rows = useMemo(() => query.data?.users ?? [], [query.data]);
  const owners = useMemo(
    () => rows.filter((u) => canonicalRole(u.roles) === RoleOwner).length,
    [rows],
  );

  const setRole = useMutation({
    mutationFn: (v: { login: string; role: string }) => users.setUserRole(v),
    onSuccess: async (res) => {
      toast(`${res.user?.login}: ${roleLabel(canonicalRole(res.user?.roles))}.`, "ok");
      await qc.invalidateQueries({ queryKey: ["users"] });
    },
    onError: (err) => toast(errorMessage(err)),
  });

  return (
    <div>
      <PermissionsTable />

      {rows.length > 0 ? (
        <div className="flex flex-wrap items-center gap-2 border-t border-border p-4">
          <span className="inline-flex min-h-7 items-center rounded-sm border border-border bg-raised px-2 text-xs font-bold">
            {rows.length} {rows.length === 1 ? "person" : "people"}
          </span>
          <span className="inline-flex min-h-7 items-center rounded-sm border border-border bg-raised px-2 text-xs font-bold">
            {owners} {owners === 1 ? "owner" : "owners"}
          </span>
        </div>
      ) : null}

      {!manage ? (
        <div className="border-t border-border p-4">
          <Alert variant="info" title="You can see who is here">
            Changing a role takes the owner role. Ask an owner if you need to operate the
            fleet or the agent.
          </Alert>
        </div>
      ) : null}

      {query.isPending ? (
        <TableSkeleton cols={4} />
      ) : rows.length === 0 ? (
        query.error ? (
          <Alert variant="destructive" title="Could not list users">
            {errorMessage(query.error)}
          </Alert>
        ) : (
          <Empty
            icon={Users}
            title="No one has signed in yet"
            hint="A login appears here the first time Tailscale or Google Workspace names them."
          />
        )
      ) : (
        <Table containerClassName="rounded-none border-0 bg-transparent" className="min-w-[640px]" stack>
          <caption className="sr-only">People</caption>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead className={headCell}>Person</TableHead>
              <TableHead className={headCell}>Login</TableHead>
              <TableHead className={headCell}>Role</TableHead>
              <TableHead className={headCell}>Last seen</TableHead>
            </TableRow>
          </TableHeader>
          <TableBody>
            {rows.map((u) => (
              <UserRow
                key={u.login}
                user={u}
                manage={manage}
                lastOwner={canonicalRole(u.roles) === RoleOwner && owners === 1}
                busy={setRole.isPending && setRole.variables?.login === u.login}
                onRole={(role) => setRole.mutate({ login: u.login, role })}
              />
            ))}
          </TableBody>
        </Table>
      )}
    </div>
  );
}

const headCell =
  "px-4 py-3 text-xs font-[750] leading-[normal] tracking-[0.08em] text-muted uppercase";

function PermissionsTable() {
  return (
    // On a phone the matrix fits rather than scrolls: the three role columns are only as wide
    // as their headings, and the permission wraps.
    <Table containerClassName="rounded-none border-0 bg-transparent" className="max-sm:min-w-0 sm:min-w-[640px]">
      <caption className="sr-only">What each role can do</caption>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead className={cn(headCell, "px-3 sm:px-4")}>Permission</TableHead>
          {ROLES.map((role) => (
            <TableHead key={role} className={cn(headCell, "w-px px-2 text-center sm:w-28 sm:px-4")}>
              {roleLabel(role)}
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {CAPABILITIES.map((row) => (
          <TableRow key={row.label}>
            <TableCell className="px-3 py-3 text-sm text-fg sm:px-4 sm:py-4">{row.label}</TableCell>
            <AllowedCell allowed={row.member} label={`${roleLabel(RoleMember)}: ${row.label}`} />
            <AllowedCell allowed={row.admin} label={`${roleLabel(RoleAdmin)}: ${row.label}`} />
            <AllowedCell allowed={row.owner} label={`${roleLabel(RoleOwner)}: ${row.label}`} />
          </TableRow>
        ))}
      </TableBody>
    </Table>
  );
}

function AllowedCell({ allowed, label }: { allowed: boolean; label: string }) {
  return (
    <TableCell className="px-2 py-3 text-center text-sm font-bold sm:px-4 sm:py-4">
      {allowed ? (
        <span className="text-ok" aria-label={label}>
          Yes
        </span>
      ) : (
        <span className="font-normal text-muted" aria-label={`${label}: no`}>
          —
        </span>
      )}
    </TableCell>
  );
}

function UserRow({
  user,
  manage,
  lastOwner,
  busy,
  onRole,
}: {
  user: User;
  manage: boolean;
  lastOwner: boolean;
  busy: boolean;
  onRole: (role: string) => void;
}) {
  const role = canonicalRole(user.roles);
  const name = user.displayName || user.login;
  return (
    <TableRow>
      <TableCell className="px-4 py-4">
        <div className="min-w-0">
          <strong className="block truncate text-sm">{name}</strong>
          {user.hostedDomain ? (
            <span className="block truncate text-xs text-muted">{user.hostedDomain}</span>
          ) : null}
        </div>
      </TableCell>
      <TableCell data-stack="full" className="px-4 py-4 text-sm break-all text-soft">
        {user.login}
      </TableCell>
      <TableCell data-stack="side" className="px-4 py-4">
        {manage ? (
          <Tooltip
            label={lastOwner ? "An instance with no owner cannot recover from the UI." : undefined}
          >
            <span className="inline-flex">
              <Select
                value={role}
                onValueChange={onRole}
                disabled={busy || lastOwner}
              >
                <SelectTrigger size="sm" className="w-32" aria-label={`Role for ${user.login}`}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {ROLES.map((r) => (
                    <SelectItem key={r} value={r}>
                      {roleLabel(r)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </span>
          </Tooltip>
        ) : (
          <RoleBadge role={role} />
        )}
      </TableCell>
      <TableCell
        data-label="Last seen"
        className="px-4 py-4 text-sm whitespace-nowrap text-soft"
        title={absolute(user.lastSeenAt)}
      >
        {relative(user.lastSeenAt)}
      </TableCell>
    </TableRow>
  );
}

function RoleBadge({ role }: { role: string }) {
  const variant = role === RoleOwner ? "ok" : role === RoleAdmin ? "run" : "idle";
  return (
    <Badge variant={variant} className={cn(role === RoleOwner && "gap-1")}>
      {role === RoleOwner ? <Shield /> : null}
      {roleLabel(role)}
    </Badge>
  );
}
