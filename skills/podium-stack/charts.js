// Writes the Fernhill sales charts a seeded `sales-charts` turn attaches to its chat message:
// three real PNGs of different shapes (wide, tall, very wide) and a CSV, into the turn's
// artifacts directory. Plain Node — no canvas, no Python — so it runs in the bare runtime image.
const zlib = require("zlib");
const fs = require("fs");
const dir = "/workspace/.podium/artifacts";
fs.mkdirSync(dir, { recursive: true });

const table = new Int32Array(256).map((_, n) => {
  let c = n;
  for (let k = 0; k < 8; k++) c = c & 1 ? 0xedb88320 ^ (c >>> 1) : c >>> 1;
  return c;
});
const crc = (buf) => {
  let c = -1;
  for (const x of buf) c = table[(c ^ x) & 255] ^ (c >>> 8);
  return (c ^ -1) >>> 0;
};
const chunk = (type, data) => {
  const len = Buffer.alloc(4);
  len.writeUInt32BE(data.length);
  const body = Buffer.concat([Buffer.from(type), data]);
  const sum = Buffer.alloc(4);
  sum.writeUInt32BE(crc(body));
  return Buffer.concat([len, body, sum]);
};
// A bar chart: one bar per value (0..1 of the height), coloured, on white.
const png = (w, h, bars, [r, g, b]) => {
  const stride = w * 3 + 1;
  const raw = Buffer.alloc(stride * h, 255);
  const slot = Math.floor(w / bars.length);
  for (let y = 0; y < h; y++) {
    raw[y * stride] = 0;
    for (let x = 0; x < w; x++) {
      const v = bars[Math.min(bars.length - 1, Math.floor(x / slot))];
      if (h - y <= v * h && x % slot > slot * 0.15) {
        const o = y * stride + 1 + x * 3;
        raw[o] = r;
        raw[o + 1] = g;
        raw[o + 2] = b;
      }
    }
  }
  const head = Buffer.alloc(13);
  head.writeUInt32BE(w);
  head.writeUInt32BE(h, 4);
  head[8] = 8; // bit depth
  head[9] = 2; // truecolour
  return Buffer.concat([
    Buffer.from([137, 80, 78, 71, 13, 10, 26, 10]),
    chunk("IHDR", head),
    chunk("IDAT", zlib.deflateSync(raw)),
    chunk("IEND", Buffer.alloc(0)),
  ]);
};

fs.writeFileSync(`${dir}/sales-by-day.png`, png(800, 400, [0.4, 0.55, 0.7, 0.5, 0.9, 0.8, 0.3], [46, 125, 50]));
fs.writeFileSync(`${dir}/top-skus.png`, png(400, 600, [0.9, 0.7, 0.5, 0.35, 0.2], [21, 101, 192]));
fs.writeFileSync(`${dir}/refunds-trend.png`, png(1200, 300, [0.2, 0.25, 0.15, 0.3, 0.1, 0.05, 0.12, 0.08], [198, 40, 40]));
fs.writeFileSync(
  `${dir}/weekly-summary.csv`,
  "day,orders,total_cad\nMon,41,3120.50\nTue,38,2890.10\nWed,52,4011.75\nThu,47,3560.00\nFri,63,5122.40\n",
);
console.log("wrote sales-by-day.png, top-skus.png, refunds-trend.png and weekly-summary.csv");
