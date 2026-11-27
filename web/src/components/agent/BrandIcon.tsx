import { Plug } from "lucide-react";
import claude from "../../assets/marks/claude.svg";
import gemini from "../../assets/marks/gemini.svg";
import github from "../../assets/marks/github.svg";
import grok from "../../assets/marks/grok.svg";
import linear from "../../assets/marks/linear.svg";
import notion from "../../assets/marks/notion.svg";
import openai from "../../assets/marks/openai.svg";
import sentry from "../../assets/marks/sentry.svg";
import slack from "../../assets/marks/slack.svg";
import stripe from "../../assets/marks/stripe.svg";
import { cn } from "../../lib/utils";

/**
 * BrandIcon is a commercial mark from the design, drawn at 24px on the white tile.
 * The product name is always in the text beside it, so the image has no alt.
 */
const MARKS: Record<string, string> = {
  claude,
  gemini,
  github,
  grok,
  linear,
  notion,
  openai,
  sentry,
  slack,
  stripe,
};

const TILE = {
  row: { box: "size-10", img: "h-6 w-6 max-h-6" },
  dialog: { box: "size-8", img: "h-[18px] w-[18px] max-h-[18px]" },
  detail: { box: "size-14", img: "h-8 w-8 max-h-8" },
} as const;

export function BrandTile({
  name,
  size = "row",
}: {
  name: string;
  size?: keyof typeof TILE;
}) {
  const tile = TILE[size];
  const src = MARKS[name];
  return (
    <span
      aria-hidden
      className={cn(
        "grid shrink-0 place-items-center rounded-sm border border-line-strong bg-white",
        tile.box,
      )}
    >
      {src ? (
        <img src={src} alt="" className={cn("block h-auto", tile.img)} />
      ) : name === "figma" ? (
        <FigmaMark className={tile.img} />
      ) : (
        <Plug className={cn("text-[#222326]", tile.img)} />
      )}
    </span>
  );
}

function FigmaMark({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" className={cn("block", className)} aria-hidden>
      <path fill="#F24E1E" d="M8 24a4 4 0 0 0 4-4v-4H8a4 4 0 1 0 0 8z" />
      <path fill="#FF7262" d="M8 16h4v-4H8a4 4 0 1 0 0 4z" />
      <path fill="#A259FF" d="M8 8h4V0H8a4 4 0 1 0 0 8z" />
      <path fill="#1ABCFE" d="M16 8a4 4 0 1 0 0-8h-4v8h4z" />
      <circle fill="#0ACF83" cx="16" cy="12" r="4" />
    </svg>
  );
}

/**
 * The glyphs are the same marks in one colour, the text's, for where a mark sits inline beside
 * a label as an icon would rather than on the white tile.
 */
export function GitHubGlyph({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" className={cn("size-4 shrink-0", className)} aria-hidden>
      <path d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
    </svg>
  );
}

export function LinearGlyph({ className }: { className?: string }) {
  return (
    <svg viewBox="0 0 24 24" fill="currentColor" className={cn("size-4 shrink-0", className)} aria-hidden>
      <path d="M2.886 4.18A11.982 11.982 0 0 1 11.99 0C18.624 0 24 5.376 24 12.009c0 3.64-1.62 6.903-4.18 9.105L2.887 4.18ZM1.817 5.626l16.556 16.556c-.524.33-1.075.62-1.65.866L.951 7.277c.247-.575.537-1.126.866-1.65ZM.322 9.163l14.515 14.515c-.71.172-1.443.282-2.195.322L0 11.358a12 12 0 0 1 .322-2.195Zm-.17 4.862 9.823 9.824a12.02 12.02 0 0 1-9.824-9.824Z" />
    </svg>
  );
}
