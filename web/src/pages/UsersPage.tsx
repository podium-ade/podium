import { useEffect, useMemo } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Check, Shield, Users } from "lucide-react";
import { Chip } from "../components/Badge";
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
  { label: "Configure keys, playbooks, skills and MCP", member: false, admin: true, owner: true },
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
    <div className="space-y-5">
      {rows.length > 0 ? (
        <div className="flex flex-wrap items-center gap-2">
          <Chip>
            {rows.length} {rows.length === 1 ? "person" : "people"}
          </Chip>
          <Chip>
            {owners} {owners === 1 ? "owner" : "owners"}
          </Chip>
        </div>
      ) : null}

      <PermissionsTable />

      {!manage ? (
        <Alert variant="info" title="You can see who is here">
          Changing a role takes the owner role. Ask an owner if you need to operate the
          fleet or the agent.
        </Alert>
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
        <Table>
          <TableHeader>
            <TableRow className="hover:bg-transparent">
              <TableHead>Person</TableHead>
              <TableHead>Login</TableHead>
              <TableHead>Role</TableHead>
              <TableHead>Last seen</TableHead>
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

function PermissionsTable() {
  return (
    <Table>
      <TableHeader>
        <TableRow className="hover:bg-transparent">
          <TableHead>Permission</TableHead>
          {ROLES.map((role) => (
            <TableHead key={role} className="w-28 text-center">
              <RoleBadge role={role} />
            </TableHead>
          ))}
        </TableRow>
      </TableHeader>
      <TableBody>
        {CAPABILITIES.map((row) => (
          <TableRow key={row.label}>
            <TableCell className="text-xs text-fg">{row.label}</TableCell>
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
    <TableCell className="text-center">
      {allowed ? (
        <Check className="mx-auto size-3.5 text-ok" aria-label={label} />
      ) : (
        <span className="text-faint" aria-label={`${label}: no`}>
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
      <TableCell>
        <div className="min-w-0">
          <div className="truncate text-sm font-medium text-fg">{name}</div>
          {user.hostedDomain ? (
            <div className="truncate text-2xs text-faint">{user.hostedDomain}</div>
          ) : null}
        </div>
      </TableCell>
      <TableCell className="font-mono text-xs text-muted">{user.login}</TableCell>
      <TableCell>
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
      <TableCell className="text-xs whitespace-nowrap text-muted" title={absolute(user.lastSeenAt)}>
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
