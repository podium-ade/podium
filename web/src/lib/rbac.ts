import { IdentityKind } from "../gen/podium/v1/identity_pb";
import type { Viewer } from "./identity";

/** Roles stored on a user after a Google Workspace claim. */
export const RoleOwner = "owner";
export const RoleAdmin = "admin";
export const RoleMember = "member";

/**
 * rbacEnforced is when KindUser is actually gated. The local token, a node, an unclaimed
 * instance and a deployment without Google sign-in all skip the map — same as the server.
 */
export function rbacEnforced(v?: Viewer): boolean {
  return Boolean(v?.googleAuthEnabled && v.claimed && v.kind === IdentityKind.USER);
}

/** Highest role in the list. Empty is member: a person, not an operator. */
export function canonicalRole(roles: string[] | undefined): string {
  if (roles?.includes(RoleOwner)) return RoleOwner;
  if (roles?.includes(RoleAdmin)) return RoleAdmin;
  return RoleMember;
}

/** Nodes, secrets, registries, agent config. Owner satisfies it. */
export function canManageInfra(v?: Viewer): boolean {
  if (!rbacEnforced(v)) return true;
  const role = canonicalRole(v?.roles);
  return role === RoleOwner || role === RoleAdmin;
}

/** Changing someone else's role. Only an owner. */
export function canManageUsers(v?: Viewer): boolean {
  if (!rbacEnforced(v)) return true;
  return canonicalRole(v?.roles) === RoleOwner;
}

export function roleLabel(role: string): string {
  switch (role) {
    case RoleOwner:
      return "Owner";
    case RoleAdmin:
      return "Admin";
    default:
      return "Member";
  }
}
