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
