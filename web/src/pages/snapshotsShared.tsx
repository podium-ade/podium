/** Design table head: 12px, weight 750, muted, uppercase, 0.08em tracking, 12×16 padding. */
export const headCell =
  "px-4 py-3 text-xs font-[750] leading-[normal] tracking-[0.08em] text-muted uppercase";

export function MobileLabel({ children }: { children: string }) {
  return (
    <span className="mb-1 hidden text-xs font-bold tracking-[0.08em] text-muted uppercase max-[600px]:block">
      {children}
    </span>
  );
}
