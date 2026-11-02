//go:build modal

package sandboxexec

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/podium-ade/podium/internal/node/docker"
	"github.com/podium-ade/podium/internal/sandbox/modal"
	"github.com/podium-ade/podium/pkg/spec"
)

// shotJS drives the browser sidecar over the DevTools protocol, as the agent runtime's
// browser tools do, and writes a screenshot of a page. Chrome refuses a Host header that is
// not an address, so the sidecar's name is resolved first.
const shotJS = `
const dns = require("node:dns/promises"), fs = require("node:fs");
const [cdp, page, out] = process.argv.slice(2);
(async () => {
  const u = new URL(cdp);
  const { address } = await dns.lookup(u.hostname, { family: 4 });
  const base = "http://" + address + ":" + u.port;
  const t = await (await fetch(base + "/json/new?" + encodeURIComponent("about:blank"), { method: "PUT" })).json();
  const ws = new WebSocket(t.webSocketDebuggerUrl.replace(/\/\/[^/]+/, "//" + address + ":" + u.port));
  let id = 0; const wait = new Map(); const events = [];
  const call = (method, params = {}) => new Promise((res) => { wait.set(++id, res); ws.send(JSON.stringify({ id, method, params })); });
  ws.onmessage = (m) => { const d = JSON.parse(m.data); if (d.id && wait.has(d.id)) wait.get(d.id)(d); else events.push(d.method); };
  await new Promise((r) => (ws.onopen = r));
  await call("Page.enable");
  await call("Page.navigate", { url: page });
  for (let i = 0; i < 100 && !events.includes("Page.loadEventFired"); i++) await new Promise((r) => setTimeout(r, 100));
  const shot = await call("Page.captureScreenshot", { format: "png" });
  fs.writeFileSync(out, Buffer.from(shot.result.data, "base64"));
  console.log("screenshot", fs.statSync(out).size);
  process.exit(0);
})().catch((e) => { console.error(e); process.exit(1); });
`

// TestModalSidecarsBesideAProcessTask runs a task in its own image in a Modal vm sandbox, with
// a privileged Docker daemon beside it sharing its workspace and a browser beside it: the
// daemon serves a file from the workspace, the task serves a page itself, and the browser
// reaches the task as "task" and takes a screenshot of it. It needs MODAL_CONFORMANCE=1,
// BOAT_TEST_AGENT, and MODAL_SIDECAR_IMAGE: a Modal image holding dockerd, node and GNU
// coreutils, such as one published from `FROM docker:29-dind` plus
// `apk add nodejs bash setpriv coreutils util-linux shadow` with examples/modal/publish_image.py.
func TestModalSidecarsBesideAProcessTask(t *testing.T) {
	agent, image := os.Getenv("BOAT_TEST_AGENT"), os.Getenv("MODAL_SIDECAR_IMAGE")
	if os.Getenv("MODAL_CONFORMANCE") == "" || agent == "" || image == "" {
		t.Skip("MODAL_CONFORMANCE, BOAT_TEST_AGENT and MODAL_SIDECAR_IMAGE are needed")
	}
	p, err := modal.New(modal.Config{App: "podium-sandbox-dev", Runtime: "vm", PrivilegedSidecars: true, CPU: 2, MemoryMB: 4096})
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Options{Provider: p, DataDir: t.TempDir(), Agent: func() ([]byte, error) { return os.ReadFile(agent) },
		Logger: slog.New(slog.NewTextHandler(os.Stderr, nil))})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	script := strings.Join([]string{
		"set -e",
		"mkdir -p /workspace/site /workspace/.podium/artifacts",
		"echo '<h1 style=\"font:64px sans-serif\">podium on modal</h1>' > /workspace/site/index.html",
		"cat > /workspace/shot.js <<'JS'\n" + shotJS + "\nJS",
		// A container on the daemon beside the task, from a bind mount of the workspace:
		// the daemon resolves /workspace on its own filesystem.
		"docker run -d -p 8080:80 -v /workspace/site:/usr/share/nginx/html:ro nginx:alpine >/dev/null",
		"for i in $(seq 60); do wget -qO- http://dind:8080 2>/dev/null | grep -q 'podium on modal' && break; sleep 1; done",
		"wget -qO- http://dind:8080 | grep -q 'podium on modal' && echo daemon-serves-workspace",
		// A page the task serves itself, which the browser reaches as task.
		"(cd /workspace/site && exec node -e \"require('http').createServer((q,r)=>r.end(require('fs').readFileSync('index.html'))).listen(3000,'0.0.0.0')\") &",
		"sleep 1",
		"node /workspace/shot.js http://chrome:9222 http://task:3000 /workspace/.podium/artifacts/shot.png",
	}, "\n")
	sp := spec.TaskSpec{
		Image:   "modal:" + image,
		Command: []string{"sh", "-c", script},
		Env:     map[string]string{"DOCKER_HOST": "tcp://dind:2375"},
		Sandbox: &spec.Sandbox{Modal: &spec.ModalSandbox{Runtime: "vm"}},
		Sidecars: map[string]spec.Sidecar{
			"dind": {
				Image: "docker:29-dind", Env: map[string]string{"DOCKER_TLS_CERTDIR": ""}, Command: []string{"--tls=false"},
				Privileged: true, ShareWorkspace: true,
				Readiness: spec.Readiness{TCPPort: 2375, Timeout: spec.Duration(2 * time.Minute)},
			},
			"chrome": {
				Image: "chromedp/headless-shell:156.0.8078.12", Command: []string{"--disable-dev-shm-usage"},
				Readiness: spec.Readiness{Command: []string{"bash", "-c", "exec 3<>/dev/tcp/127.0.0.1/9223"}, Timeout: spec.Duration(2 * time.Minute)},
			},
		},
		Timeout: spec.Duration(10 * time.Minute),
	}
	sp.ApplyDefaults()
	if err := sp.Validate(); err != nil {
		t.Fatal(err)
	}
	up := &memUploader{got: map[string]string{}}
	ch := make(chan docker.Event, 256)
	var c collector
	done := make(chan struct{})
	go func() { c.run(ch); close(done) }()
	start := time.Now()
	res, err := e.Run(ctx, docker.Request{
		TaskID: "modal-sidecars-1", LeaseID: "lease-d-" + time.Now().Format("150405.000"), Spec: sp, Artifacts: up,
	}, ch)
	t.Cleanup(func() { _ = e.Teardown(context.Background(), "modal-sidecars-1", false) })
	close(ch)
	<-done
	t.Logf("ran in %s; stdout:\n%s\nstderr:\n%s", time.Since(start).Round(time.Second), c.logs(docker.StreamStdout), c.logs(docker.StreamStderr))
	if err != nil || res.ExitCode != 0 {
		t.Fatalf("exit %d, %v; events %v", res.ExitCode, err, c.kinds())
	}
	if !strings.Contains(c.logs(docker.StreamStdout), "daemon-serves-workspace") {
		t.Error("the daemon beside the task did not serve the workspace")
	}
	png := up.got["shot.png"]
	if !bytes.HasPrefix([]byte(png), []byte("\x89PNG")) || len(png) < 2000 {
		t.Fatalf("no screenshot came back as an artifact (%d bytes); artifacts %v", len(png), keys(up.got))
	}
	if dir := os.Getenv("MODAL_SIDECAR_SHOT"); dir != "" {
		_ = os.WriteFile(dir, []byte(png), 0o600)
	}
}

func keys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
