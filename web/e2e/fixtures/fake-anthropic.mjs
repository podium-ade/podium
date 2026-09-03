// A stand-in for api.anthropic.com, for the agent settings e2e.
//
// It answers the one endpoint SetProviderKey calls, GET /v1/models, exactly the way the live
// API does — and the part that matters is the failure: an invalid key comes back **400**,
// not 401. Verified against the real API; a fake that answered 401 would let a bug through.
//
// Start it before podium-agent, because the conductor reads PODIUM_AGENT_ANTHROPIC_BASE_URL
// once at startup:
//
//   FAKE_ANTHROPIC_PORT=18999 node web/e2e/fixtures/fake-anthropic.mjs &
//   PODIUM_AGENT_ANTHROPIC_BASE_URL=http://127.0.0.1:18999 ./bin/podium-agent &
//
// FAKE_ANTHROPIC_KEY is the only key it accepts. It is an obvious fake and it is not a
// credential: nothing here talks to Anthropic.

import { createServer } from "node:http";

const PORT = Number(process.env.FAKE_ANTHROPIC_PORT ?? 18999);
const GOOD = process.env.FAKE_ANTHROPIC_KEY ?? "sk-ant-test-good";

const MODELS = {
  data: [
    { id: "claude-opus-5", display_name: "Claude Opus 5", type: "model" },
    { id: "claude-sonnet-5", display_name: "Claude Sonnet 5", type: "model" },
    { id: "claude-haiku-4-5", display_name: "Claude Haiku 4.5", type: "model" },
  ],
  first_id: "claude-opus-5",
  last_id: "claude-haiku-4-5",
  has_more: false,
};

const INVALID = {
  type: "error",
  error: { type: "authentication_error", message: "API key is invalid." },
};

const server = createServer((req, res) => {
  const path = new URL(req.url, `http://${req.headers.host}`).pathname;
  const key = req.headers["x-api-key"];
  const version = req.headers["anthropic-version"];
  console.log(`${req.method} ${path} key=${key ? "present" : "absent"} version=${version}`);

  res.setHeader("Content-Type", "application/json");
  if (req.method !== "GET" || path !== "/v1/models") {
    res.writeHead(404);
    res.end(JSON.stringify({ type: "error", error: { type: "not_found_error", message: path } }));
    return;
  }
  if (key !== GOOD) {
    res.writeHead(400);
    res.end(JSON.stringify(INVALID));
    return;
  }
  res.writeHead(200);
  res.end(JSON.stringify(MODELS));
});

server.listen(PORT, "127.0.0.1", () => {
  console.log(`fake anthropic on http://127.0.0.1:${PORT}, accepting one key`);
});

for (const signal of ["SIGINT", "SIGTERM"]) {
  process.on(signal, () => server.close(() => process.exit(0)));
}
