import { afterEach, describe, expect, it } from "vitest";
import { IdentityKind } from "../gen/podium/v1/identity_pb";
import type { Viewer } from "./identity";
import { assistantName, brandName, HOUSE_NAME } from "./brand";

const viewer: Viewer = {
  login: "local",
  displayName: "",
  kind: IdentityKind.LOCAL_TOKEN,
  agentEnabled: true,
  serverVersion: "dev",
  roles: [],
  claimed: false,
  hostedDomain: "",
  canClaim: false,
  googleAuthEnabled: false,
  claimDomain: "",
  pictureUrl: "",
};

describe("brand", () => {
  afterEach(() => document.head.querySelectorAll('meta[name="podium-deployment-name"]').forEach((m) => m.remove()));

  it("is Podium until the deployment is renamed", () => {
    expect(brandName(undefined)).toBe(HOUSE_NAME);
    expect(brandName(viewer)).toBe(HOUSE_NAME);
    expect(brandName({ ...viewer, deploymentName: "  " })).toBe(HOUSE_NAME);
    expect(brandName({ ...viewer, deploymentName: "Acme AI" })).toBe("Acme AI");
  });

  it("takes the name podium-server stamped on the page before anyone signs in", () => {
    const meta = document.createElement("meta");
    meta.name = "podium-deployment-name";
    meta.content = "Acme AI";
    document.head.append(meta);
    expect(brandName(undefined)).toBe("Acme AI");
    // WhoAmI's answer is the newer one once there is a viewer.
    expect(brandName({ ...viewer, deploymentName: "Acme Labs" })).toBe("Acme Labs");
  });

  it("renames the house assistant with the deployment unless it has a name of its own", () => {
    expect(assistantName("Podium", "Acme AI")).toBe("Acme AI");
    expect(assistantName("", "Acme AI")).toBe("Acme AI");
    expect(assistantName(undefined, "Podium")).toBe("Podium");
    expect(assistantName("Ada", "Acme AI")).toBe("Ada");
  });
});
