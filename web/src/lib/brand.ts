import { useEffect } from "react";
import { useViewer, type Viewer } from "./identity";

/** HOUSE_NAME is what the product is called until an admin renames this deployment. */
export const HOUSE_NAME = "Podium";

/** The <meta> podium-server stamps on index.html with the deployment's name. See api/ui.go. */
const SERVED_META = "podium-deployment-name";

/**
 * servedName is the name podium-server put on the page it served. It is what the sign-in
 * screen has to go on: WhoAmI, which carries the name otherwise, needs a credential.
 */
export function servedName(): string {
  if (typeof document === "undefined") return "";
  return document.querySelector(`meta[name="${SERVED_META}"]`)?.getAttribute("content")?.trim() ?? "";
}

/** brandName is what this deployment is called: the saved name, else Podium. */
export function brandName(v?: Viewer): string {
  return v?.deploymentName?.trim() || servedName() || HOUSE_NAME;
}

/** useBrand is brandName for the signed-in viewer. */
export function useBrand(): string {
  return brandName(useViewer());
}

/** useOrganizationName is the organization this deployment serves, or empty. */
export function useOrganizationName(): string {
  return useViewer()?.organizationName?.trim() ?? "";
}

/**
 * assistantName is what the house assistant is called. A deployment renamed "Acme AI"
 * renames the assistant too, unless an admin gave it a name of its own. The conductor
 * applies the same rule (conductor/branding.go), so the chat and the turn agree.
 */
export function assistantName(displayName: string | undefined, brand: string): string {
  const name = displayName?.trim() ?? "";
  if (!name || name === HOUSE_NAME) return brand;
  return name;
}

/** useDocumentTitle keeps the browser tab named after the deployment. */
export function useDocumentTitle(brand: string) {
  useEffect(() => {
    document.title = brand;
  }, [brand]);
}
