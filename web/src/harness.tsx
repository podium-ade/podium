import { createRoot } from "react-dom/client";
import { create } from "@bufbuild/protobuf";
import { ChatAttachmentSchema } from "./gen/podium/agent/v1/agent_pb";
import { ToastHost } from "./components/Toast";
import { ChatAttachments } from "./components/agent/ChatAttachments";
import "./index.css";

const q = new URLSearchParams(location.search);
const n = Number(q.get("n") ?? 5);
const fail = (q.get("fail") ?? "").split(",").filter(Boolean);
const longName = q.get("long") === "1";
const csv = q.get("csv") === "1";
const dims = [[800, 450], [400, 900], [1600, 400], [600, 600], [300, 200], [1200, 800], [500, 500], [900, 300]];
const hue = [210, 20, 120, 300, 50, 170, 0, 260];

function drawPNG(i: number): Promise<Blob> {
  const [w, h] = dims[i % dims.length];
  const c = document.createElement("canvas");
  c.width = w; c.height = h;
  const g = c.getContext("2d")!;
  g.fillStyle = `hsl(${hue[i % 8]} 60% 55%)`; g.fillRect(0, 0, w, h);
  g.fillStyle = "#fff"; g.font = `bold ${Math.min(w, h) / 4}px sans-serif`;
  g.fillText(`#${i + 1}  ${w}x${h}`, 20, h / 2);
  g.strokeStyle = "#000"; g.lineWidth = 6; g.strokeRect(3, 3, w - 6, h - 6);
  return new Promise((res) => c.toBlob((b) => res(b!), "image/png"));
}
const delay = Number(q.get("delay") ?? 0);
window.fetch = (async (url: string) => {
  const m = /art_(\d+)/.exec(String(url));
  await new Promise((r) => setTimeout(r, delay * (m ? Number(m[1]) : 0)));
  if (!m) return new Response("no", { status: 404 });
  if (fail.includes(m[1])) return new Response("gone", { status: 404 });
  return new Response(await drawPNG(Number(m[1]) - 1), { status: 200 });
}) as typeof fetch;

const atts = Array.from({ length: n }, (_, i) =>
  create(ChatAttachmentSchema, {
    artifactId: `art_${i + 1}`,
    name: longName ? `${"very-long-chart-name-".repeat(15)}${i + 1}.png` : `chart-${i + 1}.png`,
    contentType: "image/png",
    sizeBytes: BigInt(90000 + i * 12345),
  }),
);
if (csv) atts.push(create(ChatAttachmentSchema, { artifactId: "art_csv", name: "report.csv", contentType: "text/csv", sizeBytes: 4096n }));

createRoot(document.getElementById("root")!).render(
  <ToastHost>
    <div className="mx-auto flex max-w-3xl flex-col gap-3 p-4 text-fg">
      <p className="text-sm">Here are the charts you asked for.</p>
      <div className="max-w-[min(36rem,85%)] self-end rounded-2xl rounded-br-md bg-run-bg px-4 py-2.5">
        <p className="text-sm">user message with attachments</p>
        <ChatAttachments attachments={atts} />
      </div>
      <button id="after" className="self-start rounded border px-2 py-1 text-sm">after</button>
    </div>
  </ToastHost>,
);
