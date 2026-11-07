import { Building2, Users } from "lucide-react";
import linear from "../../assets/marks/linear.svg";
import slack from "../../assets/marks/slack.svg";

/**
 * OriginMark is the sign of where a list of chats lives, on a 22px tile inverted against the
 * theme as the product mark is: a solid tile in the text colour with the glyph cut out of it
 * in the raised grey, so a dark tile in the light theme and a light one in the dark. Podium
 * and GitHub are drawn here, in currentColor, for that reason; Slack and Linear keep their
 * own colours, which read on either tile. The source's name is always beside it or in the
 * tooltip, so it has no label.
 */
export function OriginMark({ origin }: { origin: string }) {
  return (
    <span
      aria-hidden
      className="grid size-[22px] shrink-0 place-items-center rounded-sm bg-fg text-raised"
    >
      <Glyph origin={origin} />
    </span>
  );
}

function Glyph({ origin }: { origin: string }) {
  switch (origin) {
    case "web":
      return <PodiumGlyph />;
    case "github":
      return (
        <svg viewBox="0 0 24 24" className="block size-[13px]">
          <path fill="currentColor" d="M12 .297c-6.63 0-12 5.373-12 12 0 5.303 3.438 9.8 8.205 11.385.6.113.82-.258.82-.577 0-.285-.01-1.04-.015-2.04-3.338.724-4.042-1.61-4.042-1.61C4.422 18.07 3.633 17.7 3.633 17.7c-1.087-.744.084-.729.084-.729 1.205.084 1.838 1.236 1.838 1.236 1.07 1.835 2.809 1.305 3.495.998.108-.776.417-1.305.76-1.605-2.665-.3-5.466-1.332-5.466-5.93 0-1.31.465-2.38 1.235-3.22-.135-.303-.54-1.523.105-3.176 0 0 1.005-.322 3.3 1.23.96-.267 1.98-.399 3-.405 1.02.006 2.04.138 3 .405 2.28-1.552 3.285-1.23 3.285-1.23.645 1.653.24 2.873.12 3.176.765.84 1.23 1.91 1.23 3.22 0 4.61-2.805 5.625-5.475 5.92.42.36.81 1.096.81 2.22 0 1.606-.015 2.896-.015 3.286 0 .315.21.69.825.57C20.565 22.092 24 17.592 24 12.297c0-6.627-5.373-12-12-12" />
        </svg>
      );
    case "slack":
      return <img src={slack} alt="" className="block size-[13px]" />;
    case "linear":
      return <img src={linear} alt="" className="block size-[13px]" />;
    case "shared":
      return <Users className="size-[13px]" strokeWidth={2} />;
    default:
      return <Building2 className="size-[13px]" strokeWidth={2} />;
  }
}

/** PodiumGlyph is PodiumMark without its tile: the queued sessions and the live terminal in
 * the glyph colour, the prompt cut out of them in the tile's own (the text colour). */
function PodiumGlyph() {
  return (
    <svg viewBox="12 12 40 40" className="block size-[15px]">
      <rect x="20" y="12" width="24" height="4" rx="2" opacity=".45" fill="currentColor" />
      <rect x="16" y="18" width="32" height="4" rx="2" opacity=".7" fill="currentColor" />
      <rect x="12" y="24" width="40" height="28" rx="4" fill="currentColor" />
      <path
        d="M18.4 33l6 5-6 5"
        fill="none"
        strokeWidth="3.2"
        strokeLinecap="round"
        strokeLinejoin="round"
        className="stroke-fg"
      />
      <g className="fill-fg">
        <rect x="28.8" y="38.5" width="5" height="4.5" rx="1.2" />
        <rect x="35.4" y="38.5" width="5" height="4.5" rx="1.2" opacity=".6" />
        <rect x="42" y="38.5" width="5" height="4.5" rx="1.2" opacity=".3" />
      </g>
    </svg>
  );
}
