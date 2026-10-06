import { describe, expect, it } from "vitest";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import type { Viewer } from "./identity";
import {
  RoleAdmin,
  RoleMember,
  RoleOwner,
  canManageInfra,
  canManageUsers,
  canonicalRole,
  rbacEnforced,
} from "./rbac";

function viewer(partial: Partial<Viewer> = {}): Viewer {
  return {
    login: "bob@acme.com",
    displayName: "Bob",
    kind: IdentityKind.USER,
    agentEnabled: true,
    serverVersion: "dev",
    roles: [RoleMember],
    claimed: true,
    hostedDomain: "acme.com",
    canClaim: false,
    googleAuthEnabled: true,
    claimDomain: "acme.com",
    pictureUrl: "",
    ...partial,
  };
}

describe("rbac", () => {
  it("is off without a claimed Google instance and a human", () => {
    expect(rbacEnforced(undefined)).toBe(false);
    expect(rbacEnforced(viewer({ googleAuthEnabled: false }))).toBe(false);
    expect(rbacEnforced(viewer({ claimed: false }))).toBe(false);
    expect(rbacEnforced(viewer({ kind: IdentityKind.LOCAL_TOKEN }))).toBe(false);
    expect(rbacEnforced(viewer())).toBe(true);
  });

  it("treats empty roles as member", () => {
    expect(canonicalRole([])).toBe(RoleMember);
    expect(canonicalRole(undefined)).toBe(RoleMember);
    expect(canonicalRole([RoleAdmin, RoleOwner])).toBe(RoleOwner);
  });

  it("lets members use the product but not infra or users", () => {
    const member = viewer({ roles: [RoleMember] });
    expect(canManageInfra(member)).toBe(false);
    expect(canManageUsers(member)).toBe(false);
  });

  it("lets admins manage infra but not users", () => {
    const admin = viewer({ roles: [RoleAdmin] });
    expect(canManageInfra(admin)).toBe(true);
    expect(canManageUsers(admin)).toBe(false);
  });

  it("lets owners do both", () => {
    const owner = viewer({ roles: [RoleOwner] });
    expect(canManageInfra(owner)).toBe(true);
    expect(canManageUsers(owner)).toBe(true);
  });

  it("opens every control when RBAC is not enforced", () => {
    expect(canManageInfra(undefined)).toBe(true);
    expect(canManageUsers(undefined)).toBe(true);
  });
});
