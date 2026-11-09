/** SURFACE_NAMES is how each surface a message can come through is said in a sentence. */
export const SURFACE_NAMES: Record<string, string> = {
  slack: "Slack",
  github: "GitHub",
  web: "Podium",
};

/** surfaceName is a surface's name for a sentence: "said in Slack". */
export function surfaceName(via?: string): string {
  return (via && SURFACE_NAMES[via]) || via || "Podium";
}
