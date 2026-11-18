import { cn } from "../../../lib/utils";
import { PROVIDERS, providerName } from "./workView";

/**
 * ProviderMark is the small sign of which issue tracker an item lives in, beside its key: the
 * provider's mark, or its name when Podium has none for it. The name is its accessible label.
 */
export function ProviderMark({ provider, className }: { provider: string; className?: string }) {
  const name = providerName(provider);
  const mark = PROVIDERS[provider]?.mark;
  if (!provider) return null;
  return mark ? (
    <img
      src={mark}
      alt={name}
      title={name}
      data-testid="work-provider-mark"
      className={cn("inline-block size-3 shrink-0 self-center", className)}
    />
  ) : (
    <span data-testid="work-provider-mark" className={cn("shrink-0 text-2xs text-faint", className)}>
      {name}
    </span>
  );
}
